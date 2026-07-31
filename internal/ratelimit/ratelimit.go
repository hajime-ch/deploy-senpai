package ratelimit

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter
type RateLimiter struct {
	enabled           bool
	requestsPerMinute int
	buckets           map[string]*bucket
	mu                sync.RWMutex
	cleanupInterval   time.Duration
	stopCleanup       chan struct{}
}

type bucket struct {
	tokens     float64
	lastUpdate time.Time
}

// Config holds rate limiter configuration
type Config struct {
	Enabled           bool
	RequestsPerMinute int
}

// New creates a new rate limiter
func New(cfg Config) *RateLimiter {
	rl := &RateLimiter{
		enabled:           cfg.Enabled,
		requestsPerMinute: cfg.RequestsPerMinute,
		buckets:           make(map[string]*bucket),
		cleanupInterval:   5 * time.Minute,
		stopCleanup:       make(chan struct{}),
	}

	if cfg.RequestsPerMinute <= 0 {
		rl.requestsPerMinute = 60 // default
	}

	// Start cleanup goroutine
	if rl.enabled {
		go rl.cleanup()
	}

	return rl
}

// Middleware returns an HTTP middleware that rate limits requests
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.enabled {
			next.ServeHTTP(w, r)
			return
		}

		// Use IP address as key (could also use API key if authenticated)
		key := getClientIP(r)

		if !rl.allow(key) {
			// Calculate retry-after
			retryAfter := rl.retryAfter(key)

			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.requestsPerMinute))
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded","retry_after":"` + strconv.Itoa(int(retryAfter.Seconds())) + `s"}`))
			return
		}

		// Add rate limit headers
		remaining := rl.remaining(key)
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.requestsPerMinute))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

		next.ServeHTTP(w, r)
	})
}

// allow checks if a request should be allowed based on the token bucket
func (rl *RateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[key]

	if !exists {
		// Create new bucket with full tokens minus one for this request
		rl.buckets[key] = &bucket{
			tokens:     float64(rl.requestsPerMinute) - 1,
			lastUpdate: now,
		}
		return true
	}

	// Refill tokens based on time elapsed
	elapsed := now.Sub(b.lastUpdate)
	refillRate := float64(rl.requestsPerMinute) / 60.0 // tokens per second
	b.tokens += elapsed.Seconds() * refillRate

	// Cap at max tokens
	if b.tokens > float64(rl.requestsPerMinute) {
		b.tokens = float64(rl.requestsPerMinute)
	}

	b.lastUpdate = now

	// Check if we have tokens available
	if b.tokens < 1 {
		return false
	}

	// Consume a token
	b.tokens--
	return true
}

// remaining returns the number of remaining requests for a key
func (rl *RateLimiter) remaining(key string) int {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	b, exists := rl.buckets[key]
	if !exists {
		return rl.requestsPerMinute
	}

	// Calculate current tokens (with refill)
	elapsed := time.Since(b.lastUpdate)
	refillRate := float64(rl.requestsPerMinute) / 60.0
	tokens := b.tokens + elapsed.Seconds()*refillRate

	if tokens > float64(rl.requestsPerMinute) {
		tokens = float64(rl.requestsPerMinute)
	}

	return int(tokens)
}

// retryAfter calculates how long until the next request would be allowed
func (rl *RateLimiter) retryAfter(key string) time.Duration {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	b, exists := rl.buckets[key]
	if !exists {
		return 0
	}

	// Calculate time until 1 token is available
	if b.tokens >= 1 {
		return 0
	}

	tokensNeeded := 1 - b.tokens
	refillRate := float64(rl.requestsPerMinute) / 60.0 // tokens per second
	secondsUntilRefill := tokensNeeded / refillRate

	return time.Duration(secondsUntilRefill * float64(time.Second))
}

// cleanup removes stale buckets periodically
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for key, b := range rl.buckets {
				// Remove buckets that haven't been used in 10 minutes
				if now.Sub(b.lastUpdate) > 10*time.Minute {
					delete(rl.buckets, key)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCleanup:
			return
		}
	}
}

// Stop stops the rate limiter's cleanup goroutine
func (rl *RateLimiter) Stop() {
	if rl.enabled {
		close(rl.stopCleanup)
	}
}

// getClientIP extracts the client IP from the request.
//
// These headers are only trustworthy when a reverse proxy in front of the
// service overwrites them. A caller reaching the listener directly can set them
// to any value and land in a different rate-limit bucket per request, so do not
// rely on this for anything but rate limiting until trusted proxies are
// configurable.
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (set by reverse proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the list
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	return r.RemoteAddr
}

// IsEnabled returns whether rate limiting is enabled
func (rl *RateLimiter) IsEnabled() bool {
	return rl.enabled
}
