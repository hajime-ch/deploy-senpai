package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Authenticator handles API key authentication
type Authenticator struct {
	apiKeys map[string]bool
	enabled bool
}

// New creates a new Authenticator
func New(apiKeys []string, enabled bool) *Authenticator {
	keyMap := make(map[string]bool)
	for _, key := range apiKeys {
		if key != "" {
			keyMap[key] = true
		}
	}
	return &Authenticator{
		apiKeys: keyMap,
		enabled: enabled,
	}
}

// Middleware returns an HTTP middleware that validates API keys
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.enabled {
			next.ServeHTTP(w, r)
			return
		}

		// Extract API key from Authorization header or X-API-Key header
		apiKey := extractAPIKey(r)
		if apiKey == "" {
			http.Error(w, "missing API key", http.StatusUnauthorized)
			return
		}

		if !a.validateKey(apiKey) {
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// extractAPIKey extracts the API key from the request
func extractAPIKey(r *http.Request) string {
	// Check X-API-Key header first
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}

	// Check Authorization header (Bearer token)
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// Check query parameter as fallback (not recommended for production)
	if key := r.URL.Query().Get("api_key"); key != "" {
		return key
	}

	return ""
}

// validateKey checks if the provided key is valid using constant-time comparison
func (a *Authenticator) validateKey(key string) bool {
	for validKey := range a.apiKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(validKey)) == 1 {
			return true
		}
	}
	return false
}

// IsEnabled returns whether authentication is enabled
func (a *Authenticator) IsEnabled() bool {
	return a.enabled
}

// HasKeys returns whether any API keys are configured
func (a *Authenticator) HasKeys() bool {
	return len(a.apiKeys) > 0
}
