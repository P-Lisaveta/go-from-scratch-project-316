package crawler_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

	var actual crawler.Report
	require.NoError(t, json.Unmarshal(report, &actual))
	require.Equal(t, "https://example.test", actual.RootURL)
	require.Equal(t, 1, actual.Depth)
	require.NotEmpty(t, actual.GeneratedAt)
	require.Equal(t, []crawler.Page{{
		URL:        "https://example.test",
		Depth:      0,
		HTTPStatus: http.StatusOK,
		Status:     "ok",
	}}, actual.Pages)
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
	require.Contains(t, string(report), `"http_status":0`)
	require.Contains(t, string(report), `"status":"error"`)
	require.Contains(t, string(report), "network unavailable")
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
