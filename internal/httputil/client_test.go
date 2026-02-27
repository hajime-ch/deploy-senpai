package httputil

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		// Private IPv4
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"169.254.1.1", true},
		{"0.0.0.0", true},

		// Public IPv4
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.1", false},
		{"172.32.0.1", false}, // Just outside 172.16-31 range

		// IPv6 private
		{"::1", true},
		{"fc00::1", true},
		{"fd00::1", true},
		{"fe80::1", true},

		// IPv6 public
		{"2001:4860:4860::8888", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			result := isPrivateIP(ip)
			if result != tt.expected {
				t.Errorf("isPrivateIP(%q) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}

func TestIsBlockedHostname(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"metadata.google.internal", true},
		{"169.254.169.254", true},
		{"kubernetes.default", true},
		{"kubernetes.default.svc.cluster.local", true},
		{"test.internal", true},
		{"test.local", true},

		{"example.com", false},
		{"raw.githubusercontent.com", false},
		{"api.github.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			result := isBlockedHostname(tt.host)
			if result != tt.expected {
				t.Errorf("isBlockedHostname(%q) = %v, want %v", tt.host, result, tt.expected)
			}
		})
	}
}

func TestSecureClientValidateURL(t *testing.T) {
	client := NewSecureClient(SecureClientConfig{
		AllowedHosts:    []string{"raw.githubusercontent.com", "example.com"},
		BlockPrivateIPs: true,
	})

	tests := []struct {
		url         string
		expectError bool
	}{
		// Valid URLs
		{"https://raw.githubusercontent.com/org/repo/main/file.sql", false},
		{"https://example.com/test", false},
		{"http://example.com/test", false},

		// Blocked - not in allowed hosts
		{"https://evil.com/malware", true},
		{"https://github.com/org/repo", true},

		// Blocked - private IPs
		{"http://127.0.0.1/secret", true},
		{"http://192.168.1.1/admin", true},
		{"http://10.0.0.1/internal", true},

		// Blocked - internal hostnames
		{"http://localhost/admin", true},
		{"http://metadata.google.internal/computeMetadata/v1/", true},
		{"http://169.254.169.254/latest/meta-data/", true},

		// Blocked - invalid scheme
		{"ftp://example.com/file", true},
		{"file:///etc/passwd", true},

		// Blocked - internal TLDs
		{"https://server.internal/api", true},
		{"https://host.local/data", true},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := client.ValidateURL(tt.url)
			if tt.expectError && err == nil {
				t.Errorf("ValidateURL(%q) should return error", tt.url)
			}
			if !tt.expectError && err != nil {
				t.Errorf("ValidateURL(%q) unexpected error: %v", tt.url, err)
			}
		})
	}
}

func TestSecureClientValidateURLNoAllowlist(t *testing.T) {
	// Client without allowlist - any non-private host is allowed
	client := NewSecureClient(SecureClientConfig{
		BlockPrivateIPs: true,
	})

	tests := []struct {
		url         string
		expectError bool
	}{
		{"https://example.com/test", false},
		{"https://any-public-site.com/data", false},
		{"http://localhost/admin", true},      // Still blocked
		{"http://192.168.1.1/internal", true}, // Still blocked
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := client.ValidateURL(tt.url)
			if tt.expectError && err == nil {
				t.Errorf("ValidateURL(%q) should return error", tt.url)
			}
			if !tt.expectError && err != nil {
				t.Errorf("ValidateURL(%q) unexpected error: %v", tt.url, err)
			}
		})
	}
}

func TestSecureClientGet(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("test content"))
		case "/large":
			w.WriteHeader(http.StatusOK)
			// Write more than 1KB
			for i := 0; i < 2000; i++ {
				_, _ = w.Write([]byte("x"))
			}
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Extract host from server URL for allowlist
	client := NewSecureClient(SecureClientConfig{
		Timeout:         5 * time.Second,
		MaxResponseSize: 1024, // 1KB limit for testing
		BlockPrivateIPs: false, // Allow localhost for testing
	})

	ctx := context.Background()

	t.Run("successful request", func(t *testing.T) {
		body, err := client.Get(ctx, server.URL+"/ok")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if string(body) != "test content" {
			t.Errorf("Get() body = %q, want %q", string(body), "test content")
		}
	})

	t.Run("response too large", func(t *testing.T) {
		_, err := client.Get(ctx, server.URL+"/large")
		if err == nil {
			t.Error("Get() should return error for large response")
		}
	})

	t.Run("server error", func(t *testing.T) {
		_, err := client.Get(ctx, server.URL+"/error")
		if err == nil {
			t.Error("Get() should return error for server error")
		}
	})

	t.Run("context timeout", func(t *testing.T) {
		slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
		}))
		defer slowServer.Close()

		shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()

		_, err := client.Get(shortCtx, slowServer.URL+"/slow")
		if err == nil {
			t.Error("Get() should return error for timeout")
		}
	})
}

func TestBytesCompare(t *testing.T) {
	tests := []struct {
		a, b     []byte
		expected int
	}{
		{[]byte{1, 2, 3}, []byte{1, 2, 3}, 0},
		{[]byte{1, 2, 3}, []byte{1, 2, 4}, -1},
		{[]byte{1, 2, 4}, []byte{1, 2, 3}, 1},
		{[]byte{1, 2}, []byte{1, 2, 3}, -1},
		{[]byte{1, 2, 3}, []byte{1, 2}, 1},
	}

	for _, tt := range tests {
		result := bytesCompare(tt.a, tt.b)
		if result != tt.expected {
			t.Errorf("bytesCompare(%v, %v) = %d, want %d", tt.a, tt.b, result, tt.expected)
		}
	}
}
