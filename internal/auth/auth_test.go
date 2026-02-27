package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticator(t *testing.T) {
	t.Run("new authenticator", func(t *testing.T) {
		auth := New([]string{"key1", "key2", ""}, true)

		if !auth.IsEnabled() {
			t.Error("IsEnabled() should be true")
		}
		if !auth.HasKeys() {
			t.Error("HasKeys() should be true")
		}
	})

	t.Run("empty keys filtered", func(t *testing.T) {
		auth := New([]string{"", "", ""}, true)

		if auth.HasKeys() {
			t.Error("HasKeys() should be false with only empty keys")
		}
	})

	t.Run("disabled auth", func(t *testing.T) {
		auth := New([]string{"key1"}, false)

		if auth.IsEnabled() {
			t.Error("IsEnabled() should be false")
		}
	})
}

func TestAuthenticatorMiddleware(t *testing.T) {
	validKey := "test-api-key-12345"
	auth := New([]string{validKey}, true)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	middleware := auth.Middleware(handler)

	t.Run("valid X-API-Key header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("X-API-Key", validKey)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("valid Bearer token", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+validKey)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("valid query parameter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test?api_key="+validKey, nil)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("missing API key", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("invalid API key", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("X-API-Key", "wrong-key")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("disabled auth passes through", func(t *testing.T) {
		disabledAuth := New([]string{validKey}, false)
		disabledMiddleware := disabledAuth.Middleware(handler)

		req := httptest.NewRequest("GET", "/api/test", nil)
		// No API key provided
		rec := httptest.NewRecorder()

		disabledMiddleware.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

func TestExtractAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		query    string
		expected string
	}{
		{
			name:     "X-API-Key header",
			headers:  map[string]string{"X-API-Key": "key123"},
			expected: "key123",
		},
		{
			name:     "Bearer token",
			headers:  map[string]string{"Authorization": "Bearer key456"},
			expected: "key456",
		},
		{
			name:     "query parameter",
			query:    "api_key=key789",
			expected: "key789",
		},
		{
			name:     "X-API-Key takes precedence",
			headers:  map[string]string{"X-API-Key": "key1", "Authorization": "Bearer key2"},
			expected: "key1",
		},
		{
			name:     "no key provided",
			headers:  map[string]string{},
			expected: "",
		},
		{
			name:     "invalid Authorization header",
			headers:  map[string]string{"Authorization": "Basic abc123"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/test"
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest("GET", url, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			result := extractAPIKey(req)
			if result != tt.expected {
				t.Errorf("extractAPIKey() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestValidateKeyConstantTime(t *testing.T) {
	auth := New([]string{"correct-key"}, true)

	// This test verifies the key validation works correctly
	// Constant-time comparison is hard to test directly, but we can verify behavior

	if !auth.validateKey("correct-key") {
		t.Error("validateKey() should accept correct key")
	}

	if auth.validateKey("wrong-key") {
		t.Error("validateKey() should reject wrong key")
	}

	if auth.validateKey("") {
		t.Error("validateKey() should reject empty key")
	}

	if auth.validateKey("correct-key-extra") {
		t.Error("validateKey() should reject key with extra characters")
	}
}
