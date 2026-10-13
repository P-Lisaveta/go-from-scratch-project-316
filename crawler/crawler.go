// Package crawler analyzes website pages and returns a JSON report.
package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Options controls an analysis run.
type Options struct {
	URL         string
	Depth       int
	Retries     int
	Delay       time.Duration
	Timeout     time.Duration
	RPS         float64
	UserAgent   string
	Concurrency int
	IndentJSON  bool
	HTTPClient  *http.Client
}

// Report is the JSON representation of an analysis run.
type Report struct {
	RootURL     string `json:"root_url"`
	Depth       int    `json:"depth"`
	GeneratedAt string `json:"generated_at"`
	Pages       []Page `json:"pages"`
}

// Page holds the result of requesting one page.
type Page struct {
	URL        string `json:"url"`
	Depth      int    `json:"depth"`
	HTTPStatus int    `json:"http_status"`
	Status     string `json:"status"`
	Error      string `json:"error"`
}

// Analyze runs a crawl and serializes its current results as JSON.
func Analyze(ctx context.Context, opts Options) ([]byte, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}

	crawler := newCrawler(opts)
	report := crawler.crawl(ctx)

	return marshalReport(report, opts.IndentJSON)
}

type crawler struct {
	opts        Options
	visited     map[string]struct{}
	requestMu   sync.Mutex
	nextRequest time.Time
}

func newCrawler(opts Options) *crawler {
	return &crawler{
		opts:    opts,
		visited: make(map[string]struct{}, 1),
	}
}

func (c *crawler) crawl(ctx context.Context) Report {
	page := Page{
		URL:   c.opts.URL,
		Depth: 0,
	}

	if !c.visit(page.URL) {
		page.Status = "skipped"
		return c.newReport(page)
	}

	page = c.fetch(ctx, page)

	return c.newReport(page)
}

func (c *crawler) newReport(page Page) Report {
	return Report{
		RootURL:     c.opts.URL,
		Depth:       c.opts.Depth,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Pages:       []Page{page},
	}
}

func (c *crawler) visit(rawURL string) bool {
	if _, exists := c.visited[rawURL]; exists {
		return false
	}

	c.visited[rawURL] = struct{}{}

	return true
}

func (c *crawler) fetch(ctx context.Context, page Page) Page {
	attempts := c.opts.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		if err := c.waitForRequest(ctx); err != nil {
			page.Status = "error"
			page.Error = err.Error()
			return page
		}

		requestCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, page.URL, nil)
		if err != nil {
			cancel()
			page.Status = "error"
			page.Error = err.Error()
			return page
		}
		if c.opts.UserAgent != "" {
			request.Header.Set("User-Agent", c.opts.UserAgent)
		}

		response, err := c.opts.HTTPClient.Do(request)
		if err != nil {
			cancel()
			page.Status = "error"
			page.Error = err.Error()
			continue
		}

		page.HTTPStatus = response.StatusCode
		bodyErr := closeResponseBody(response.Body)
		cancel()
		if bodyErr != nil {
			page.Status = "error"
			page.Error = bodyErr.Error()
			continue
		}
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusBadRequest {
			page.Status = "ok"
			page.Error = ""
			return page
		}

		page.Status = "error"
		page.Error = fmt.Sprintf("unexpected http status: %d", response.StatusCode)
		return page
	}

	return page
}

func (c *crawler) waitForRequest(ctx context.Context) error {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()

	interval := c.opts.Delay
	if c.opts.RPS > 0 {
		interval = time.Duration(float64(time.Second) / c.opts.RPS)
	}
	if interval <= 0 {
		return nil
	}

	wait := time.Until(c.nextRequest)
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.nextRequest = time.Now().Add(interval)

	return nil
}

func validateOptions(opts Options) error {
	if opts.HTTPClient == nil {
		return errors.New("http client is required")
	}
	if opts.Timeout <= 0 {
		return errors.New("timeout must be greater than zero")
	}
	if opts.Depth < 0 {
		return errors.New("depth cannot be negative")
	}
	if opts.Retries < 0 {
		return errors.New("retries cannot be negative")
	}
	if opts.RPS < 0 {
		return errors.New("rps cannot be negative")
	}

	parsedURL, err := url.ParseRequestURI(opts.URL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return fmt.Errorf("invalid url: %q", opts.URL)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme: %q", parsedURL.Scheme)
	}

	return nil
}

func closeResponseBody(body io.ReadCloser) error {
	_, copyErr := io.Copy(io.Discard, body)
	closeErr := body.Close()

	return errors.Join(copyErr, closeErr)
}

func marshalReport(report Report, indent bool) ([]byte, error) {
	if indent {
		return json.MarshalIndent(report, "", "  ")
	}

	return json.Marshal(report)
}
