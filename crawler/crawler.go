// Package crawler analyzes website pages and returns a JSON report.
package crawler

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const retryDelay = 20 * time.Millisecond

// Options controls an analysis run.
type Options struct {
	URL         string
	Depth       int
	Retries     int           // Maximum number of retries after the first request.
	Delay       time.Duration // Global delay between HTTP requests.
	Timeout     time.Duration
	RPS         float64 // Global requests per second; takes precedence over Delay.
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
	Error        string       `json:"error,omitempty"`
	SEO          SEO          `json:"seo"`
	BrokenLinks  []BrokenLink `json:"broken_links"`
	Assets       []Asset      `json:"assets"`
	DiscoveredAt string       `json:"discovered_at"`
}

// BrokenLink describes an unavailable link discovered on a page.
type BrokenLink struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Asset describes a static resource found on a page.
type Asset struct {
	URL        string `json:"url"`
	Type       string `json:"type"`
	StatusCode int    `json:"status_code"`
	SizeBytes  int64  `json:"size_bytes"`
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
	assetCache  map[string]Asset
	requestMu   sync.Mutex
	assetMu     sync.Mutex
	nextRequest time.Time
}

type httpResult struct {
	statusCode         int
	body               []byte
	contentLength      int64
	contentLengthKnown bool
}

type crawlTask struct {
	URL   string
	Depth int
}

func newCrawler(opts Options) *crawler {
	return &crawler{
		opts:       opts,
		visited:    make(map[string]struct{}, 1),
		assetCache: make(map[string]Asset),
	}
}

func (c *crawler) crawl(ctx context.Context) Report {
	rootURL, _ := url.Parse(c.opts.URL)
	c.visit(c.opts.URL)

	queue := []crawlTask{{URL: c.opts.URL, Depth: 0}}
	pages := make([]Page, 0, 1)
	for len(queue) > 0 {
		if ctx.Err() != nil {
			break
		}

		task := queue[0]
		queue = queue[1:]
		page, links := c.fetch(ctx, c.newPage(task))
		pages = append(pages, page)

		if task.Depth+1 >= c.opts.Depth {
			continue
		}
		for _, link := range links {
			if !sameDomain(rootURL, link) || !c.visit(link) {
				continue
			}
			queue = append(queue, crawlTask{URL: link, Depth: task.Depth + 1})
		}
	}

	return c.newReport(pages)
}

func (c *crawler) newPage(task crawlTask) Page {
	return Page{
		URL:          task.URL,
		Depth:        task.Depth,
		DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func (c *crawler) newReport(pages []Page) Report {
	return Report{
		RootURL:     c.opts.URL,
		Depth:       c.opts.Depth,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Pages:       pages,
	}
}

func (c *crawler) visit(rawURL string) bool {
	key := canonicalURL(rawURL)
	if _, exists := c.visited[key]; exists {
		return false
	}

	c.visited[key] = struct{}{}

	return true
}

func (c *crawler) fetch(ctx context.Context, page Page) (Page, []string) {
	result, err := c.do(ctx, page.URL, true)
	page.HTTPStatus = result.statusCode
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page, nil
	}

	if result.statusCode < http.StatusOK || result.statusCode >= http.StatusBadRequest {
		page.Status = "error"
		page.Error = fmt.Sprintf("unexpected http status: %d", result.statusCode)
		return page, nil
	}

	links, err := extractLinks(page.URL, result.body)
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page, nil
	}
	brokenLinks, err := c.findBrokenLinks(ctx, links)
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page, nil
	}
	seo, err := extractSEO(result.body)
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page, nil
	}
	assets, err := c.findAssets(ctx, page.URL, result.body)
	if err != nil {
		page.Status = "error"
		page.Error = err.Error()
		return page, nil
	}
	page.Status = "ok"
	page.BrokenLinks = brokenLinks
	page.Assets = assets
	page.SEO = seo

	return page, links
}

func (c *crawler) findBrokenLinks(ctx context.Context, links []string) ([]BrokenLink, error) {
	brokenLinks := make([]BrokenLink, 0)
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := c.do(ctx, link, false)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			brokenLinks = append(brokenLinks, BrokenLink{URL: link, StatusCode: result.statusCode, Error: err.Error()})
			continue
		}
		if result.statusCode >= http.StatusBadRequest {
			brokenLinks = append(brokenLinks, BrokenLink{
				URL:        link,
				StatusCode: result.statusCode,
			})
		}
	}

	return brokenLinks, nil
}

func (c *crawler) findAssets(ctx context.Context, pageURL string, document []byte) ([]Asset, error) {
	discoveredAssets, err := extractAssets(pageURL, document)
	if err != nil {
		return nil, err
	}

	assets := make([]Asset, 0, len(discoveredAssets))
	for _, asset := range discoveredAssets {
		assets = append(assets, c.loadAsset(ctx, asset))
	}

	return assets, nil
}

func (c *crawler) loadAsset(ctx context.Context, asset Asset) Asset {
	c.assetMu.Lock()
	defer c.assetMu.Unlock()

	if cachedAsset, exists := c.assetCache[asset.URL]; exists {
		return cachedAsset
	}

	result, err := c.do(ctx, asset.URL, true)
	asset.StatusCode = result.statusCode
	if err != nil {
		asset.SizeBytes = 0
		asset.Error = err.Error()
	} else {
		asset.SizeBytes = assetSize(result)
	}
	if err == nil && result.statusCode >= http.StatusBadRequest {
		asset.Error = fmt.Sprintf("unexpected http status: %d", result.statusCode)
	}
	c.assetCache[asset.URL] = asset

	return asset
}

func assetSize(result httpResult) int64 {
	if result.contentLengthKnown {
		return result.contentLength
	}

	return int64(len(result.body))
}

func sameDomain(rootURL *url.URL, rawURL string) bool {
	link, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	return strings.EqualFold(rootURL.Host, link.Host)
}

func (c *crawler) do(ctx context.Context, rawURL string, readBody bool) (httpResult, error) {
	attempts := c.opts.Retries + 1
	lastResult := httpResult{}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := c.waitForRequest(ctx); err != nil {
			return lastResult, err
		}

		requestCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return lastResult, err
		}
		if c.opts.UserAgent != "" {
			request.Header.Set("User-Agent", c.opts.UserAgent)
		}

		response, err := c.opts.HTTPClient.Do(request)
		if err != nil {
			cancel()
			lastErr = err
			if err := c.waitForRetry(ctx, attempt, attempts); err != nil {
				return lastResult, err
			}
			continue
		}

		result := httpResult{
			statusCode:         response.StatusCode,
			contentLength:      response.ContentLength,
			contentLengthKnown: response.ContentLength > 0 || response.Header.Get("Content-Length") != "",
		}
		if readBody {
			result.body, err = readResponseBody(response.Body)
		} else {
			err = closeResponseBody(response.Body)
		}
		cancel()
		if err != nil {
			lastResult = result
			lastErr = err
			if err := c.waitForRetry(ctx, attempt, attempts); err != nil {
				return lastResult, err
			}
			continue
		}
		if !isTemporaryStatus(response.StatusCode) {
			return result, nil
		}
		if err := c.waitForRetry(ctx, attempt, attempts); err != nil {
			return result, err
		}
		if attempt+1 < attempts {
			continue
		}

		return result, nil
	}

	return lastResult, lastErr
}

func (c *crawler) waitForRetry(ctx context.Context, attempt, attempts int) error {
	if attempt+1 >= attempts {
		return nil
	}

	timer := time.NewTimer(retryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTemporaryStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func (c *crawler) waitForRequest(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	interval := c.opts.Delay
	if c.opts.RPS > 0 {
		interval = time.Duration(float64(time.Second) / c.opts.RPS)
	}
	if interval <= 0 {
		return nil
	}

	c.requestMu.Lock()
	requestAt := time.Now()
	if c.nextRequest.After(requestAt) {
		requestAt = c.nextRequest
	}
	c.nextRequest = requestAt.Add(interval)
	c.requestMu.Unlock()

	wait := time.Until(requestAt)
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}

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
	slices.Sort(links)

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
			key := canonicalURL(absoluteURL)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
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

func canonicalURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Path != "/" {
		return rawURL
	}
	parsedURL.Path = ""
	parsedURL.RawPath = ""

	return parsedURL.String()
}

func extractAssets(pageURL string, document []byte) ([]Asset, error) {
	baseURL, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("parse page url: %w", err)
	}

	node, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	assets := make([]Asset, 0)
	seen := make(map[string]struct{})
	collectAssets(node, baseURL, seen, &assets)
	slices.SortFunc(assets, func(left, right Asset) int {
		return cmp.Or(
			strings.Compare(left.Type, right.Type),
			strings.Compare(left.URL, right.URL),
		)
	})

	return assets, nil
}

func collectAssets(node *html.Node, baseURL *url.URL, seen map[string]struct{}, assets *[]Asset) {
	assetType, attribute := assetDetails(node)
	if assetType != "" {
		rawURL, found := nodeAttribute(node, attribute)
		if found {
			absoluteURL, ok := absoluteHTTPURL(baseURL, rawURL)
			if ok {
				if _, exists := seen[absoluteURL]; !exists {
					seen[absoluteURL] = struct{}{}
					*assets = append(*assets, Asset{URL: absoluteURL, Type: assetType})
				}
			}
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectAssets(child, baseURL, seen, assets)
	}
}

func assetDetails(node *html.Node) (string, string) {
	if node.Type != html.ElementNode {
		return "", ""
	}

	switch node.Data {
	case "img":
		return "image", "src"
	case "script":
		return "script", "src"
	case "link":
		if isStylesheet(node) {
			return "style", "href"
		}
	}

	return "", ""
}

func isStylesheet(node *html.Node) bool {
	rel, found := nodeAttribute(node, "rel")
	if !found {
		return false
	}
	for _, value := range strings.Fields(rel) {
		if strings.EqualFold(value, "stylesheet") {
			return true
		}
	}

	return false
}

func nodeAttribute(node *html.Node, name string) (string, bool) {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val, true
		}
	}

	return "", false
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
