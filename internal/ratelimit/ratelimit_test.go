package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	t.Run("enabled limiter", func(t *testing.T) {
		rl := New(Config{
			Enabled:           true,
			RequestsPerMinute: 60,
		})
		defer rl.Stop()

		if !rl.IsEnabled() {
			t.Error("IsEnabled() should be true")
		}
	})

	t.Run("disabled limiter", func(t *testing.T) {
		rl := New(Config{
			Enabled:           false,
			RequestsPerMinute: 60,
		})

		if rl.IsEnabled() {
			t.Error("IsEnabled() should be false")
		}
	})

	t.Run("default requests per minute", func(t *testing.T) {
		rl := New(Config{
			Enabled:           true,
			RequestsPerMinute: 0, // Should default to 60
		})
		defer rl.Stop()

		if rl.requestsPerMinute != 60 {
			t.Errorf("requestsPerMinute = %d, want 60", rl.requestsPerMinute)
		}
	})
}

func TestRateLimiterAllow(t *testing.T) {
	rl := New(Config{
		Enabled:           true,
		RequestsPerMinute: 2, // Very low for testing
	})
	defer rl.Stop()

	key := "test-ip"

	// First request should be allowed
	if !rl.allow(key) {
		t.Error("First request should be allowed")
	}

	// Second request should be allowed
	if !rl.allow(key) {
		t.Error("Second request should be allowed")
	}

	// Third request should be blocked (exceeded limit)
	if rl.allow(key) {
		t.Error("Third request should be blocked")
	}

	// Different key should be allowed
	if !rl.allow("other-ip") {
		t.Error("Different key should be allowed")
	}
}

func TestRateLimiterTokenRefill(t *testing.T) {
	rl := New(Config{
		Enabled:           true,
		RequestsPerMinute: 60, // 1 per second
	})
	defer rl.Stop()

	key := "test-ip"

	// Exhaust tokens
	for i := 0; i < 60; i++ {
		rl.allow(key)
	}

	// Should be blocked
	if rl.allow(key) {
		t.Error("Should be blocked after exhausting tokens")
	}

	// Wait for token refill (1.1 seconds should give us ~1 token)
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again
	if !rl.allow(key) {
		t.Error("Should be allowed after token refill")
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	rl := New(Config{
		Enabled:           true,
		RequestsPerMinute: 2,
	})
	defer rl.Stop()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Middleware(handler)

	t.Run("allowed requests", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("X-Forwarded-For", "1.2.3.4")
			rec := httptest.NewRecorder()

			middleware.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("Request %d: status = %d, want %d", i+1, rec.Code, http.StatusOK)
			}

			// Check rate limit headers
			if rec.Header().Get("X-RateLimit-Limit") != "2" {
				t.Errorf("X-RateLimit-Limit = %q, want %q", rec.Header().Get("X-RateLimit-Limit"), "2")
			}
		}
	})

	t.Run("blocked request", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		rec := httptest.NewRecorder()

		middleware.ServeHTTP(rec, req)

		if rec.Code != http.StatusTooManyRequests {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusTooManyRequests)
		}

		// Check Retry-After header
		if rec.Header().Get("Retry-After") == "" {
			t.Error("Retry-After header should be set")
		}
	})

	t.Run("disabled middleware passes through", func(t *testing.T) {
		disabledRL := New(Config{
			Enabled:           false,
			RequestsPerMinute: 1,
		})

		disabledMiddleware := disabledRL.Middleware(handler)

		// Should pass through even with many requests
		for i := 0; i < 10; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			rec := httptest.NewRecorder()

			disabledMiddleware.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("Request %d: status = %d, want %d", i+1, rec.Code, http.StatusOK)
			}
		}
	})
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name           string
		xForwardedFor  string
		xRealIP        string
		remoteAddr     string
		expectedPrefix string
	}{
		{
			name:           "X-Forwarded-For",
			xForwardedFor:  "1.2.3.4",
			expectedPrefix: "1.2.3.4",
		},
		{
			name:           "X-Forwarded-For with multiple IPs",
			xForwardedFor:  "1.2.3.4, 5.6.7.8",
			expectedPrefix: "1.2.3.4",
		},
		{
			name:           "X-Real-IP",
			xRealIP:        "9.10.11.12",
			expectedPrefix: "9.10.11.12",
		},
		{
			name:           "RemoteAddr fallback",
			remoteAddr:     "13.14.15.16:1234",
			expectedPrefix: "13.14.15.16:1234",
		},
		{
			name:           "X-Forwarded-For takes precedence",
			xForwardedFor:  "1.2.3.4",
			xRealIP:        "5.6.7.8",
			remoteAddr:     "9.10.11.12:1234",
			expectedPrefix: "1.2.3.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}

			result := getClientIP(req)
			if result != tt.expectedPrefix {
				t.Errorf("getClientIP() = %q, want %q", result, tt.expectedPrefix)
			}
		})
	}
}

func TestRateLimiterRemaining(t *testing.T) {
	rl := New(Config{
		Enabled:           true,
		RequestsPerMinute: 10,
	})
	defer rl.Stop()

	key := "test-ip"

	// Initial remaining
	if rem := rl.remaining(key); rem != 10 {
		t.Errorf("Initial remaining = %d, want 10", rem)
	}

	// After one request
	rl.allow(key)
	if rem := rl.remaining(key); rem != 9 {
		t.Errorf("After 1 request, remaining = %d, want 9", rem)
	}
}

func TestRateLimiterRetryAfter(t *testing.T) {
	rl := New(Config{
		Enabled:           true,
		RequestsPerMinute: 60, // 1 per second
	})
	defer rl.Stop()

	key := "test-ip"

	// Exhaust tokens
	for i := 0; i < 60; i++ {
		rl.allow(key)
	}

	// Retry after should be about 1 second
	retryAfter := rl.retryAfter(key)
	if retryAfter < 900*time.Millisecond || retryAfter > 1100*time.Millisecond {
		t.Errorf("retryAfter = %v, want ~1s", retryAfter)
	}
}
