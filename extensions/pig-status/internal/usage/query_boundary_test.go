package usage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryRevalidatesOnlyAfterAdmission(t *testing.T) {
	stop := occupyQuerySlots(t)
	var requests, revalidations atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	auth := deepSeekAuth()
	auth.Revalidate = func(context.Context) error {
		revalidations.Add(1)
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	result := make(chan providerQueryResult, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		report, err := client.Query(ctx, "deepseek", auth, time.Hour)
		result <- providerQueryResult{report: report, err: err}
	})
	t.Cleanup(func() { cancel(); workers.Wait() })

	queued := awaitProviderQuery(t, result)
	queuedRevalidations, queuedRequests := revalidations.Load(), requests.Load()
	stop()
	report, err := client.Query(t.Context(), "deepseek", auth, time.Second)

	requireRequestCount(t, queuedRevalidations, 0)
	requireDeadline(t, queued.err)
	requireRequestCount(t, queuedRequests, 0)
	requireQuerySuccess(t, report, err)
	requireRequestCount(t, revalidations.Load(), 2)
	requireRequestCount(t, requests.Load(), 1)
}

type queryRoundTripper func(*http.Request) (*http.Response, error)

func (f queryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type queryErrorBody struct {
	err    error
	reads  atomic.Int32
	closes atomic.Int32
}

func (b *queryErrorBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, b.err
}

func (b *queryErrorBody) Close() error {
	b.closes.Add(1)
	return nil
}

func requireNonContextFailure(t testing.TB, err error) {
	t.Helper()
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected non-context refusal, got %v", err)
	}
}

func TestQuerySuppressesDependencyErrorDetails(t *testing.T) {
	for _, tt := range []struct {
		name       string
		response   func(*queryErrorBody) (*http.Response, error)
		wantReads  int32
		wantCloses int32
	}{
		{
			name: "transport failure",
			response: func(body *queryErrorBody) (*http.Response, error) {
				return nil, body.err
			},
		},
		{
			name: "body read failure",
			response: func(body *queryErrorBody) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
			},
			wantReads:  1,
			wantCloses: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goodClient := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeQueryFixture(t, w, []byte(deepSeekBalance))
			})
			body := &queryErrorBody{err: errors.New("fixture-secret fixture-custom-secret account-canary-89342 raw-provider-canary\x1b[31m\n\r")}
			var requests atomic.Int32
			client := Client{Transport: queryRoundTripper(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return tt.response(body)
			})}

			goodReport, goodErr := goodClient.Query(t.Context(), "deepseek", deepSeekAuth(), time.Second)
			report, err := client.Query(t.Context(), "deepseek", deepSeekAuth(), time.Hour)

			requireQuerySuccess(t, goodReport, goodErr)
			requireRequestCount(t, requests.Load(), 1)
			requireRequestCount(t, body.reads.Load(), tt.wantReads)
			requireRequestCount(t, body.closes.Load(), tt.wantCloses)
			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireSafeText(t, fmt.Sprint(report))
			requireNonContextFailure(t, err)
		})
	}
}

var _ io.ReadCloser = (*queryErrorBody)(nil)
