package crawler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
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
		Depth:      2,
		Timeout:    time.Second,
		HTTPClient: client,
	})

	require.NoError(t, err)
	actual := decodeReport(t, report)
	require.Len(t, actual.Pages, 1)
	require.Equal(t, "ok", actual.Pages[0].Status)
	require.Equal(t, []crawler.BrokenLink{
		{URL: "https://simple.test/assets/ghost.css", StatusCode: http.StatusNotFound},
		{URL: "https://cdn.simple.test/app.js", Error: `Get "https://cdn.simple.test/app.js": host unavailable`},
	}, actual.Pages[0].BrokenLinks)
	require.Equal(t, 4, len(requests))
	require.Zero(t, requests["mailto:help@simple.test"])
	require.Zero(t, requests["https://simple.test/blog/index.html#section"])
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

func decodeReport(t testing.TB, report []byte) crawler.Report {
	t.Helper()

	var actual crawler.Report
	require.NoError(t, json.Unmarshal(report, &actual))

	return actual
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func htmlResponse(status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}
