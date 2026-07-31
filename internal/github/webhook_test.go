package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateSignature(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"test": "payload"}`)

	// Generate valid signature
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	tests := []struct {
		name      string
		body      []byte
		signature string
		secret    string
		expected  bool
	}{
		{
			name:      "valid signature",
			body:      body,
			signature: validSig,
			secret:    secret,
			expected:  true,
		},
		{
			name:      "invalid signature",
			body:      body,
			signature: "sha256=invalid",
			secret:    secret,
			expected:  false,
		},
		{
			name:      "wrong secret",
			body:      body,
			signature: validSig,
			secret:    "wrong-secret",
			expected:  false,
		},
		{
			name:      "missing sha256 prefix",
			body:      body,
			signature: hex.EncodeToString(mac.Sum(nil)),
			secret:    secret,
			expected:  false,
		},
		{
			name:      "modified body",
			body:      []byte(`{"test": "modified"}`),
			signature: validSig,
			secret:    secret,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateSignature(tt.body, tt.signature, tt.secret)
			if result != tt.expected {
				t.Errorf("validateSignature() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractBranchName(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		expected string
	}{
		{
			name:     "simple branch",
			ref:      "refs/heads/main",
			expected: "main",
		},
		{
			name:     "feature branch",
			ref:      "refs/heads/feature/login",
			expected: "feature/login",
		},
		{
			name:     "no prefix",
			ref:      "main",
			expected: "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractBranchName(tt.ref)
			if result != tt.expected {
				t.Errorf("extractBranchName(%q) = %q, want %q", tt.ref, result, tt.expected)
			}
		})
	}
}

func TestExtractTagName(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		expected string
	}{
		{
			name:     "simple tag",
			ref:      "refs/tags/v1.0.0",
			expected: "v1.0.0",
		},
		{
			name:     "release tag",
			ref:      "refs/tags/release/1.0",
			expected: "release/1.0",
		},
		{
			name:     "no prefix",
			ref:      "v1.0.0",
			expected: "v1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTagName(tt.ref)
			if result != tt.expected {
				t.Errorf("extractTagName(%q) = %q, want %q", tt.ref, result, tt.expected)
			}
		})
	}
}

func TestIsBranch(t *testing.T) {
	tests := []struct {
		ref      string
		expected bool
	}{
		{"refs/heads/main", true},
		{"refs/heads/feature/login", true},
		{"refs/tags/v1.0.0", false},
		{"main", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			result := IsBranch(tt.ref)
			if result != tt.expected {
				t.Errorf("IsBranch(%q) = %v, want %v", tt.ref, result, tt.expected)
			}
		})
	}
}

func TestIsTag(t *testing.T) {
	tests := []struct {
		ref      string
		expected bool
	}{
		{"refs/tags/v1.0.0", true},
		{"refs/tags/release", true},
		{"refs/heads/main", false},
		{"main", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			result := IsTag(tt.ref)
			if result != tt.expected {
				t.Errorf("IsTag(%q) = %v, want %v", tt.ref, result, tt.expected)
			}
		})
	}
}

func TestParseWebhook(t *testing.T) {
	secret := "test-secret"

	t.Run("ping event", func(t *testing.T) {
		body := []byte(`{"zen": "test"}`)
		req := createSignedRequest(t, "ping", body, secret)

		payload, err := ParseWebhook(req, secret)
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		if payload.Event != EventPing {
			t.Errorf("Event = %q, want %q", payload.Event, EventPing)
		}
	})

	t.Run("push event", func(t *testing.T) {
		body := []byte(`{
			"ref": "refs/heads/feature/test",
			"deleted": false,
			"repository": {"name": "test-repo"},
			"head_commit": {"id": "abc123"}
		}`)
		req := createSignedRequest(t, "push", body, secret)

		payload, err := ParseWebhook(req, secret)
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		if payload.Event != EventPush {
			t.Errorf("Event = %q, want %q", payload.Event, EventPush)
		}
		if payload.Repository != "test-repo" {
			t.Errorf("Repository = %q, want %q", payload.Repository, "test-repo")
		}
		if payload.Branch != "feature/test" {
			t.Errorf("Branch = %q, want %q", payload.Branch, "feature/test")
		}
		if payload.CommitSHA != "abc123" {
			t.Errorf("CommitSHA = %q, want %q", payload.CommitSHA, "abc123")
		}
		if payload.Deleted {
			t.Error("Deleted should be false")
		}
	})

	t.Run("tag push event", func(t *testing.T) {
		body := []byte(`{
			"ref": "refs/tags/v1.2.3",
			"deleted": false,
			"repository": {"name": "test-repo"},
			"head_commit": {"id": "abc123def456"}
		}`)
		req := createSignedRequest(t, "push", body, secret)

		payload, err := ParseWebhook(req, secret)
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		if payload.Event != EventPush {
			t.Errorf("Event = %q, want %q", payload.Event, EventPush)
		}
		if !payload.IsTag {
			t.Error("IsTag should be true")
		}
		if payload.Branch != "v1.2.3" {
			t.Errorf("Branch = %q, want %q", payload.Branch, "v1.2.3")
		}
		if payload.CommitSHA != "v1.2.3" {
			t.Errorf("CommitSHA = %q, want %q (should be tag name)", payload.CommitSHA, "v1.2.3")
		}
		if payload.Repository != "test-repo" {
			t.Errorf("Repository = %q, want %q", payload.Repository, "test-repo")
		}
	})

	t.Run("push delete event", func(t *testing.T) {
		body := []byte(`{
			"ref": "refs/heads/old-branch",
			"deleted": true,
			"repository": {"name": "test-repo"}
		}`)
		req := createSignedRequest(t, "push", body, secret)

		payload, err := ParseWebhook(req, secret)
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		if !payload.Deleted {
			t.Error("Deleted should be true")
		}
	})

	t.Run("delete event", func(t *testing.T) {
		body := []byte(`{
			"ref": "old-branch",
			"ref_type": "branch",
			"repository": {"name": "test-repo"}
		}`)
		req := createSignedRequest(t, "delete", body, secret)

		payload, err := ParseWebhook(req, secret)
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		if payload.Event != EventDelete {
			t.Errorf("Event = %q, want %q", payload.Event, EventDelete)
		}
		if !payload.Deleted {
			t.Error("Deleted should be true")
		}
	})

	t.Run("missing event header", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader([]byte(`{}`)))

		_, err := ParseWebhook(req, secret)
		if err == nil {
			t.Error("Expected error for missing event header")
		}
	})

	t.Run("invalid signature", func(t *testing.T) {
		body := []byte(`{"test": "payload"}`)
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-Hub-Signature-256", "sha256=invalid")

		_, err := ParseWebhook(req, secret)
		if err == nil {
			t.Error("Expected error for invalid signature")
		}
	})

	t.Run("no secret configured is rejected", func(t *testing.T) {
		body := []byte(`{"ref": "refs/heads/main", "repository": {"name": "test"}}`)
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")

		// An unconfigured secret must fail closed, not skip verification.
		if _, err := ParseWebhook(req, ""); err == nil {
			t.Error("ParseWebhook() = nil, want error when no secret is configured")
		}
	})

	t.Run("no secret configured rejects even a signed request", func(t *testing.T) {
		body := []byte(`{"ref": "refs/heads/main", "repository": {"name": "test"}}`)
		req := createSignedRequest(t, "push", body, "some-other-secret")

		if _, err := ParseWebhook(req, ""); err == nil {
			t.Error("ParseWebhook() = nil, want error when no secret is configured")
		}
	})

	t.Run("ref with newline is rejected", func(t *testing.T) {
		body := []byte("{\"ref\": \"refs/heads/main\\n    privileged: true\", \"repository\": {\"name\": \"test\"}}")
		req := createSignedRequest(t, "push", body, secret)

		if _, err := ParseWebhook(req, secret); err == nil {
			t.Error("ParseWebhook() = nil, want error for ref containing a newline")
		}
	})

	t.Run("delete event ref with traversal is rejected", func(t *testing.T) {
		body := []byte(`{"ref": "../../etc", "ref_type": "branch", "repository": {"name": "test"}}`)
		req := createSignedRequest(t, "delete", body, secret)

		if _, err := ParseWebhook(req, secret); err == nil {
			t.Error("ParseWebhook() = nil, want error for ref containing ..")
		}
	})
}

func createSignedRequest(t *testing.T, event string, body []byte, secret string) *http.Request {
	t.Helper()

	req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "test-delivery-id")

	// Generate signature
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	req.Header.Set("X-Hub-Signature-256", sig)

	return req
}
