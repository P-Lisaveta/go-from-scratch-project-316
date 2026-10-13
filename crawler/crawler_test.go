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
	actual := decodeReport(t, report)
	require.Equal(t, []crawler.Page{{
		URL:    "https://example.test",
		Status: "error",
		Error:  `Get "https://example.test": network unavailable`,
	}}, actual.Pages)
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
			require.Equal(t, []crawler.Page{{
				URL:        "https://example.test",
				HTTPStatus: testCase.status,
				Status:     "error",
				Error:      "unexpected http status: " + strconv.Itoa(testCase.status),
			}}, actual.Pages)
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
