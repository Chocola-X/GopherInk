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
	"time"
)

type Config struct {
	Timeout      time.Duration
	UserAgent    string
	Proxy        string
	Retries      int
	AllowPrivate bool
	MaxBody      int64
}

type Client struct {
	httpClient *http.Client
	userAgent  string
	retries    int
	maxBody    int64
	allowPriv  bool
}

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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Proxy != "" {
		proxyURL, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	client := &Client{
		userAgent: cfg.UserAgent,
		retries:   cfg.Retries,
		maxBody:   cfg.MaxBody,
		allowPriv: cfg.AllowPrivate,
	}
	client.httpClient = &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return client.checkURL(req.Context(), req.URL.String())
		},
	}
	return client, nil
}

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
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", c.userAgent)
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
				last = errors.New("response body too large")
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

func (c *Client) PostXML(ctx context.Context, rawURL, body string) error {
	return c.post(ctx, rawURL, "text/xml; charset=utf-8", body)
}

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
	for i := 0; i < attempts; i++ {
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
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("unsupported URL scheme")
	}
	if c.allowPriv {
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("missing URL host")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if privateIP(ip.IP) {
			return errors.New("private address is not allowed")
		}
	}
	return nil
}

func privateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 169 && ip4[1] == 254
	}
	return strings.HasPrefix(ip.String(), "fe80:")
}

func ParseTimeoutSeconds(value string, fallback time.Duration) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}
