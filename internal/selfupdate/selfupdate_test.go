package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBinary is a stand-in for a released build: it answers `version` with
// whatever we bake in, so the smoke test has something real to execute.
func fakeBinary(version string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\n[ \"$1\" = version ] && echo %s\n", version))
}

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	for _, c := range []interface{ Close() error }{tw, zw} {
		if err := c.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	return buf.Bytes()
}

type serverOpts struct {
	tag           string
	binaryVersion string
	assetName     string // defaults to the current platform's name
	digest        string // defaults to the real one
}

// newReleaseServer serves a GitHub-shaped release document plus its asset.
func newReleaseServer(t *testing.T, o serverOpts) *httptest.Server {
	t.Helper()
	if o.assetName == "" {
		o.assetName = fmt.Sprintf("deploy-senpai-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	}
	archive := tarGz(t, "deploy-senpai", fakeBinary(o.binaryVersion))
	sum := sha256.Sum256(archive)
	digest := o.digest
	if digest == "" {
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": o.tag,
			"assets": []map[string]string{{
				"name":                 o.assetName,
				"browser_download_url": srv.URL + "/asset",
				"digest":               digest,
			}},
		})
	})
	t.Cleanup(srv.Close)
	return srv
}

// newUpdater returns an updater whose "running binary" is a real executable in
// a temp dir, so Apply can rename over it the way it would in production.
func newUpdater(t *testing.T, srv *httptest.Server, currentVersion string) *Updater {
	t.Helper()
	execPath := filepath.Join(t.TempDir(), "deploy-senpai")
	if err := os.WriteFile(execPath, fakeBinary(currentVersion), 0o755); err != nil {
		t.Fatalf("writing current binary: %v", err)
	}
	return &Updater{
		Repo:        "hajime-ch/deploy-senpai",
		APIBase:     srv.URL,
		HTTPClient:  srv.Client(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		ExecPath:    execPath,
		Version:     currentVersion,
		InContainer: func() bool { return false },
	}
}

func reportedVersion(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		t.Fatalf("running %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

func TestApplyReplacesTheRunningBinary(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{tag: "v0.9.9", binaryVersion: "0.9.9"})
	u := newUpdater(t, srv, "0.4.0")

	up, err := u.Check(context.Background(), "")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if up.Tag != "v0.9.9" {
		t.Errorf("tag = %q, want %q", up.Tag, "v0.9.9")
	}

	newVersion, err := u.Apply(context.Background(), up)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if newVersion != "0.9.9" {
		t.Errorf("Apply returned %q, want %q", newVersion, "0.9.9")
	}
	if got := reportedVersion(t, u.ExecPath); got != "0.9.9" {
		t.Errorf("installed binary reports %q, want %q", got, "0.9.9")
	}
}

func TestApplyKeepsThePreviousBinary(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{tag: "v0.9.9", binaryVersion: "0.9.9"})
	u := newUpdater(t, srv, "0.4.0")

	up, _ := u.Check(context.Background(), "")
	if _, err := u.Apply(context.Background(), up); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := reportedVersion(t, u.ExecPath+".old"); got != "0.4.0" {
		t.Errorf("backup reports %q, want the replaced version %q", got, "0.4.0")
	}
}

func TestApplyRefusesOnChecksumMismatch(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{
		tag: "v0.9.9", binaryVersion: "0.9.9",
		digest: "sha256:" + strings.Repeat("d", 64),
	})
	u := newUpdater(t, srv, "0.4.0")

	up, _ := u.Check(context.Background(), "")
	_, err := u.Apply(context.Background(), up)
	if err == nil {
		t.Fatal("Apply succeeded despite a bad checksum")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error = %q, want it to mention the checksum", err)
	}
	if got := reportedVersion(t, u.ExecPath); got != "0.4.0" {
		t.Errorf("binary was replaced anyway: reports %q", got)
	}
}

func TestApplyRefusesABinaryReportingTheWrongVersion(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{tag: "v0.9.9", binaryVersion: "1.2.3"})
	u := newUpdater(t, srv, "0.4.0")

	up, _ := u.Check(context.Background(), "")
	_, err := u.Apply(context.Background(), up)
	if err == nil {
		t.Fatal("Apply installed a binary that reports a different version")
	}
	if !strings.Contains(err.Error(), "1.2.3") {
		t.Errorf("error = %q, want it to name the version the binary reported", err)
	}
	if got := reportedVersion(t, u.ExecPath); got != "0.4.0" {
		t.Errorf("binary was replaced anyway: reports %q", got)
	}
}

// Releases before v0.4.0 were built without -X main.version. Rolling back to
// one is legitimate, so an unstamped build must not be refused.
func TestApplyAcceptsAnUnstampedRelease(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{tag: "v0.3.0", binaryVersion: "dev"})
	u := newUpdater(t, srv, "0.4.0")

	up, _ := u.Check(context.Background(), "")
	if _, err := u.Apply(context.Background(), up); err != nil {
		t.Fatalf("Apply refused an unstamped release: %v", err)
	}
}

func TestCheckNamesThePlatformWhenNoAssetMatches(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{
		tag: "v0.9.9", binaryVersion: "0.9.9", assetName: "deploy-senpai-plan9-sparc.tar.gz",
	})
	u := newUpdater(t, srv, "0.4.0")

	_, err := u.Check(context.Background(), "")
	if err == nil {
		t.Fatal("Check succeeded with no asset for this platform")
	}
	if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("error = %q, want it to name %q", err, runtime.GOOS)
	}
}

func TestPreflightRefusesInsideAContainer(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{tag: "v0.9.9", binaryVersion: "0.9.9"})
	u := newUpdater(t, srv, "0.4.0")
	u.InContainer = func() bool { return true }

	err := u.Preflight()
	if err == nil {
		t.Fatal("Preflight allowed a self-update inside a container")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("error = %q, want it to point at updating the image instead", err)
	}
}

func TestPreflightRefusesADevBuildUnlessForced(t *testing.T) {
	srv := newReleaseServer(t, serverOpts{tag: "v0.9.9", binaryVersion: "0.9.9"})
	u := newUpdater(t, srv, "dev")

	if err := u.Preflight(); err == nil {
		t.Fatal("Preflight allowed replacing a dev build")
	}

	u.Force = true
	if err := u.Preflight(); err != nil {
		t.Errorf("Preflight refused a dev build even with --force: %v", err)
	}
}

func TestPreflightRefusesWhenTheTargetIsNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	srv := newReleaseServer(t, serverOpts{tag: "v0.9.9", binaryVersion: "0.9.9"})
	u := newUpdater(t, srv, "0.4.0")

	dir := filepath.Dir(u.ExecPath)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := u.Preflight()
	if err == nil {
		t.Fatal("Preflight allowed an update into a read-only directory")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("error = %q, want it to suggest sudo", err)
	}
}
