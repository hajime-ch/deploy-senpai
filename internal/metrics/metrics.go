package metrics

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics collects application metrics in Prometheus format
type Metrics struct {
	// Counters
	deploymentsTotal     int64
	deploymentsSucceeded int64
	deploymentsFailed    int64
	requestsTotal        int64
	webhooksReceived     int64

	// Gauges
	activeDeployments int64

	// Histograms (simplified as counters per bucket)
	requestDurations map[string]*int64

	mu sync.RWMutex
}

// New creates a new Metrics instance
func New() *Metrics {
	return &Metrics{
		requestDurations: make(map[string]*int64),
	}
}

// IncDeploymentsTotal increments total deployments counter
func (m *Metrics) IncDeploymentsTotal() {
	atomic.AddInt64(&m.deploymentsTotal, 1)
}

// IncDeploymentsSucceeded increments successful deployments counter
func (m *Metrics) IncDeploymentsSucceeded() {
	atomic.AddInt64(&m.deploymentsSucceeded, 1)
}

// IncDeploymentsFailed increments failed deployments counter
func (m *Metrics) IncDeploymentsFailed() {
	atomic.AddInt64(&m.deploymentsFailed, 1)
}

// IncRequestsTotal increments total requests counter
func (m *Metrics) IncRequestsTotal() {
	atomic.AddInt64(&m.requestsTotal, 1)
}

// IncWebhooksReceived increments webhooks counter
func (m *Metrics) IncWebhooksReceived() {
	atomic.AddInt64(&m.webhooksReceived, 1)
}

// SetActiveDeployments sets the active deployments gauge
func (m *Metrics) SetActiveDeployments(count int64) {
	atomic.StoreInt64(&m.activeDeployments, count)
}

// RecordRequestDuration records a request duration
func (m *Metrics) RecordRequestDuration(path string, duration time.Duration) {
	// Simplified histogram: just track counts per endpoint
	m.mu.Lock()
	defer m.mu.Unlock()

	key := path
	if _, ok := m.requestDurations[key]; !ok {
		var val int64
		m.requestDurations[key] = &val
	}
	atomic.AddInt64(m.requestDurations[key], 1)
}

// Handler returns an HTTP handler for the /metrics endpoint
func (m *Metrics) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		// Output Prometheus format metrics

		// Counters
		_, _ = fmt.Fprintf(w, "# HELP deployer_deployments_total Total number of deployment attempts\n")
		_, _ = fmt.Fprintf(w, "# TYPE deployer_deployments_total counter\n")
		_, _ = fmt.Fprintf(w, "deployer_deployments_total %d\n\n", atomic.LoadInt64(&m.deploymentsTotal))

		_, _ = fmt.Fprintf(w, "# HELP deployer_deployments_succeeded_total Total number of successful deployments\n")
		_, _ = fmt.Fprintf(w, "# TYPE deployer_deployments_succeeded_total counter\n")
		_, _ = fmt.Fprintf(w, "deployer_deployments_succeeded_total %d\n\n", atomic.LoadInt64(&m.deploymentsSucceeded))

		_, _ = fmt.Fprintf(w, "# HELP deployer_deployments_failed_total Total number of failed deployments\n")
		_, _ = fmt.Fprintf(w, "# TYPE deployer_deployments_failed_total counter\n")
		_, _ = fmt.Fprintf(w, "deployer_deployments_failed_total %d\n\n", atomic.LoadInt64(&m.deploymentsFailed))

		_, _ = fmt.Fprintf(w, "# HELP deployer_http_requests_total Total number of HTTP requests\n")
		_, _ = fmt.Fprintf(w, "# TYPE deployer_http_requests_total counter\n")
		_, _ = fmt.Fprintf(w, "deployer_http_requests_total %d\n\n", atomic.LoadInt64(&m.requestsTotal))

		_, _ = fmt.Fprintf(w, "# HELP deployer_webhooks_received_total Total number of webhooks received\n")
		_, _ = fmt.Fprintf(w, "# TYPE deployer_webhooks_received_total counter\n")
		_, _ = fmt.Fprintf(w, "deployer_webhooks_received_total %d\n\n", atomic.LoadInt64(&m.webhooksReceived))

		// Gauges
		_, _ = fmt.Fprintf(w, "# HELP deployer_active_deployments Current number of active deployments\n")
		_, _ = fmt.Fprintf(w, "# TYPE deployer_active_deployments gauge\n")
		_, _ = fmt.Fprintf(w, "deployer_active_deployments %d\n\n", atomic.LoadInt64(&m.activeDeployments))

		// Request counts per path
		_, _ = fmt.Fprintf(w, "# HELP deployer_http_requests_by_path Total requests by path\n")
		_, _ = fmt.Fprintf(w, "# TYPE deployer_http_requests_by_path counter\n")
		m.mu.RLock()
		for path, count := range m.requestDurations {
			_, _ = fmt.Fprintf(w, "deployer_http_requests_by_path{path=\"%s\"} %d\n", path, atomic.LoadInt64(count))
		}
		m.mu.RUnlock()
	}
}

// Middleware returns an HTTP middleware that tracks request metrics
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.IncRequestsTotal()
		start := time.Now()

		next.ServeHTTP(w, r)

		m.RecordRequestDuration(r.URL.Path, time.Since(start))
	})
}
