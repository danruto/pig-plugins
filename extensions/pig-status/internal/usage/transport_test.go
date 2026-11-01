package usage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const deepSeekBalance = `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"1.000000001","granted_balance":"0.000000001","topped_up_balance":"1.000000000"}]}`

func fixtureClient(t testing.TB, handler http.HandlerFunc) Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	return Client{Transport: transport}
}

func deepSeekAuth() ResolvedAuth {
	return ResolvedAuth{
		ModelBaseURL: "https://api.deepseek.com/v1",
		Headers: http.Header{
			"Authorization": {"Bearer fixture-secret"},
			"X-Private-Key": {"fixture-custom-secret"},
		},
		Revalidate: func(context.Context) error { return nil },
	}
}

func TestQueryProviderEndpoints(t *testing.T) {
	for _, tt := range []struct {
		provider string
		base     string
		host     string
		path     string
		body     string
		currency string
	}{
		{"deepseek", "https://api.deepseek.com/v1", "api.deepseek.com", "/user/balance", deepSeekBalance, "USD"},
		{"vercel-ai-gateway", "https://ai-gateway.vercel.sh/v1", "ai-gateway.vercel.sh", "/v1/credits", `{"balance":"1.000000001","total_used":"0.000000001"}`, "USD"},
		{"moonshotai", "https://api.moonshot.ai/v1", "api.moonshot.ai", "/v1/users/me/balance", `{"code":0,"status":true,"data":{"available_balance":1,"voucher_balance":2,"cash_balance":-1}}`, "USD"},
		{"moonshotai-cn", "https://api.moonshot.cn/v1", "api.moonshot.cn", "/v1/users/me/balance", `{"code":0,"status":true,"data":{"available_balance":1,"voucher_balance":2,"cash_balance":-1}}`, "CNY"},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Host != tt.host || r.URL.Path != tt.path || r.URL.RawQuery != "" {
					t.Errorf("incorrect endpoint: %s%s", r.Host, r.URL)
				}
				_, _ = io.WriteString(w, tt.body)
			})
			auth := deepSeekAuth()
			auth.ModelBaseURL = tt.base
			report, err := client.Query(t.Context(), tt.provider, auth, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if report.ProviderID != tt.provider || len(report.Metrics) == 0 || report.Metrics[len(report.Metrics)-1].Currency != tt.currency {
				t.Fatalf("incorrect provider report: %+v", report)
			}
		})
	}
}

func TestQueryResponseBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		wantOK bool
	}{
		{"exact balance", 200, deepSeekBalance, true},
		{"account and terminal data omitted", 200, strings.TrimSuffix(deepSeekBalance, "}") + `,"account_id":"account-canary-89342","message":"raw-provider-canary fixture-secret\u001b[31m\n\r"}`, true},
		{"account and terminal malformed body", 200, "account-canary-89342 raw-provider-canary fixture-secret\x1b[31m\n\r", false},
		{"account and terminal HTTP failure", 503, "account-canary-89342 raw-provider-canary fixture-secret\x1b[31m\n\r", false},
		{"account and terminal invalid amount", 200, strings.Replace(deepSeekBalance, "1.000000001", `account-canary-89342 raw-provider-canary fixture-secret\u001b[31m\n\r`, 1), false},
		{"exact body limit", 200, deepSeekBalance + strings.Repeat(" ", maxSuccessBodyBytes-len(deepSeekBalance)), true},
		{"overflow", 200, deepSeekBalance + strings.Repeat(" ", maxSuccessBodyBytes-len(deepSeekBalance)+1), false},
		{"bad JSON", 200, "fixture-secret invalid", false},
		{"array", 200, "[]", false},
		{"null", 200, "null", false},
		{"redirect", 302, "fixture-secret", false},
		{"error", 503, strings.Repeat("fixture-secret ", 1000), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/user/balance" || r.Host != "api.deepseek.com" || r.Method != http.MethodGet {
					t.Errorf("unexpected request: %s %s %s", r.Method, r.Host, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer fixture-secret" || r.Header.Get("X-Private-Key") != "fixture-custom-secret" || r.UserAgent() != "pi-usage" {
					t.Error("resolved authentication or user agent was not preserved")
				}
				w.Header().Set("Location", "https://canary.invalid/leak?secret=fixture-secret")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			report, err := client.Query(t.Context(), "deepseek", deepSeekAuth(), time.Second)
			if tt.wantOK {
				if err != nil {
					t.Fatal(err)
				}
				if report.ProviderID != "deepseek" || len(report.Metrics) != 4 || report.Metrics[1].Value != "1.000000001" || report.CapturedAt == 0 {
					t.Fatalf("incorrect report: %+v", report)
				}
				requireSafeText(t, fmt.Sprint(report))
			} else {
				if err == nil {
					t.Fatal("unsafe response accepted")
				}
				if strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "canary.invalid") || len(err.Error()) > 200 {
					t.Fatalf("unsafe diagnostic: %s", err)
				}
				requireSafeDiagnostic(t, err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d; a redirect must never be followed", requests.Load())
			}
		})
	}
}

func TestQueryValidatesModelOriginBeforeRequest(t *testing.T) {
	for _, tt := range []struct {
		provider string
		base     string
		wantOK   bool
	}{
		{"deepseek", "https://api.deepseek.com:443/v1", true},
		{"deepseek", "https://API.DEEPSEEK.COM/v1", true},
		{"deepseek", "http://api.deepseek.com/v1", false},
		{"deepseek", "https://api.deepseek.com:444/v1", false},
		{"deepseek", "https://api.deepseek.com.attacker.invalid/v1", false},
		{"deepseek", "https://fixture-secret@api.deepseek.com/v1", false},
		{"deepseek", "https://api.deepseek.com:bad/v1", false},
		{"deepseek", "", false},
		{"moonshotai-cn", "https://api.moonshot.ai/v1", false},
		{"moonshotai", "https://api.moonshot.cn/v1", false},
		{"unknown", "https://api.deepseek.com/v1", false},
	} {
		t.Run(tt.provider+" "+tt.base, func(t *testing.T) {
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, deepSeekBalance)
			})
			auth := deepSeekAuth()
			auth.ModelBaseURL = tt.base
			_, err := client.Query(t.Context(), tt.provider, auth, time.Second)
			if tt.wantOK {
				if err != nil || requests.Load() != 1 {
					t.Fatalf("official origin rejected: %v; requests = %d", err, requests.Load())
				}
			} else if err == nil || requests.Load() != 0 || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("origin guard failed: %v; requests = %d", err, requests.Load())
			}
		})
	}
}

func TestQueryRejectsChangedAuthentication(t *testing.T) {
	for _, tt := range []struct {
		name         string
		failAt       int
		wantRequests int32
		wantOK       bool
	}{
		{"current", 0, 1, true},
		{"missing guard", -1, 0, false},
		{"before request", 1, 0, false},
		{"before publication", 2, 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, deepSeekBalance)
			})
			auth := deepSeekAuth()
			calls := 0
			auth.Revalidate = func(context.Context) error {
				calls++
				if calls == tt.failAt {
					return errors.New("fixture-secret rotated")
				}
				return nil
			}
			if tt.failAt == -1 {
				auth.Revalidate = nil
			}

			report, err := client.Query(t.Context(), "deepseek", auth, time.Second)
			if tt.wantOK {
				if err != nil || len(report.Metrics) != 4 {
					t.Fatalf("current credentials rejected: %+v, %v", report, err)
				}
			} else if err == nil || report.ProviderID != "" || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("authentication change was not refused safely: %+v, %v", report, err)
			}
			if requests.Load() != tt.wantRequests {
				t.Fatalf("requests = %d, want %d", requests.Load(), tt.wantRequests)
			}
		})
	}
}

func TestQueryDeadlineCoversAuthentication(t *testing.T) {
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, deepSeekBalance)
	})
	auth := deepSeekAuth()
	auth.Revalidate = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	_, err := client.Query(t.Context(), "deepseek", auth, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 0 {
		t.Fatalf("revalidation exceeded query budget: %v; requests = %d", err, requests.Load())
	}
}

func TestQueryDeadlineCoversResponseBody(t *testing.T) {
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	_, err := client.Query(t.Context(), "deepseek", deepSeekAuth(), 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestQueryCancellationStopsHeldRequest(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
			_, _ = io.WriteString(w, deepSeekBalance)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.Query(ctx, "deepseek", deepSeekAuth(), time.Second)
		result <- err
	}()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("request did not start: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancelled", err)
	}
}

func requireQuerySuccess(t testing.TB, report Report, err error) {
	t.Helper()
	if err != nil || report.ProviderID != "deepseek" || len(report.Metrics) != 4 {
		t.Fatalf("valid query did not produce balance report: %+v; %v", report, err)
	}
}

func requireQueryRefused(t testing.TB, report Report, err error) {
	t.Helper()
	if err == nil || report.ProviderID != "" || len(report.Metrics) != 0 {
		t.Fatalf("unsafe query returned a report: %+v; %v", report, err)
	}
}

func requireRequestCount(t testing.TB, got, want int32) {
	t.Helper()
	if got != want {
		t.Fatalf("HTTPS requests = %d, want %d", got, want)
	}
}

func requireSafeDiagnostic(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected sanitized refusal")
	}
	requireSafeText(t, err.Error())
}

func requireSafeText(t testing.TB, text string) {
	t.Helper()
	for _, canary := range []string{"fixture-secret", "fixture-custom-secret", "account-canary-89342", "raw-provider-canary", "\x1b", "\n", "\r"} {
		if strings.Contains(text, canary) {
			t.Fatal("query output disclosed canary or terminal control")
		}
	}
}

func requireString(t testing.TB, got, want string) {
	t.Helper()
	if got != want {
		t.Fatal("authorization did not preserve validated snapshot")
	}
}

func requireDeadline(t testing.TB, err error) {
	t.Helper()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func requireDurationAtMost(t testing.TB, got, limit time.Duration) {
	t.Helper()
	if got > limit {
		t.Fatalf("total query duration = %s, exceeds %s including admission", got, limit)
	}
}

func TestQueryRejectsUnsafeExplicitAuthorization(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers http.Header
	}{
		{"missing", nil},
		{"removed", http.Header{"Authorization": nil}},
		{"empty", http.Header{"Authorization": {""}}},
		{"whitespace", http.Header{"Authorization": {" \t "}}},
		{"duplicate values", http.Header{"Authorization": {"Bearer fixture-secret", "Bearer account-canary-89342"}}},
		{"duplicate case", http.Header{"Authorization": {"Bearer fixture-secret"}, "authorization": {"Bearer account-canary-89342"}}},
		{"non bearer deepseek", http.Header{"Authorization": {"Basic account-canary-89342"}}},
		{"empty bearer", http.Header{"Authorization": {"Bearer \t"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, deepSeekBalance)
			})
			auth := deepSeekAuth()
			auth.Headers = tt.headers

			goodReport, goodErr := client.Query(t.Context(), "deepseek", deepSeekAuth(), time.Second)
			report, err := client.Query(t.Context(), "deepseek", auth, time.Second)

			requireQuerySuccess(t, goodReport, goodErr)
			requireQueryRefused(t, report, err)
			requireRequestCount(t, requests.Load(), 1)
			requireSafeDiagnostic(t, err)
		})
	}
}

func TestQueryCopiesValidatedAuthorizationBeforeRevalidation(t *testing.T) {
	authorization := make(chan string, 1)
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		authorization <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, deepSeekBalance)
	})
	auth := deepSeekAuth()
	auth.Revalidate = func(context.Context) error {
		auth.Headers.Set("Authorization", "Bearer account-canary-89342")
		return nil
	}

	report, err := client.Query(t.Context(), "deepseek", auth, time.Second)

	requireQuerySuccess(t, report, err)
	requireString(t, <-authorization, "Bearer fixture-secret")
}

func occupyQuerySlots(t testing.TB) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	started := make(chan struct{}, 2)
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	for range 2 {
		workers.Go(func() {
			_, _ = client.Query(ctx, "deepseek", deepSeekAuth(), time.Hour)
		})
	}
	stop := sync.OnceFunc(func() { cancel(); workers.Wait() })
	t.Cleanup(stop)
	<-started
	<-started
	return stop
}

func TestQueryBoundsAdmissionAcrossClients(t *testing.T) {
	stop := occupyQuerySlots(t)
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, deepSeekBalance)
	})

	_, err := client.Query(t.Context(), "deepseek", deepSeekAuth(), 100*time.Millisecond)
	stop()
	report, successErr := client.Query(t.Context(), "deepseek", deepSeekAuth(), time.Second)

	requireDeadline(t, err)
	requireRequestCount(t, requests.Load(), 1)
	requireQuerySuccess(t, report, successErr)
}

func TestQueryCapsTotalBudgetIncludingAdmission(t *testing.T) {
	stop := occupyQuerySlots(t)
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 18*time.Second)
	defer cancel()
	started := time.Now()

	_, err := client.Query(ctx, "deepseek", deepSeekAuth(), time.Hour)
	elapsed := time.Since(started)
	stop()
	goodClient := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, deepSeekBalance)
	})
	report, goodErr := goodClient.Query(t.Context(), "deepseek", deepSeekAuth(), time.Second)

	requireDeadline(t, err)
	requireDurationAtMost(t, elapsed, 16*time.Second)
	requireQuerySuccess(t, report, goodErr)
}
