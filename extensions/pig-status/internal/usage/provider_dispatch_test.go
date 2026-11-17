package usage

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryRefusesUnsupportedProviderWithSentinelError(t *testing.T) {
	var requests atomic.Int32
	client := Client{Transport: queryRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected provider request")
	})}

	report, err := client.Query(t.Context(), "not-a-provider", deepSeekAuth(), time.Second)

	requireQueryRefused(t, report, err)
	requireSentinelError(t, err, ErrUsageUnsupported)
	requireRequestCount(t, requests.Load(), 0)
}

func TestQueryRefusesForeignProviderOriginBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	client := Client{Transport: queryRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected provider request")
	})}
	auth := deepSeekAuth()
	auth.ModelBaseURL = "https://example.test/v1"

	report, err := client.Query(t.Context(), "deepseek", auth, time.Second)

	requireQueryRefused(t, report, err)
	requireSentinelError(t, err, ErrUsageOrigin)
	requireRequestCount(t, requests.Load(), 0)
}

func requireSentinelError(tb testing.TB, err error, want error) {
	tb.Helper()
	if !errors.Is(err, want) {
		tb.Fatalf("errors.Is(%v, %v) is false", err, want)
	}
}
