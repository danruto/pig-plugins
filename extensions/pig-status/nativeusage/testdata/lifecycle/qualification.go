package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	native "github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

var qualified = struct {
	sync.Mutex
	used    int
	fail    bool
	hold    bool
	release chan struct{}
	targets bool
}{used: 20, targets: true}

func qualificationEnabled() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("NA_OUTPUT"), "qualification"))
	return err == nil
}

func qualificationResponse(w http.ResponseWriter, r *http.Request) {
	if !qualificationEnabled() {
		return
	}
	qualified.Lock()
	used, fail, targets := qualified.used, qualified.fail, qualified.targets
	qualified.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if fail {
		record("failure-response", true)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"fixture-private-canary\u001b[31m"}`)
		return
	}
	if r.Host == "api.fireworks.ai" && r.URL.Path == "/v1/accounts" {
		record("discovery", targets)
		if targets {
			fmt.Fprint(w, `{"accounts":[{"name":"accounts/first"}]}`)
			return
		}
		fmt.Fprint(w, `{"accounts":[]}`)
		return
	}
	if r.Host == "api.fireworks.ai" {
		record("billing", r.URL.Path)
		fmt.Fprint(w, `{"lineItems":[{"series":"SERVERLESS","totalCost":{"currencyCode":"USD","units":"2","nanos":500000000}}]}`)
		return
	}
	if r.Host == "openrouter.ai" {
		fmt.Fprintf(w, `{"data":{"usage":%d,"limit":100,"limit_remaining":%d,"is_free_tier":false}}`, used, 100-used)
		return
	}
	fmt.Fprint(w, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.50","granted_balance":"0","topped_up_balance":"12.50"}]}`)
}

type qualifiedTransport struct{ base http.RoundTripper }

func (t *qualifiedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	qualified.Lock()
	hold := qualified.hold
	qualified.hold = false
	release := qualified.release
	qualified.Unlock()
	if !hold {
		return response, nil
	}
	data, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	response.Body = &lateBody{Reader: bytes.NewReader(data), ctx: r.Context(), release: release}
	record("late-held", true)
	return response, nil
}

type lateBody struct {
	*bytes.Reader
	ctx     context.Context
	release <-chan struct{}
	once    sync.Once
}

func (b *lateBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-b.release:
		case <-b.ctx.Done():
			record("late-cancelled", b.ctx.Err().Error())
			select {
			case <-b.release:
			case <-timer.C:
				panic("fixture late response release timeout")
			}
		case <-timer.C:
			panic("fixture late response release timeout")
		}
		record("late-body", true)
	})
	return b.Reader.Read(p)
}

func (b *lateBody) Close() error { return nil }

func registerQualification(ext *sdk.Extension, runtime *native.Runtime, transport http.RoundTripper) {
	collector := usage.NewCollector(usage.Client{Transport: transport})
	ext.RegisterCommand("lifecycle-stop", sdk.CommandOptions{Handler: func(_ sdk.Context, _ string) error {
		err := runtime.Close()
		record("stopped", err == nil)
		return err
	}})
	ext.RegisterCommand("lifecycle-response", sdk.CommandOptions{Handler: func(_ sdk.Context, args string) error {
		var input struct {
			Used                int
			Fail, Hold, Targets bool
		}
		if err := json.Unmarshal([]byte(args), &input); err != nil {
			return err
		}
		qualified.Lock()
		qualified.used, qualified.fail, qualified.hold, qualified.targets = input.Used, input.Fail, input.Hold, input.Targets
		if input.Hold {
			qualified.release = make(chan struct{})
		}
		qualified.Unlock()
		record("response-set", input)
		return nil
	}})
	ext.RegisterCommand("lifecycle-release", sdk.CommandOptions{Handler: func(_ sdk.Context, _ string) error {
		qualified.Lock()
		defer qualified.Unlock()
		if qualified.release == nil {
			return errors.New("fixture response not held")
		}
		close(qualified.release)
		qualified.release = nil
		return nil
	}})
	ext.RegisterCommand("lifecycle-owner", sdk.CommandOptions{Handler: updateOwner})
	ext.RegisterCommand("lifecycle-collect", sdk.CommandOptions{Handler: func(call sdk.Context, name string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := collector.Refresh(ctx, usage.Resolver{Host: hostPort{call: call}})
		view := collector.Snapshot(time.Now())
		record("cache-view", map[string]any{"name": name, "windows": view.Windows, "reason": view.Reason, "failed": err != nil})
		return nil
	}})
	ext.RegisterCommand("lifecycle-cache-config", sdk.CommandOptions{Handler: func(_ sdk.Context, target string) error {
		collector.Configure(usage.CollectionSettings{Targets: map[string]string{"fireworks": target}})
		record("cache-configured", target)
		return nil
	}})
	ext.RegisterCommand("lifecycle-session", sdk.CommandOptions{Handler: func(call sdk.Context, _ string) error {
		before, err := call.GetSessionID()
		if err != nil {
			return err
		}
		result, err := call.NewSession(nil)
		if err != nil {
			return err
		}
		after, staleErr := call.GetSessionID()
		record("stale-context-rejected", staleErr != nil)
		record("session-changed", map[string]any{"before": before, "after": after, "result": result})
		return nil
	}})
}

func updateOwner(call sdk.Context, args string) error {
	var input struct {
		Provider, Key string
		MaxTokens     int
		Select        bool
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return err
	}
	base := "https://openrouter.ai/api/v1"
	if input.Provider == "fireworks" {
		base = "https://api.fireworks.ai/inference/v1"
	}
	if input.Provider == "xai" {
		base = "https://api.x.ai/v1"
	}
	model := map[string]any{"id": "fixture", "name": "Fixture", "reasoning": false, "input": []string{"text"}, "contextWindow": 4096, "maxTokens": input.MaxTokens, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}
	if err := call.ModelRegistry().RegisterProvider(input.Provider, sdk.ProviderConfig{"baseUrl": base, "api": "openai-completions", "apiKey": input.Key, "models": []map[string]any{model}}); err != nil {
		return err
	}
	if input.Select {
		ok, err := call.SetModel(input.Provider + "/fixture")
		if err != nil || !ok {
			return errors.New("fixture owner selection failed")
		}
	}
	record("owner-updated", map[string]any{"provider": input.Provider, "maxTokens": input.MaxTokens, "selected": input.Select, "rotated": strings.Contains(input.Key, "rotated")})
	return nil
}
