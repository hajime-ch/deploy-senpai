// Package selfupdate replaces the running binary with a release from GitHub.
//
// Replacing a running executable is safe on Unix: the rename swaps the
// directory entry while the live process keeps its inode, so a running server
// carries on off the old file until it is restarted. Restarting is left to the
// caller — doing it as a side effect of an update is too surprising for a tool
// that manages other people's deployments.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// maxAssetSize bounds what we are willing to download and decompress. The real
// asset is a few MB; anything near this is not a deploy-senpai release.
const maxAssetSize = 256 << 20

// binaryName is the entry we extract from the release archive.
const binaryName = "deploy-senpai"

// Update describes a release asset that can replace the running binary.
type Update struct {
	Tag         string
	AssetName   string
	DownloadURL string
	Digest      string
}

// Version is the release version without the leading "v" of the tag.
func (u *Update) Version() string { return strings.TrimPrefix(u.Tag, "v") }

// Updater resolves and applies releases.
type Updater struct {
	Repo        string
	APIBase     string
	HTTPClient  *http.Client
	OS, Arch    string
	ExecPath    string
	Version     string
	Force       bool
	InContainer func() bool
}

// New returns an Updater for the running binary.
func New(repo, version string) (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating the running binary: %w", err)
	}
	return &Updater{
		Repo:        repo,
		APIBase:     "https://api.github.com",
		HTTPClient:  &http.Client{Timeout: 60 * time.Second},
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		ExecPath:    exe,
		Version:     version,
		InContainer: inContainer,
	}, nil
}

func inContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// Preflight reports why this binary must not update itself, before anything is
// downloaded. Every case here is one where guessing would be worse than
// stopping: the update would be undone, escalate silently, or fight a package
// manager.
func (u *Updater) Preflight() error {
	if u.InContainer != nil && u.InContainer() {
		return fmt.Errorf("running inside a container: update the image tag instead of the binary")
	}

	if u.Version == "dev" && !u.Force {
		return fmt.Errorf("this is a dev build, not a release; re-run with --force to replace it anyway")
	}

	// Only the binary itself matters here, not its parents: on macOS /var is a
	// symlink to /private/var, so resolving the whole path flags every install
	// under /tmp. A symlinked binary is the package-manager case (Homebrew),
	// where overwriting the link fights whatever owns it.
	//
	// Best-effort: on Linux os.Executable() reads /proc/self/exe, which is
	// already resolved, so this can only fire where the symlink survives.
	if fi, err := os.Lstat(u.ExecPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, _ := filepath.EvalSymlinks(u.ExecPath)
		return fmt.Errorf("%s is a symlink to %s, which usually means a package manager owns it; update it with that instead",
			u.ExecPath, target)
	}

	// Probe for write access the only way that is not a lie on every
	// filesystem: try to create a file where the new binary has to land.
	dir := filepath.Dir(u.ExecPath)
	probe, err := os.CreateTemp(dir, ".deploy-senpai-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w (try: sudo %s self-update)", dir, err, u.ExecPath)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return nil
}

// Check resolves a release — the latest one, or a specific tag — and the asset
// matching this platform.
func (u *Updater) Check(ctx context.Context, tag string) (*Update, error) {
	endpoint := u.APIBase + "/repos/" + u.Repo + "/releases/latest"
	if tag != "" {
		endpoint = u.APIBase + "/repos/" + u.Repo + "/releases/tags/" + tag
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching release metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound && tag != "" {
			return nil, fmt.Errorf("no release tagged %s in %s", tag, u.Repo)
		}
		return nil, fmt.Errorf("fetching release metadata: %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("parsing release metadata: %w", err)
	}
	if release.TagName == "" {
		return nil, fmt.Errorf("release metadata has no tag")
	}

	want := fmt.Sprintf("%s-%s-%s.tar.gz", binaryName, u.OS, u.Arch)
	for _, a := range release.Assets {
		if a.Name == want {
			return &Update{Tag: release.TagName, AssetName: a.Name, DownloadURL: a.URL, Digest: a.Digest}, nil
		}
	}
	return nil, fmt.Errorf("release %s has no asset for %s/%s (expected %s)", release.TagName, u.OS, u.Arch, want)
}

// Apply downloads, verifies and installs the update, returning the version the
// installed binary reports. Nothing touches ExecPath until the download has
// been checksummed and the new binary has proven it runs.
func (u *Updater) Apply(ctx context.Context, up *Update) (string, error) {
	// Stage in the target's own directory: a rename across filesystems fails,
	// so /tmp is not an option.
	dir := filepath.Dir(u.ExecPath)

	archive, err := u.download(ctx, up, dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archive) }()

	staged, err := extractBinary(archive, dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(staged) }()

	reported, err := smokeTest(ctx, staged, up)
	if err != nil {
		return "", err
	}

	if err := backup(u.ExecPath); err != nil {
		return "", err
	}
	if err := os.Rename(staged, u.ExecPath); err != nil {
		return "", fmt.Errorf("installing %s: %w", u.ExecPath, err)
	}
	return reported, nil
}

// download fetches the asset into dir and verifies the digest the release
// advertises. That digest and the file come from the same place, so this
// catches a corrupted or truncated download — not a compromised release.
func (u *Updater) download(ctx context.Context, up *Update, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", up.AssetName, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", up.AssetName, resp.Status)
	}

	f, err := os.CreateTemp(dir, ".deploy-senpai-download-*")
	if err != nil {
		return "", err
	}
	name := f.Name()

	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(resp.Body, maxAssetSize))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("downloading %s: %w", up.AssetName, cmpErr(copyErr, closeErr))
	}

	if up.Digest == "" {
		return name, nil
	}
	want := strings.TrimPrefix(up.Digest, "sha256:")
	if got := hex.EncodeToString(hasher.Sum(nil)); got != want {
		_ = os.Remove(name)
		return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s", up.AssetName, want, got)
	}
	return name, nil
}

// extractBinary pulls exactly the deploy-senpai entry out of the archive.
// Other entries are ignored rather than written, so a crafted archive cannot
// place files elsewhere.
func extractBinary(archive, dir string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("reading archive: %w", err)
	}
	defer func() { _ = zr.Close() }()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("archive did not contain a %s binary", binaryName)
		}
		if err != nil {
			return "", fmt.Errorf("reading archive: %w", err)
		}
		if filepath.Base(hdr.Name) != binaryName || hdr.Typeflag != tar.TypeReg {
			continue
		}

		out, err := os.CreateTemp(dir, ".deploy-senpai-staged-*")
		if err != nil {
			return "", err
		}
		name := out.Name()
		_, copyErr := io.Copy(out, io.LimitReader(tr, maxAssetSize))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("extracting %s: %w", binaryName, cmpErr(copyErr, closeErr))
		}
		if err := os.Chmod(name, 0o755); err != nil {
			_ = os.Remove(name)
			return "", err
		}
		return name, nil
	}
}

// smokeTest runs the staged binary before it is trusted, which catches a
// wrong-architecture or mismatched build while it is still a temp file.
func smokeTest(ctx context.Context, staged string, up *Update) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, staged, "version").Output()
	if err != nil {
		return "", fmt.Errorf("downloaded binary would not run: %w", err)
	}
	reported := strings.TrimSpace(string(out))

	// Releases before v0.4.0 were built without -X main.version and cannot
	// identify themselves. Rolling back to one is legitimate, so accept it.
	if reported == "dev" {
		return reported, nil
	}
	if reported != up.Version() {
		return "", fmt.Errorf("downloaded binary reports version %q, expected %q", reported, up.Version())
	}
	return reported, nil
}

// backup copies the current binary aside so a bad update can be undone by hand.
func backup(execPath string) error {
	current, err := os.ReadFile(execPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading the current binary: %w", err)
	}
	if err := os.WriteFile(execPath+".old", current, 0o755); err != nil {
		return fmt.Errorf("keeping a copy of the current binary: %w", err)
	}
	return nil
}

func cmpErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
