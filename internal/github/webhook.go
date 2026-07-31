package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hajime-ch/deploy-senpai/internal/config"
)

// WebhookEvent types
const (
	EventPush   = "push"
	EventDelete = "delete"
	EventPing   = "ping"
)

// PushEvent represents a GitHub push webhook payload
type PushEvent struct {
	Ref        string     `json:"ref"`
	Before     string     `json:"before"`
	After      string     `json:"after"`
	Created    bool       `json:"created"`
	Deleted    bool       `json:"deleted"`
	Repository Repository `json:"repository"`
	Pusher     Pusher     `json:"pusher"`
	HeadCommit *Commit    `json:"head_commit"`
}

// DeleteEvent represents a GitHub delete webhook payload
type DeleteEvent struct {
	Ref        string     `json:"ref"`
	RefType    string     `json:"ref_type"` // "branch" or "tag"
	Repository Repository `json:"repository"`
}

// Repository represents the repository information
type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	Owner    Owner  `json:"owner"`
	CloneURL string `json:"clone_url"`
	SSHURL   string `json:"ssh_url"`
}

// Owner represents the repository owner
type Owner struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// Pusher represents who triggered the push
type Pusher struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Commit represents a commit
type Commit struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	Author    Author `json:"author"`
}

// Author represents commit author
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// WebhookPayload is a generic container for webhook data
type WebhookPayload struct {
	Event      string
	Delivery   string
	Signature  string
	Repository string
	Branch     string
	CommitSHA  string
	Deleted    bool
	IsTag      bool
	Raw        []byte
}

// ParseWebhook parses and validates a GitHub webhook request
func ParseWebhook(r *http.Request, secret string) (*WebhookPayload, error) {
	// Read the body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}

	// Get headers
	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")
	signature := r.Header.Get("X-Hub-Signature-256")

	if event == "" {
		return nil, fmt.Errorf("missing X-GitHub-Event header")
	}

	// Fail closed: an unconfigured secret means no request can be authenticated,
	// not that every request is trusted.
	if secret == "" {
		return nil, fmt.Errorf("webhook secret is not configured, refusing to process webhook")
	}
	if signature == "" {
		return nil, fmt.Errorf("missing X-Hub-Signature-256 header")
	}
	if !validateSignature(body, signature, secret) {
		return nil, fmt.Errorf("invalid webhook signature")
	}

	payload := &WebhookPayload{
		Event:    event,
		Delivery: delivery,
		Raw:      body,
	}

	// Parse event-specific data
	switch event {
	case EventPush:
		var push PushEvent
		if err := json.Unmarshal(body, &push); err != nil {
			return nil, fmt.Errorf("parsing push event: %w", err)
		}
		payload.Repository = push.Repository.Name
		payload.Deleted = push.Deleted

		if IsTag(push.Ref) {
			payload.IsTag = true
			payload.Branch = extractTagName(push.Ref)
			payload.CommitSHA = extractTagName(push.Ref)
		} else {
			payload.Branch = extractBranchName(push.Ref)
			if push.HeadCommit != nil {
				payload.CommitSHA = push.HeadCommit.ID
			} else {
				payload.CommitSHA = push.After
			}
		}

	case EventDelete:
		var del DeleteEvent
		if err := json.Unmarshal(body, &del); err != nil {
			return nil, fmt.Errorf("parsing delete event: %w", err)
		}
		if del.RefType == "branch" {
			payload.Repository = del.Repository.Name
			payload.Branch = del.Ref
			payload.Deleted = true
		}

	case EventPing:
		// Ping events don't need additional parsing
	}

	// The ref reaches deployment paths and the rendered compose file, so it is
	// validated here rather than trusted because it arrived over a signed request.
	if payload.Branch != "" {
		if err := config.ValidateRefName(payload.Branch); err != nil {
			return nil, fmt.Errorf("invalid ref %q: %w", payload.Branch, err)
		}
	}

	return payload, nil
}

// validateSignature checks the HMAC-SHA256 signature
func validateSignature(body []byte, signature, secret string) bool {
	// Signature format: sha256=xxxxx
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig := strings.TrimPrefix(signature, "sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expected))
}

// extractBranchName extracts the branch name from a ref
func extractBranchName(ref string) string {
	// refs/heads/main -> main
	// refs/heads/feature/foo -> feature/foo
	return strings.TrimPrefix(ref, "refs/heads/")
}

// extractTagName extracts the tag name from a ref
func extractTagName(ref string) string {
	// refs/tags/v1.2.3 -> v1.2.3
	return strings.TrimPrefix(ref, "refs/tags/")
}

// IsBranch checks if the ref is a branch (not a tag)
func IsBranch(ref string) bool {
	return strings.HasPrefix(ref, "refs/heads/")
}

// IsTag checks if the ref is a tag
func IsTag(ref string) bool {
	return strings.HasPrefix(ref, "refs/tags/")
}
