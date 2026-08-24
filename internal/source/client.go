package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// MaxBodyBytes is the default cap on every read from a host we do not control.
// An unbounded io.ReadAll on a hostile or merely broken response is the most
// reliable way to OOM this service.
//
// A bulk board API returns the whole board in one document, so the cap has to fit
// the largest legitimate board rather than a typical one. Measured against live
// endpoints: Greenhouse's gitlab board is 143 KB, Ashby's linear board 1.2 MB,
// Ashby's ramp board 2.3 MB — and Ashby's openai board is 12.4 MB, which the
// original 8 MiB cap rejected outright. A cap that silently excludes the largest
// employers is worse than a generous one, because the failure looks like a small
// market rather than a configuration error.
const MaxBodyBytes = 32 << 20 // 32 MiB

// MaxBodyBytesFor returns the cap for one client, falling back to the default.
// Per-client rather than global so a source family measured to need more does
// not raise the ceiling for every other host we talk to.
func (c *Client) MaxBodyBytesFor() int64 {
	if c.maxBody > 0 {
		return c.maxBody
	}
	return MaxBodyBytes
}

// ErrBodyTooLarge means the response exceeded the cap. The source should be
// quarantined rather than retried blindly — retrying will just OOM later.
var ErrBodyTooLarge = errors.New("source: response body exceeds cap")

// Client is the only way this package makes an HTTP request.
//
// http.DefaultClient is never used: it has no timeout at all, so one hung source
// holds a goroutine and a connection until both are exhausted.
type Client struct {
	http      *http.Client
	userAgent string

	maxBody int64

	mu      sync.Mutex
	limiter map[string]*rate.Limiter // per HOST: politeness is per host, not global
	rps     float64
	burst   int
}

type ClientConfig struct {
	UserAgent string
	// RequestsPerSecond applies per host.
	RequestsPerSecond float64
	Burst             int
	TotalTimeout      time.Duration
	// MaxBodyBytes overrides the default cap for this client. Zero uses the
	// default; set it only with a measurement to point at.
	MaxBodyBytes int64
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		UserAgent:         "DevSignal/0.1 (+https://github.com/Xubair001/devsignal)",
		RequestsPerSecond: 1,
		Burst:             2,
		TotalTimeout:      45 * time.Second,
	}
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.RequestsPerSecond <= 0 {
		cfg.RequestsPerSecond = 1
	}
	if cfg.Burst < 1 {
		cfg.Burst = 1
	}
	// Four separate timeouts, because they fail differently: a dial timeout is a
	// dead host, a TLS timeout a broken middlebox, a response-header timeout a
	// hung server, and the total timeout is the only defence against a slow drip.
	tr := &http.Transport{
		MaxConnsPerHost:       4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		http:      &http.Client{Transport: tr, Timeout: cfg.TotalTimeout},
		userAgent: cfg.UserAgent,
		limiter:   map[string]*rate.Limiter{},
		rps:       cfg.RequestsPerSecond,
		burst:     cfg.Burst,
		maxBody:   cfg.MaxBodyBytes,
	}
}

// Response is a fetched document plus what is needed for the next conditional GET.
type Response struct {
	Body         []byte
	ContentType  string
	ETag         string
	LastModified string
	StatusCode   int
}

// GetConditional performs a polite, size-capped GET.
//
// It sends If-None-Match / If-Modified-Since when the caller has them, and
// returns ErrNotModified on a 304. That is what makes a 5-minute poll interval
// affordable across hundreds of boards — most responses are 304s.
func (c *Client) GetConditional(ctx context.Context, rawURL string, cur Cursor) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("source: bad url: %w", err)
	}

	if err := c.wait(ctx, u.Host); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("source: request: %w", err)
	}
	// Identified user agent with a contact URL: required for Tier B, and simply
	// good manners for Tier A.
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if cur.ETag != "" {
		req.Header.Set("If-None-Match", cur.ETag)
	}
	if cur.LastModified != "" {
		req.Header.Set("If-Modified-Since", cur.LastModified)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("source: fetch: %w", err)
	}
	defer func() {
		// Drain a little before closing so the connection can be reused; do not
		// drain the whole body, which would defeat the cap.
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		_ = resp.Body.Close()
	}()

	out := &Response{
		StatusCode:   resp.StatusCode,
		ContentType:  resp.Header.Get("Content-Type"),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return out, ErrNotModified
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusServiceUnavailable:
		return out, fmt.Errorf("source: throttled: status %d", resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return out, fmt.Errorf("source: unexpected status %d", resp.StatusCode)
	}

	limit := c.MaxBodyBytesFor()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return out, fmt.Errorf("source: read body: %w", err)
	}
	if int64(len(body)) > limit {
		return out, ErrBodyTooLarge
	}
	out.Body = body
	return out, nil
}

func (c *Client) wait(ctx context.Context, host string) error {
	c.mu.Lock()
	lim, ok := c.limiter[host]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(c.rps), c.burst)
		c.limiter[host] = lim
	}
	c.mu.Unlock()

	if err := lim.Wait(ctx); err != nil {
		return fmt.Errorf("source: rate limit wait: %w", err)
	}
	return nil
}
