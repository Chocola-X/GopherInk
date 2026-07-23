// Package httpclient is GopherInk's single outbound HTTP surface. It is used
// by the XML-RPC / pingback code, the admin remote-callback probe and the
// theme/plugin runtime helpers.
//
// The package is intentionally strict:
//
//   - Only http / https URLs are permitted; every other scheme (file, gopher,
//     ftp, data, ldap, dict, ...) is refused before a DNS request is made.
//   - DNS resolution is performed once, in-process, and every returned IP is
//     validated. When a hostname resolves to a loopback, link-local, private,
//     multicast, broadcast, cloud metadata, ULA or reserved range, the request
//     is refused with a stable error. This blocks classic SSRF, blind SSRF
//     via redirects and the CVE-2019-... style DNS rebinding trick because
//     the pinned dialer will only connect to the address we validated.
//   - Every hop of a redirect chain is re-validated with the same checks.
//   - Response bodies are limited (MaxBody) so an adversary cannot exhaust
//     memory by returning a multi-gigabyte HTML page for a pingback probe.
//   - Response headers are limited and Location headers are re-parsed rather
//     than trusted verbatim.
//
// AllowPrivate is exposed only for tests and self-hosted internal integrations
// that intentionally target private ranges. Production callers must leave it
// off.
package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Common errors returned for SSRF-style rejections. Callers may compare with
// errors.Is to decide how to surface the problem.
var (
	ErrUnsupportedScheme = errors.New("httpclient: unsupported URL scheme")
	ErrPrivateAddress    = errors.New("httpclient: private or reserved address is not allowed")
	ErrHostNotResolved   = errors.New("httpclient: unable to resolve host")
	ErrBodyTooLarge      = errors.New("httpclient: response body exceeds the configured limit")
	ErrTooManyRedirects  = errors.New("httpclient: too many redirects")
)

// Config is the tunable surface. Zero values are safe defaults.
type Config struct {
	Timeout      time.Duration
	UserAgent    string
	Proxy        string
	Retries      int
	MaxBody      int64
	MaxRedirects int
	// AllowPrivate disables the loopback / private / metadata address filter.
	// Do not set this in production.
	AllowPrivate bool
	// AllowSchemes overrides the default {http, https} allowlist.
	AllowSchemes []string
	// Resolver, when set, is used instead of net.DefaultResolver. Tests use
	// this to inject deterministic answers.
	Resolver *net.Resolver
	// Now lets tests inject a clock.
	Now func() time.Time
}

// Client is safe for concurrent use.
type Client struct {
	httpClient   *http.Client
	userAgent    string
	retries      int
	maxBody      int64
	maxRedirects int
	allowPriv    bool
	allowScheme  map[string]struct{}
	resolver     *net.Resolver
	mu           sync.Mutex
}

// New returns a hardened HTTP client.
func New(cfg Config) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "GopherInk/0.5.0"
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = 1 << 20
	}
	if cfg.MaxRedirects <= 0 {
		cfg.MaxRedirects = 5
	}
	allow := map[string]struct{}{}
	if len(cfg.AllowSchemes) == 0 {
		allow["http"] = struct{}{}
		allow["https"] = struct{}{}
	} else {
		for _, scheme := range cfg.AllowSchemes {
			allow[strings.ToLower(strings.TrimSpace(scheme))] = struct{}{}
		}
	}
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	client := &Client{
		userAgent:    cfg.UserAgent,
		retries:      cfg.Retries,
		maxBody:      cfg.MaxBody,
		maxRedirects: cfg.MaxRedirects,
		allowPriv:    cfg.AllowPrivate,
		allowScheme:  allow,
		resolver:     resolver,
	}

	dialer := &net.Dialer{Timeout: cfg.Timeout, KeepAlive: 15 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   cfg.Timeout,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip, err := client.resolveAndValidate(ctx, host)
			if err != nil {
				return nil, err
			}
			// Pin the dial target to the validated IP. This defeats DNS
			// rebinding: even if the resolver later hands the same host a
			// private answer, the pinned socket cannot swap out from under us.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		},
	}
	if cfg.Proxy != "" {
		proxyURL, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	client.httpClient = &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= client.maxRedirects {
				return ErrTooManyRedirects
			}
			return client.checkURL(req.Context(), req.URL.String())
		},
	}
	return client, nil
}

// GetText fetches a URL and returns its body as a string.
func (c *Client) GetText(ctx context.Context, rawURL string) (string, error) {
	if c == nil {
		return "", errors.New("nil http client")
	}
	if err := c.checkURL(ctx, rawURL); err != nil {
		return "", err
	}
	var last error
	var body string
	attempts := c.retries + 1
	for range attempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "text/html,text/plain,application/xhtml+xml;q=0.9,*/*;q=0.8")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			last = err
			continue
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				last = fmt.Errorf("unexpected status %d", resp.StatusCode)
				return
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
			if err != nil {
				last = err
				return
			}
			if int64(len(data)) > c.maxBody {
				last = ErrBodyTooLarge
				return
			}
			last = nil
			body = string(data)
		}()
		if last == nil {
			return body, nil
		}
	}
	return "", last
}

// PostXML sends an XML-RPC payload with proper Content-Type.
func (c *Client) PostXML(ctx context.Context, rawURL, body string) error {
	return c.post(ctx, rawURL, "text/xml; charset=utf-8", body)
}

// PostForm sends an application/x-www-form-urlencoded payload.
func (c *Client) PostForm(ctx context.Context, rawURL, body string) error {
	return c.post(ctx, rawURL, "application/x-www-form-urlencoded", body)
}

func (c *Client) post(ctx context.Context, rawURL, contentType, body string) error {
	if c == nil {
		return errors.New("nil http client")
	}
	if err := c.checkURL(ctx, rawURL); err != nil {
		return err
	}
	var last error
	attempts := c.retries + 1
	for range attempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewBufferString(body))
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Content-Type", contentType)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			last = err
			continue
		}
		func() {
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxBody))
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				last = fmt.Errorf("unexpected status %d", resp.StatusCode)
				return
			}
			last = nil
		}()
		if last == nil {
			return nil
		}
	}
	return last
}

func (c *Client) checkURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	scheme := strings.ToLower(u.Scheme)
	if _, ok := c.allowScheme[scheme]; !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedScheme, u.Scheme)
	}
	if c.allowPriv {
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: %q", ErrHostNotResolved, rawURL)
	}
	// Reject bracketed IP literals that decode to a private range.
	if ip := net.ParseIP(host); ip != nil {
		if privateOrReservedIP(ip) {
			return fmt.Errorf("%w: %s", ErrPrivateAddress, ip.String())
		}
		return nil
	}
	if _, err := c.resolveAndValidate(ctx, host); err != nil {
		return err
	}
	return nil
}

func (c *Client) resolveAndValidate(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !c.allowPriv && privateOrReservedIP(ip) {
			return nil, fmt.Errorf("%w: %s", ErrPrivateAddress, ip.String())
		}
		return ip, nil
	}
	c.mu.Lock()
	resolver := c.resolver
	c.mu.Unlock()
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrHostNotResolved, err.Error())
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no records for %q", ErrHostNotResolved, host)
	}
	// Every answer must pass. If any is private we reject entirely because a
	// rebinding attack could steer us to the tainted one.
	for _, ip := range ips {
		if !c.allowPriv && privateOrReservedIP(ip.IP) {
			return nil, fmt.Errorf("%w: %s -> %s", ErrPrivateAddress, host, ip.IP.String())
		}
	}
	return ips[0].IP, nil
}

// privateOrReservedIP returns true for every address family the caller must
// not reach in production. This is stricter than net.IP.IsPrivate: it also
// covers link-local, loopback, unspecified, multicast, broadcast, metadata
// endpoints and IPv6 reserved ranges.
func privateOrReservedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 169.254.0.0/16 covers link-local, including cloud metadata
		// (169.254.169.254 on AWS/GCP/Azure).
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		// 127.0.0.0/8
		if ip4[0] == 127 {
			return true
		}
		// 0.0.0.0/8
		if ip4[0] == 0 {
			return true
		}
		// 100.64.0.0/10 CGNAT
		if ip4[0] == 100 && ip4[1]&0xC0 == 64 {
			return true
		}
		// 255.255.255.255 broadcast
		if ip4[0] == 255 && ip4[1] == 255 && ip4[2] == 255 && ip4[3] == 255 {
			return true
		}
		return false
	}
	s := ip.String()
	// fe80::/10 link-local, fc00::/7 ULA, ::1 loopback, ::/128 unspecified.
	if strings.HasPrefix(s, "fe80:") || strings.HasPrefix(s, "fc") || strings.HasPrefix(s, "fd") {
		return true
	}
	if s == "::1" || s == "::" {
		return true
	}
	// IPv4-mapped IPv6.
	if ip4 := ip.To4(); ip4 != nil {
		return privateOrReservedIP(ip4)
	}
	return false
}

// ParseTimeoutSeconds parses a number-of-seconds string, returning fallback on
// an unparseable or non-positive input.
func ParseTimeoutSeconds(value string, fallback time.Duration) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}
