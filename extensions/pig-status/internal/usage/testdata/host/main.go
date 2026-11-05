package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

var logMu sync.Mutex
var requestsMu sync.Mutex
var activeRequests int

func record(row any) {
	logMu.Lock()
	defer logMu.Unlock()
	file, err := os.OpenFile(filepath.Join(os.Getenv("NA_OUTPUT"), "native.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic("fixture receipt unavailable")
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(row); err != nil {
		panic("fixture receipt unavailable")
	}
}

func serve(w http.ResponseWriter, r *http.Request) {
	requestsMu.Lock()
	activeRequests++
	record(map[string]any{"event": "request-active", "active": activeRequests})
	requestsMu.Unlock()
	defer func() {
		requestsMu.Lock()
		activeRequests--
		record(map[string]any{"event": "request-active", "active": activeRequests})
		requestsMu.Unlock()
	}()
	tag := "unrecognized"
	for name, value := range map[string]string{"initial": "Bearer synthetic-initial", "rotated": "Bearer synthetic-rotated", "selected": "Bearer synthetic-selected", "codex": "Bearer " + codexToken} {
		if r.Header.Get("Authorization") == value {
			tag = name
		}
	}
	record(map[string]any{"event": "request", "host": r.Host, "path": r.URL.Path, "authTag": tag, "accountHeader": r.Header.Get("ChatGPT-Account-Id") != ""})
	root := os.Getenv("NA_OUTPUT")
	if _, err := os.Stat(filepath.Join(root, "hold")); err == nil {
		for {
			if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
				break
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Host == "api.deepseek.com":
		fmt.Fprint(w, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.50","granted_balance":"0","topped_up_balance":"12.50"}]}`)
	case r.Host == "openrouter.ai":
		fmt.Fprint(w, `{"data":{"label":"fixture","usage":2,"limit":10,"limit_remaining":8,"is_free_tier":false}}`)
	case r.Host == "api.fireworks.ai" && r.URL.Path == "/v1/accounts":
		fmt.Fprint(w, `{"accounts":[{"name":"accounts/first"},{"name":"accounts/second"}]}`)
	case r.Host == "api.fireworks.ai":
		fmt.Fprint(w, `{"lineItems":[{"series":"SERVERLESS","totalCost":{"currencyCode":"USD","units":"2","nanos":500000000}}]}`)
	case r.Host == "chatgpt.com" && strings.HasSuffix(r.URL.Path, "chatpass/apps"):
		fmt.Fprint(w, `{"items":[{"id":"fixture-client","windows":[{"used_percent":20,"limit_window_seconds":18000,"reset_at":2000000000}]}]}`)
	case r.Host == "chatgpt.com":
		fmt.Fprint(w, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000,"reset_at":2000000000}}}`)
	default:
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":"synthetic-private-body"}`)
	}
}

const codexToken = "header.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoic3ludGhldGljLWFjY291bnQifX0.signature"

func parseSelection(args string) (usage.Selection, error) {
	fields := strings.Fields(args)
	selection := usage.Selection{Timeout: 15 * time.Second, Targets: make(map[string]string)}
	if len(fields) == 0 {
		return selection, fmt.Errorf("missing fixture scope")
	}
	selection.Mode = fields[0]
	if strings.HasPrefix(fields[0], "provider:") {
		selection.Mode = "provider"
		selection.ProviderID = strings.TrimPrefix(fields[0], "provider:")
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "target:") {
			selection.Targets["fireworks"] = strings.TrimPrefix(field, "target:")
			continue
		}
		if field == "short" {
			selection.Timeout = 10 * time.Second
			continue
		}
		if field == "companion" {
			selection.UseCodexUsage = true
			continue
		}
		return selection, fmt.Errorf("invalid fixture option")
	}
	return selection, nil
}

func query(call sdk.Context, args string, client usage.Client) error {
	selection, err := parseSelection(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-call.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-joined }()
	dispatcher := usage.Dispatcher{Resolver: usage.Resolver{Host: hostPort{call: call}}, Client: client}
	results, err := dispatcher.Query(ctx, selection)
	reason := ""
	if err != nil {
		reason = "operation-failed"
		switch {
		case errors.Is(err, usage.ErrUsageHost):
			reason = "host-acquisition-failed"
		case errors.Is(err, usage.ErrUsageChanged):
			reason = "identity-changed"
		case errors.Is(err, usage.ErrUsageOrigin):
			reason = "origin-rejected"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timed-out"
		}
	}
	record(map[string]any{"event": "result", "selection": args, "outcomes": results, "error": reason})
	return nil
}

func main() {
	server := httptest.NewTLSServer(http.HandlerFunc(serve))
	defer server.Close()
	addr := server.Listener.Addr().String()
	record(map[string]any{"event": "https", "address": addr})
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || !allowedHost(host) {
			return nil, fmt.Errorf("fixture destination refused")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	defer transport.CloseIdleConnections()
	ext := sdk.New("consumer")
	ext.RegisterCommand("na-query", sdk.CommandOptions{Description: "Disposable production auth query", Handler: func(ctx sdk.Context, args string) error { return query(ctx, args, usage.Client{Transport: transport}) }})
	ext.RegisterCommand("na-model", sdk.CommandOptions{Description: "Disposable selected model", Handler: func(ctx sdk.Context, args string) error {
		ok, err := ctx.SetModel(args)
		if err != nil || !ok {
			return fmt.Errorf("fixture model change failed")
		}
		record(map[string]any{"event": "model", "model": args})
		return nil
	}})
	ext.OnEvent("session_start", func(_ sdk.Context, _ map[string]any) (any, error) {
		record(map[string]any{"event": "ready"})
		return nil, nil
	})
	if err := ext.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "native auth fixture stopped")
		os.Exit(1)
	}
}

func allowedHost(host string) bool {
	switch host {
	case "api.deepseek.com", "api.baseten.co", "openrouter.ai", "api.fireworks.ai", "chatgpt.com", "api.openai.com", "api.x.ai", "api.github.com":
		return true
	}
	return false
}
