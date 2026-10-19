package crawler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code/crawler"

	"github.com/stretchr/testify/require"
)

func TestAnalyze(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "test-crawler", request.UserAgent())
		require.Equal(t, "https://example.test", request.URL.String())

		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Depth:      1,
		Timeout:    time.Second,
		UserAgent:  "test-crawler",
		IndentJSON: true,
		HTTPClient: client,
	})

	require.NoError(t, err)

	actual := decodeReport(t, report)
	require.Equal(t, "https://example.test", actual.RootURL)
	require.Equal(t, 1, actual.Depth)
	require.NotEmpty(t, actual.GeneratedAt)
	require.Len(t, actual.Pages, 1)
	require.Equal(t, "https://example.test", actual.Pages[0].URL)
	require.Equal(t, 0, actual.Pages[0].Depth)
	require.Equal(t, http.StatusOK, actual.Pages[0].HTTPStatus)
	require.Equal(t, "ok", actual.Pages[0].Status)
	require.Empty(t, actual.Pages[0].Error)
	require.Empty(t, actual.Pages[0].BrokenLinks)
	require.NotEmpty(t, actual.Pages[0].DiscoveredAt)
}

func TestAnalyzeCapturesNetworkErrorInReport(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Len(t, actual.Pages, 1)
	require.Equal(t, "error", actual.Pages[0].Status)
	require.Contains(t, actual.Pages[0].Error, "network unavailable")
}

func TestAnalyzeCapturesErrorHTTPStatusInReport(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		status int
	}{
		{name: "not found", status: http.StatusNotFound},
		{name: "server error", status: http.StatusInternalServerError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: testCase.status,
					Body:       io.NopCloser(strings.NewReader("error")),
					Header:     make(http.Header),
				}, nil
			})}

			report, err := crawler.Analyze(t.Context(), crawler.Options{
				URL:        "https://example.test",
				Timeout:    time.Second,
				HTTPClient: client,
			})

			require.NoError(t, err)
			actual := decodeReport(t, report)
			require.Len(t, actual.Pages, 1)
			require.Equal(t, testCase.status, actual.Pages[0].HTTPStatus)
			require.Equal(t, "error", actual.Pages[0].Status)
			require.Equal(t, "unexpected http status: "+strconv.Itoa(testCase.status), actual.Pages[0].Error)
		})
	}
}

func TestAnalyzeCapturesTimeoutInReport(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Timeout:    20 * time.Millisecond,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, "error", actual.Pages[0].Status)
	require.Contains(t, actual.Pages[0].Error, context.DeadlineExceeded.Error())
}

func TestAnalyzeReportsOnlyBrokenLinks(t *testing.T) {
	t.Parallel()

	requests := make(map[string]int)
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		requests[request.URL.String()]++

		switch request.URL.String() {
		case "https://simple.test/blog/index.html":
			return htmlResponse(http.StatusOK, `
				<a href="/assets/working.css">working</a>
				<a href="/assets/ghost.css">missing</a>
				<a href="https://cdn.simple.test/app.js">unavailable</a>
				<a href="">empty</a>
				<a href="mailto:help@simple.test">email</a>
				<a href="javascript:void(0)">script</a>
				<a href="#section">fragment</a>
			`)
		case "https://simple.test/assets/working.css":
			return htmlResponse(http.StatusOK, "ok")
		case "https://simple.test/assets/ghost.css":
			return htmlResponse(http.StatusNotFound, "not found")
		case "https://cdn.simple.test/app.js":
			return nil, errors.New("host unavailable")
		default:
			return nil, errors.New("unexpected request")
		}
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://simple.test/blog/index.html",
		Depth:      1,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Len(t, actual.Pages, 1)
	require.Equal(t, "ok", actual.Pages[0].Status)
	require.Equal(t, []crawler.BrokenLink{
		{URL: "https://cdn.simple.test/app.js", Error: `Head "https://cdn.simple.test/app.js": host unavailable`},
		{
			URL:        "https://simple.test/assets/ghost.css",
			StatusCode: http.StatusNotFound,
		},
	}, actual.Pages[0].BrokenLinks)
	require.Equal(t, 4, len(requests))
	require.Zero(t, requests["mailto:help@simple.test"])
	require.Zero(t, requests["https://simple.test/blog/index.html#section"])
}

func TestAnalyzeChecksRootLinkWithoutRecrawlingRoot(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://example.test":
			return htmlResponse(http.StatusOK, `<a href="/">home</a><a href="https://example.test">home duplicate</a><a href="/missing">missing</a>`)
		case "https://example.test/":
			return htmlResponse(http.StatusOK, "home")
		case "https://example.test/missing":
			return htmlResponse(http.StatusNotFound, "not found")
		default:
			return nil, errors.New("unexpected request")
		}
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Depth:      2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Len(t, actual.Pages, 2)
	require.Equal(t, "https://example.test/missing", actual.Pages[1].URL)
	require.Equal(t, []crawler.BrokenLink{{
		URL:        "https://example.test/missing",
		StatusCode: http.StatusNotFound,
	}}, actual.Pages[0].BrokenLinks)
}

func TestAnalyzeCollectsSEO(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, `
			<html>
				<head>
					<title> Example &amp; test </title>
					<meta name="description" content=" Build &amp; ship ">
				</head>
				<body><h1> Hello <span>world</span> </h1></body>
			</html>
		`)
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, crawler.SEO{
		HasTitle:       true,
		Title:          "Example & test",
		HasDescription: true,
		Description:    "Build & ship",
		HasH1:          true,
	}, actual.Pages[0].SEO)
}

func TestAnalyzeReportsMissingSEO(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, "<html><body><p>content</p></body></html>")
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, crawler.SEO{}, actual.Pages[0].SEO)
}

func TestAnalyzeCrawlsInternalPagesWithinDepth(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name          string
		depth         int
		expectedPages []string
	}{
		{
			name:          "start page only",
			depth:         1,
			expectedPages: []string{"https://simple.test"},
		},
		{
			name:  "first-level internal pages",
			depth: 2,
			expectedPages: []string{
				"https://simple.test",
				"https://simple.test/first",
				"https://simple.test/second",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
				switch request.URL.String() {
				case "https://simple.test":
					return htmlResponse(http.StatusOK, `
						<a href="/first">first</a>
						<a href="/first">first duplicate</a>
						<a href="/second">second</a>
						<a href="https://external.test/page">external</a>
					`)
				case "https://simple.test/first", "https://simple.test/second", "https://external.test/page":
					return htmlResponse(http.StatusOK, "<html></html>")
				default:
					return nil, errors.New("unexpected request")
				}
			})}

			report, err := crawler.Analyze(t.Context(), crawler.Options{
				URL:        "https://simple.test",
				Depth:      testCase.depth,
				Timeout:    time.Second,
				HTTPClient: client,
			})

			require.NoError(t, err)
			actual := decodeReport(t, report)
			require.Len(t, actual.Pages, len(testCase.expectedPages))
			for index, expectedURL := range testCase.expectedPages {
				expectedDepth := 1
				if index == 0 {
					expectedDepth = 0
				}
				require.Equal(t, expectedURL, actual.Pages[index].URL)
				require.Equal(t, expectedDepth, actual.Pages[index].Depth)
			}
		})
	}
}

func TestAnalyzeUsesConfiguredWorkerPool(t *testing.T) {
	t.Parallel()

	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	var activeRequests atomic.Int32
	var maxActiveRequests atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://example.test":
			return htmlResponse(http.StatusOK, `<a href="/first">first</a><a href="/second">second</a>`)
		case "https://example.test/first":
			if firstCalls.Add(1) == 1 {
				return htmlResponse(http.StatusOK, "first")
			}
		case "https://example.test/second":
			if secondCalls.Add(1) == 1 {
				return htmlResponse(http.StatusOK, "second")
			}
		default:
			return nil, errors.New("unexpected request")
		}

		active := activeRequests.Add(1)
		for {
			maximum := maxActiveRequests.Load()
			if active <= maximum || maxActiveRequests.CompareAndSwap(maximum, active) {
				break
			}
		}
		started <- struct{}{}
		<-release
		activeRequests.Add(-1)

		return htmlResponse(http.StatusOK, "child")
	})}

	type analysisResult struct {
		report []byte
		err    error
	}
	done := make(chan analysisResult, 1)
	go func() {
		report, err := crawler.Analyze(t.Context(), crawler.Options{
			URL:         "https://example.test",
			Depth:       2,
			Timeout:     time.Second,
			Concurrency: 2,
			HTTPClient:  client,
		})
		done <- analysisResult{report: report, err: err}
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker pool did not process child pages concurrently")
		}
	}
	close(release)

	select {
	case result := <-done:
		require.NoError(t, result.err)
		require.Len(t, decodeReport(t, result.report).Pages, 3)
		require.Equal(t, int32(2), maxActiveRequests.Load())
	case <-time.After(time.Second):
		t.Fatal("analysis did not finish")
	}
}

func TestAnalyzeReturnsReportWhenContextIsCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("request must not be sent")
	})}

	report, err := crawler.Analyze(ctx, crawler.Options{
		URL:        "https://simple.test",
		Depth:      2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Empty(t, actual.Pages)
}

func TestAnalyzeLimitsRequestRate(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		delay       time.Duration
		rps         float64
		minInterval time.Duration
		maxInterval time.Duration
	}{
		{
			name:        "delay",
			delay:       25 * time.Millisecond,
			minInterval: 25 * time.Millisecond,
		},
		{
			name:        "rps overrides delay",
			delay:       500 * time.Millisecond,
			rps:         50,
			minInterval: 20 * time.Millisecond,
			maxInterval: 250 * time.Millisecond,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			requestTimes := make([]time.Time, 0, 2)
			client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
				requestTimes = append(requestTimes, time.Now())
				if request.URL.String() == "https://simple.test" {
					return htmlResponse(http.StatusOK, `<a href="/page">page</a>`)
				}

				return htmlResponse(http.StatusOK, "<html></html>")
			})}

			report, err := crawler.Analyze(t.Context(), crawler.Options{
				URL:        "https://simple.test",
				Depth:      1,
				Delay:      testCase.delay,
				RPS:        testCase.rps,
				Timeout:    time.Second,
				HTTPClient: client,
			})

			require.NoError(t, err)
			actual := decodeReport(t, report)
			require.Len(t, actual.Pages, 1)
			require.Equal(t, "ok", actual.Pages[0].Status)
			require.Len(t, requestTimes, 2)

			interval := requestTimes[1].Sub(requestTimes[0])
			require.GreaterOrEqual(t, interval, testCase.minInterval)
			if testCase.maxInterval > 0 {
				require.Less(t, interval, testCase.maxInterval)
			}
		})
	}
}

func TestAnalyzeStopsRateLimitWaitOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstRequestSent := make(chan struct{})
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == "https://simple.test" {
			close(firstRequestSent)
			return htmlResponse(http.StatusOK, `<a href="/page">page</a>`)
		}

		return htmlResponse(http.StatusOK, "<html></html>")
	})}

	type result struct {
		report []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		report, err := crawler.Analyze(ctx, crawler.Options{
			URL:        "https://simple.test",
			Depth:      1,
			Delay:      time.Second,
			Timeout:    time.Second,
			HTTPClient: client,
		})
		done <- result{report: report, err: err}
	}()

	select {
	case <-firstRequestSent:
	case <-time.After(time.Second):
		t.Fatal("first request was not sent")
	}
	cancel()

	select {
	case actual := <-done:
		require.NoError(t, actual.err)
		require.Len(t, decodeReport(t, actual.report).Pages, 1)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("analysis did not stop after context cancellation")
	}
}

func TestAnalyzeRetriesNetworkFailureUntilSuccess(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("temporary network failure")
		}

		return htmlResponse(http.StatusOK, "<html></html>")
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Retries:    2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, "ok", actual.Pages[0].Status)
	require.Equal(t, 2, attempts)
}

func TestAnalyzeRetriesTemporaryHTTPStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			attempts := 0
			client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return htmlResponse(status, "temporary failure")
				}

				return htmlResponse(http.StatusOK, "<html></html>")
			})}

			report, err := crawler.Analyze(t.Context(), crawler.Options{
				URL:        "https://example.test",
				Retries:    2,
				Timeout:    time.Second,
				HTTPClient: client,
			})

			require.NoError(t, err)
			actual := decodeReport(t, report)
			require.Equal(t, "ok", actual.Pages[0].Status)
			require.Equal(t, 2, attempts)
		})
	}
}

func TestAnalyzeStopsAfterRetryLimit(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("temporary network failure")
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Retries:    2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, "error", actual.Pages[0].Status)
	require.Contains(t, actual.Pages[0].Error, "temporary network failure")
	require.Equal(t, 3, attempts)
}

func TestAnalyzeDoesNotRetryPermanentHTTPStatus(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		return htmlResponse(http.StatusNotFound, "not found")
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Retries:    2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, "error", actual.Pages[0].Status)
	require.Equal(t, 1, attempts)
}

func TestAnalyzeReportsLastBrokenLinkAttempt(t *testing.T) {
	t.Parallel()

	busyAttempts := 0
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == "https://example.test" {
			return htmlResponse(http.StatusOK, `<a href="/busy">busy</a>`)
		}
		busyAttempts++

		return htmlResponse(http.StatusServiceUnavailable, "unavailable")
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Depth:      1,
		Retries:    2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, []crawler.BrokenLink{{
		URL:        "https://example.test/busy",
		StatusCode: http.StatusServiceUnavailable,
	}}, actual.Pages[0].BrokenLinks)
	require.Equal(t, 3, busyAttempts)
}

func TestAnalyzeStopsRetriesWhenContextIsCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstAttempt := make(chan struct{})
	attempts := 0
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		close(firstAttempt)
		return nil, errors.New("temporary network failure")
	})}

	type result struct {
		report []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		report, err := crawler.Analyze(ctx, crawler.Options{
			URL:        "https://example.test",
			Retries:    2,
			Timeout:    time.Second,
			HTTPClient: client,
		})
		done <- result{report: report, err: err}
	}()

	select {
	case <-firstAttempt:
	case <-time.After(time.Second):
		t.Fatal("first request was not sent")
	}
	cancel()

	select {
	case actual := <-done:
		require.NoError(t, actual.err)
		require.Len(t, decodeReport(t, actual.report).Pages, 1)
		require.Equal(t, 1, attempts)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("analysis did not stop after context cancellation")
	}
}

func TestAnalyzeReportsAssets(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://example.test":
			return htmlResponse(http.StatusOK, `
				<img src="/static/logo.png">
				<link rel="stylesheet" href="/static/site.css">
				<script src="/static/broken.js"></script>
			`)
		case "https://example.test/static/logo.png":
			return responseWithContentLength(http.StatusOK, "logo", -1)
		case "https://example.test/static/site.css":
			return responseWithContentLength(http.StatusOK, "body", 4)
		case "https://example.test/static/broken.js":
			return responseWithContentLength(http.StatusServiceUnavailable, "down", 4)
		default:
			return nil, errors.New("unexpected request")
		}
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Depth:      1,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Equal(t, []crawler.Asset{
		{
			URL:        "https://example.test/static/logo.png",
			Type:       "image",
			StatusCode: http.StatusOK,
			SizeBytes:  4,
		},
		{
			URL:        "https://example.test/static/broken.js",
			Type:       "script",
			StatusCode: http.StatusServiceUnavailable,
			SizeBytes:  4,
			Error:      "unexpected http status: 503",
		},
		{
			URL:        "https://example.test/static/site.css",
			Type:       "style",
			StatusCode: http.StatusOK,
			SizeBytes:  4,
		},
	}, actual.Pages[0].Assets)
}

func TestAnalyzeCachesRepeatedAssetsAcrossPages(t *testing.T) {
	t.Parallel()

	assetRequests := 0
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://example.test":
			return htmlResponse(http.StatusOK, `
				<a href="/next">next</a>
				<img src="/static/logo.png">
				<img src="/static/logo.png">
			`)
		case "https://example.test/next":
			return htmlResponse(http.StatusOK, `<img src="/static/logo.png">`)
		case "https://example.test/static/logo.png":
			assetRequests++
			return responseWithContentLength(http.StatusOK, "logo", -1)
		default:
			return nil, errors.New("unexpected request")
		}
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.test",
		Depth:      2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Len(t, actual.Pages, 2)
	require.Equal(t, 1, assetRequests)
	require.Equal(t, []crawler.Asset{{
		URL:        "https://example.test/static/logo.png",
		Type:       "image",
		StatusCode: http.StatusOK,
		SizeBytes:  4,
	}}, actual.Pages[0].Assets)
	require.Equal(t, actual.Pages[0].Assets, actual.Pages[1].Assets)
}

func TestAnalyzeReturnsExpectedJSON(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://example.com":
			return htmlResponse(http.StatusOK, `
				<title>Example title</title>
				<meta name="description" content="Example description">
				<h1>Example heading</h1>
				<a href="/missing">missing</a>
				<img src="/static/logo.png">
			`)
		case "https://example.com/missing":
			return htmlResponse(http.StatusNotFound, "not found")
		case "https://example.com/static/logo.png":
			return responseWithContentLength(http.StatusOK, "asset", 12345)
		default:
			return nil, errors.New("unexpected request")
		}
	})}

	report, err := crawler.Analyze(t.Context(), crawler.Options{
		URL:        "https://example.com",
		Depth:      1,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	require.JSONEq(t, `{
		"root_url": "https://example.com",
		"depth": 1,
		"generated_at": "2024-06-01T12:34:56Z",
		"pages": [{
			"url": "https://example.com",
			"depth": 0,
			"http_status": 200,
			"status": "ok",
			"seo": {
				"has_title": true,
				"title": "Example title",
				"has_description": true,
				"description": "Example description",
				"has_h1": true
			},
			"broken_links": [{
				"url": "https://example.com/missing",
				"status_code": 404
			}],
			"assets": [{
				"url": "https://example.com/static/logo.png",
				"type": "image",
				"status_code": 200,
				"size_bytes": 12345
			}],
			"discovered_at": "2024-06-01T12:34:56Z"
		}]
	}`, string(normalizeReportTimes(t, report)))
}

func TestAnalyzeIndentJSONOnlyChangesFormatting(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, "<title>Example</title>")
	})}
	opts := crawler.Options{
		URL:        "https://example.test",
		Depth:      1,
		Timeout:    time.Second,
		HTTPClient: client,
	}

	compact, err := crawler.Analyze(t.Context(), opts)
	require.NoError(t, err)
	opts.IndentJSON = true
	indented, err := crawler.Analyze(t.Context(), opts)

	require.NoError(t, err)
	require.NotContains(t, string(compact), "\n")
	require.Contains(t, string(indented), "\n")
	require.JSONEq(t, string(normalizeReportTimes(t, compact)), string(normalizeReportTimes(t, indented)))
}

func decodeReport(t testing.TB, report []byte) crawler.Report {
	t.Helper()

	var actual crawler.Report
	require.NoError(t, json.Unmarshal(report, &actual))

	return actual
}

func normalizeReportTimes(t testing.TB, report []byte) []byte {
	t.Helper()

	actual := decodeReport(t, report)
	actual.GeneratedAt = "2024-06-01T12:34:56Z"
	for index := range actual.Pages {
		actual.Pages[index].DiscoveredAt = "2024-06-01T12:34:56Z"
	}
	normalized, err := json.Marshal(actual)
	require.NoError(t, err)

	return normalized
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func htmlResponse(status int, body string) (*http.Response, error) {
	return responseWithContentLength(status, body, int64(len(body)))
}

func responseWithContentLength(status int, body string, contentLength int64) (*http.Response, error) {
	return &http.Response{
		StatusCode:    status,
		ContentLength: contentLength,
		Body:          io.NopCloser(strings.NewReader(body)),
		Header:        make(http.Header),
	}, nil
}
