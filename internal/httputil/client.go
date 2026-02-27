package httputil

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultTimeout is the default request timeout
	DefaultTimeout = 30 * time.Second
	// DefaultMaxResponseSize is the default maximum response size (10MB)
	DefaultMaxResponseSize = 10 * 1024 * 1024
)

// SecureClientConfig holds configuration for the secure HTTP client
type SecureClientConfig struct {
	// Timeout for the entire request
	Timeout time.Duration
	// MaxResponseSize limits the response body size
	MaxResponseSize int64
	// AllowedHosts is a list of allowed hostnames (if empty, all non-private are allowed)
	AllowedHosts []string
	// BlockPrivateIPs prevents requests to private IP ranges
	BlockPrivateIPs bool
}

// SecureClient is an HTTP client with SSRF protections
type SecureClient struct {
	client          *http.Client
	allowedHosts    map[string]bool
	maxResponseSize int64
	blockPrivateIPs bool
}

// NewSecureClient creates a new secure HTTP client
func NewSecureClient(cfg SecureClientConfig) *SecureClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	maxSize := cfg.MaxResponseSize
	if maxSize == 0 {
		maxSize = DefaultMaxResponseSize
	}

	allowedHosts := make(map[string]bool)
	for _, host := range cfg.AllowedHosts {
		allowedHosts[strings.ToLower(host)] = true
	}

	sc := &SecureClient{
		allowedHosts:    allowedHosts,
		maxResponseSize: maxSize,
		blockPrivateIPs: cfg.BlockPrivateIPs,
	}

	// Create transport with custom dialer for IP validation
	transport := &http.Transport{
		DialContext: sc.dialContext,
		// Security-focused timeouts
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Disable keep-alives for security
		DisableKeepAlives: true,
	}

	sc.client = &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// Don't follow redirects automatically - validate each redirect
		CheckRedirect: sc.checkRedirect,
	}

	return sc
}

// dialContext is a custom dialer that validates IPs before connecting
func (sc *SecureClient) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	// Resolve the hostname to IPs
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed: %w", err)
	}

	// Check each resolved IP
	for _, ip := range ips {
		if sc.blockPrivateIPs && isPrivateIP(ip.IP) {
			return nil, fmt.Errorf("connection to private IP %s is not allowed", ip.IP)
		}
	}

	// Use the first valid IP
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPs resolved for host %s", host)
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 0, // Disable keep-alive
	}

	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// checkRedirect validates redirects to prevent SSRF via redirect
func (sc *SecureClient) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}

	// Validate the redirect URL
	if err := sc.ValidateURL(req.URL.String()); err != nil {
		return fmt.Errorf("redirect blocked: %w", err)
	}

	return nil
}

// ValidateURL checks if a URL is safe to fetch
func (sc *SecureClient) ValidateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Only allow HTTP and HTTPS
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme %q not allowed, only http/https permitted", parsed.Scheme)
	}

	host := strings.ToLower(parsed.Hostname())

	// Check allowed hosts if configured
	if len(sc.allowedHosts) > 0 {
		if !sc.allowedHosts[host] {
			return fmt.Errorf("host %q not in allowed list", host)
		}
	}

	// Block localhost and common internal hostnames when private IP blocking is enabled
	if sc.blockPrivateIPs && isBlockedHostname(host) {
		return fmt.Errorf("host %q is not allowed", host)
	}

	// If the host is an IP address, check if it's private
	if ip := net.ParseIP(host); ip != nil {
		if sc.blockPrivateIPs && isPrivateIP(ip) {
			return fmt.Errorf("private IP %q is not allowed", host)
		}
	}

	return nil
}

// Get performs a validated GET request
func (sc *SecureClient) Get(ctx context.Context, url string) ([]byte, error) {
	if err := sc.ValidateURL(url); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := sc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Limit response size
	limitedReader := io.LimitReader(resp.Body, sc.maxResponseSize+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if int64(len(body)) > sc.maxResponseSize {
		return nil, fmt.Errorf("response exceeds maximum size of %d bytes", sc.maxResponseSize)
	}

	return body, nil
}

// isPrivateIP checks if an IP is in a private range
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	// Check for IPv4 private ranges
	privateRanges := []struct {
		start net.IP
		end   net.IP
	}{
		// 10.0.0.0/8
		{net.ParseIP("10.0.0.0"), net.ParseIP("10.255.255.255")},
		// 172.16.0.0/12
		{net.ParseIP("172.16.0.0"), net.ParseIP("172.31.255.255")},
		// 192.168.0.0/16
		{net.ParseIP("192.168.0.0"), net.ParseIP("192.168.255.255")},
		// 127.0.0.0/8 (loopback)
		{net.ParseIP("127.0.0.0"), net.ParseIP("127.255.255.255")},
		// 169.254.0.0/16 (link-local)
		{net.ParseIP("169.254.0.0"), net.ParseIP("169.254.255.255")},
		// 0.0.0.0/8
		{net.ParseIP("0.0.0.0"), net.ParseIP("0.255.255.255")},
	}

	ip4 := ip.To4()
	if ip4 != nil {
		for _, r := range privateRanges {
			if bytesCompare(ip4, r.start.To4()) >= 0 && bytesCompare(ip4, r.end.To4()) <= 0 {
				return true
			}
		}
	}

	// Check for IPv6 private ranges
	if ip.To4() == nil {
		// ::1 (loopback)
		if ip.Equal(net.ParseIP("::1")) {
			return true
		}
		// fc00::/7 (unique local)
		if ip[0] == 0xfc || ip[0] == 0xfd {
			return true
		}
		// fe80::/10 (link-local)
		if ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
			return true
		}
	}

	return false
}

// bytesCompare compares two byte slices
func bytesCompare(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// isBlockedHostname checks for common internal hostnames
func isBlockedHostname(host string) bool {
	blocked := []string{
		"localhost",
		"127.0.0.1",
		"::1",
		"0.0.0.0",
		"metadata.google.internal",        // GCP metadata
		"169.254.169.254",                 // AWS/GCP/Azure metadata
		"metadata.google.com",             // GCP metadata
		"kubernetes.default",              // K8s
		"kubernetes.default.svc",          // K8s
		"kubernetes.default.svc.cluster",  // K8s
		"kubernetes.default.svc.cluster.local",
	}

	for _, b := range blocked {
		if host == b {
			return true
		}
	}

	// Block .internal and .local TLDs
	if strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return true
	}

	return false
}
