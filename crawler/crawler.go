// Package crawler analyzes website pages and returns a JSON report.
package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
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
	URL          string       `json:"url"`
	Depth        int          `json:"depth"`
	HTTPStatus   int          `json:"http_status"`
	Status       string       `json:"status"`
	Error        string       `json:"error"`
	BrokenLinks  []BrokenLink `json:"broken_links"`
	DiscoveredAt string       `json:"discovered_at"`
	SEO          SEO          `json:"seo"`
}

// BrokenLink describes an unavailable link discovered on a page.
type BrokenLink struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// SEO contains basic search-engine optimization data found on a page.
type SEO struct {
	HasTitle       bool   `json:"has_title"`
	Title          string `json:"title"`
	HasDescription bool   `json:"has_description"`
	Description    string `json:"description"`
	HasH1          bool   `json:"has_h1"`
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
		URL:          c.opts.URL,
		Depth:        0,
		BrokenLinks:  make([]BrokenLink, 0),
		DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
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
	status, document, err := c.do(ctx, page.URL, true)
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page
	}

	page.HTTPStatus = status
	if status < http.StatusOK || status >= http.StatusBadRequest {
		page.Status = "error"
		page.Error = fmt.Sprintf("unexpected http status: %d", status)
		return page
	}

	brokenLinks, err := c.findBrokenLinks(ctx, page.URL, document)
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page
	}
	seo, err := extractSEO(document)
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page
	}
	page.Status = "ok"
	page.BrokenLinks = brokenLinks
	page.SEO = seo

	return page
}

func (c *crawler) findBrokenLinks(ctx context.Context, pageURL string, document []byte) ([]BrokenLink, error) {
	links, err := extractLinks(pageURL, document)
	if err != nil {
		return nil, err
	}

	brokenLinks := make([]BrokenLink, 0)
	for _, link := range links {
		status, _, err := c.do(ctx, link, false)
		if err != nil {
			brokenLinks = append(brokenLinks, BrokenLink{URL: link, Error: err.Error()})
			continue
		}
		if status >= http.StatusBadRequest {
			brokenLinks = append(brokenLinks, BrokenLink{URL: link, StatusCode: status})
		}
	}

	return brokenLinks, nil
}

func (c *crawler) do(ctx context.Context, rawURL string, readBody bool) (int, []byte, error) {
	attempts := c.opts.Retries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := c.waitForRequest(ctx); err != nil {
			return 0, nil, err
		}

		requestCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return 0, nil, err
		}
		if c.opts.UserAgent != "" {
			request.Header.Set("User-Agent", c.opts.UserAgent)
		}

		response, err := c.opts.HTTPClient.Do(request)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		var body []byte
		if readBody {
			body, err = readResponseBody(response.Body)
		} else {
			err = closeResponseBody(response.Body)
		}
		cancel()
		if err != nil {
			lastErr = err
			continue
		}

		return response.StatusCode, body, nil
	}

	return 0, nil, lastErr
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

func readResponseBody(body io.ReadCloser) ([]byte, error) {
	content, readErr := io.ReadAll(body)
	closeErr := body.Close()

	return content, errors.Join(readErr, closeErr)
}

func extractLinks(pageURL string, document []byte) ([]string, error) {
	baseURL, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("parse page url: %w", err)
	}

	node, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	links := make([]string, 0)
	seen := make(map[string]struct{})
	collectLinks(node, baseURL, seen, &links)

	return links, nil
}

func collectLinks(node *html.Node, baseURL *url.URL, seen map[string]struct{}, links *[]string) {
	if node.Type == html.ElementNode && (node.Data == "a" || node.Data == "area") {
		for _, attr := range node.Attr {
			if attr.Key != "href" {
				continue
			}
			absoluteURL, ok := absoluteHTTPURL(baseURL, attr.Val)
			if !ok {
				continue
			}
			if _, exists := seen[absoluteURL]; exists {
				continue
			}
			seen[absoluteURL] = struct{}{}
			*links = append(*links, absoluteURL)
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectLinks(child, baseURL, seen, links)
	}
}

func absoluteHTTPURL(baseURL *url.URL, rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || strings.HasPrefix(rawURL, "#") {
		return "", false
	}

	link, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	link = baseURL.ResolveReference(link)
	if (link.Scheme != "http" && link.Scheme != "https") || link.Host == "" {
		return "", false
	}
	link.Fragment = ""

	return link.String(), true
}

func extractSEO(document []byte) (SEO, error) {
	node, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return SEO{}, fmt.Errorf("parse html: %w", err)
	}

	seo := SEO{}
	collectSEO(node, &seo)

	return seo, nil
}

func collectSEO(node *html.Node, seo *SEO) {
	if node.Type == html.ElementNode {
		switch node.Data {
		case "title":
			if !seo.HasTitle {
				seo.HasTitle = true
				seo.Title = nodeText(node)
			}
		case "h1":
			if !seo.HasH1 {
				seo.HasH1 = true
			}
		case "meta":
			if !seo.HasDescription && isDescriptionMeta(node) {
				seo.HasDescription = true
				seo.Description = normalizedAttribute(node, "content")
			}
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectSEO(child, seo)
	}
}

func isDescriptionMeta(node *html.Node) bool {
	return strings.EqualFold(normalizedAttribute(node, "name"), "description")
}

func normalizedAttribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return strings.Join(strings.Fields(attr.Val), " ")
		}
	}

	return ""
}

func nodeText(node *html.Node) string {
	var text strings.Builder
	collectText(node, &text)

	return strings.Join(strings.Fields(text.String()), " ")
}

func collectText(node *html.Node, text *strings.Builder) {
	if node.Type == html.TextNode {
		text.WriteString(node.Data)
		text.WriteByte(' ')
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectText(child, text)
	}
}

func marshalReport(report Report, indent bool) ([]byte, error) {
	if indent {
		return json.MarshalIndent(report, "", "  ")
	}

	return json.Marshal(report)
}
