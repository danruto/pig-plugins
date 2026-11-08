// Package lifecycle is a disposable conventional factory exercising the staged adapter.
// Its trust root and transport admit only the synthetic loopback HTTPS listener.
// Inputs and receipts belong to a private disposable NA_OUTPUT root, never live data.
package lifecycle

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
	"sync"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	native "github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

var receiptMu sync.Mutex

func record(kind string, detail any) {
	receiptMu.Lock()
	defer receiptMu.Unlock()
	file, err := os.OpenFile(filepath.Join(os.Getenv("NA_OUTPUT"), "lifecycle.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic("fixture receipt unavailable")
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(map[string]any{"time": time.Now().UnixMilli(), "event": kind, "detail": detail}); err != nil {
		panic("fixture receipt unavailable")
	}
}

func hold(ctx context.Context) bool {
	for {
		_, err := os.Stat(filepath.Join(os.Getenv("NA_OUTPUT"), "hold"))
		if os.IsNotExist(err) {
			return false
		}
		if err != nil {
			panic("fixture control unavailable")
		}
		select {
		case <-ctx.Done():
			record("cancelled", true)
			return true
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func serve(w http.ResponseWriter, r *http.Request, held chan<- string) {
	record("request", r.Host)
	if _, err := os.Stat(filepath.Join(os.Getenv("NA_OUTPUT"), "hold")); err == nil {
		record("held", true)
		held <- r.Host
		if hold(r.Context()) {
			return
		}
	} else if !os.IsNotExist(err) {
		panic("fixture control unavailable")
	}
	qualificationResponse(w, r)
	if qualificationEnabled() {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Host == "openrouter.ai" {
		fmt.Fprint(w, `{"data":{"usage":2,"limit":10,"limit_remaining":8,"is_free_tier":false}}`)
		return
	}
	fmt.Fprint(w, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.50","granted_balance":"0","topped_up_balance":"12.50"}]}`)
}

func localTransport(server *httptest.Server) *http.Transport {
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || (host != "api.deepseek.com" && host != "openrouter.ai" && host != "api.fireworks.ai") {
			return nil, errors.New("fixture destination rejected")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	return transport
}

func Extension() *sdk.Extension {
	held := make(chan string, 8)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serve(w, r, held) }))
	record("https", server.Listener.Addr().String())
	transport := localTransport(server)
	ext := sdk.New("lifecycle")
	var clientTransport http.RoundTripper = transport
	if qualificationEnabled() {
		clientTransport = &qualifiedTransport{base: transport}
	}
	runtime, err := native.Register(ext, native.Options{Client: usage.Client{Transport: clientTransport}, Publish: func(view usage.CachedUsage) {
		record("publish", map[string]any{"windows": view.Windows, "reason": view.Reason, "expires": !view.ExpiresAt.IsZero()})
	}})
	if err != nil {
		transport.CloseIdleConnections()
		server.Close()
		panic("fixture registration failed")
	}
	registerControls(ext, runtime, held)
	registerQualification(ext, runtime, clientTransport)
	ext.OnSessionStart(func(_ sdk.Context, _ map[string]any) (any, error) { record("ready", true); return nil, nil })
	ext.OnSessionShutdown(func(_ sdk.Context, _ map[string]any) (any, error) {
		transport.CloseIdleConnections()
		server.Close()
		record("server-closed", true)
		return nil, nil
	})
	return ext
}

func registerControls(ext *sdk.Extension, runtime *native.Runtime, held <-chan string) {
	ext.RegisterCommand("lifecycle-model", sdk.CommandOptions{Handler: func(call sdk.Context, args string) error {
		if args != "deepseek/fixture" && args != "openrouter/fixture" {
			return errors.New("fixture model rejected")
		}
		ok, err := call.SetModel(args)
		if err != nil || !ok {
			return errors.New("fixture model change failed")
		}
		record("model", args)
		return nil
	}})
	ext.RegisterCommand("lifecycle-config", sdk.CommandOptions{Handler: func(_ sdk.Context, _ string) error {
		err := runtime.Configure(usage.CollectionSettings{Targets: map[string]string{"fireworks": "target"}})
		record("configured", err == nil)
		return err
	}})
	ext.RegisterCommand("lifecycle-close", sdk.CommandOptions{Handler: func(call sdk.Context, _ string) error {
		if err := os.WriteFile(filepath.Join(os.Getenv("NA_OUTPUT"), "hold"), nil, 0600); err != nil {
			return errors.New("fixture hold unavailable")
		}
		ok, err := call.SetModel("openrouter/fixture")
		if err != nil || !ok {
			return errors.New("fixture close model change failed")
		}
		if err := awaitHeld(held, "openrouter.ai"); err != nil {
			return err
		}
		record("closing-held", true)
		err = runtime.Close()
		record("joined", err == nil)
		return err
	}})
	ext.RegisterCommand("lifecycle-reload", sdk.CommandOptions{Handler: func(call sdk.Context, _ string) error { return call.Reload() }})
}

func awaitHeld(held <-chan string, host string) error {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-held:
			if got == host {
				return nil
			}
		case <-timer.C:
			return errors.New("fixture held request timeout")
		}
	}
}
