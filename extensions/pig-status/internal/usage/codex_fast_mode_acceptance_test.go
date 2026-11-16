package usage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const (
	acceptFastDirectoryPath = "/backend-api/codex/models"
	acceptFastVersion       = "0.0.0-acceptance"
	acceptFastSession       = "synthetic-session"

	acceptFastSupportedDirectory   = `{"models":[{"slug":"gpt-5.4","service_tiers":[{"id":"priority"}]},{"slug":"gpt-5.5","service_tiers":[{"id":"priority"}]}]}`
	acceptFastUnsupportedDirectory = `{"models":[{"slug":"gpt-5.4","service_tiers":[{"id":"standard"}]},{"slug":"gpt-5.5","service_tiers":[{"id":"standard"}]}]}`
	acceptFastTierlessDirectory    = `{"models":[{"slug":"gpt-5.4"},{"slug":"gpt-5.5"}]}`
	acceptFastOtherModelDirectory  = `{"models":[{"slug":"other-model","service_tiers":[{"id":"priority"}]}]}`
	acceptFastInvalidDirectory     = `{"models":{"synthetic":"object"}}`
	acceptFastTransientBody        = `{"error":"raw-provider-canary account-canary-89342"}`
)

type acceptFastClock struct {
	mu  sync.Mutex
	now time.Time
}

func acceptFastClockStart() *acceptFastClock {
	return &acceptFastClock{now: time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *acceptFastClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *acceptFastClock) Advance(step time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(step)
}

type acceptFastFixture struct {
	store     *SettingsStore
	path      string
	clock     *acceptFastClock
	ui        *acceptUI
	fast      *CodexFast
	requests  atomic.Int32
	refreshes atomic.Int32
}

func acceptFastResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

func acceptFastDirectory(body string) http.RoundTripper {
	return queryRoundTripper(func(*http.Request) (*http.Response, error) {
		return acceptFastResponse(http.StatusOK, body), nil
	})
}

func acceptFastStatus(status int, body string) http.RoundTripper {
	return queryRoundTripper(func(*http.Request) (*http.Response, error) {
		return acceptFastResponse(status, body), nil
	})
}

func acceptFastScope(session, modelID string) FastScope {
	return FastScope{SessionID: session, HasModel: true, Model: CodexModel{Provider: acceptFastProvider, ID: modelID, API: acceptFastAPI, BaseURL: acceptFastBaseURL, Cost: acceptFastCost()}}
}

func acceptFastAccount() CodexAccount {
	return CodexAccount{AccountID: "synthetic-account-id", Headers: http.Header{"Authorization": {"Bearer synthetic-access"}, "ChatGPT-Account-Id": {"synthetic-account-id"}}}
}

func acceptFastNew(t *testing.T, document string, handler http.RoundTripper, tune func(*CodexFastOptions)) *acceptFastFixture {
	t.Helper()
	path := SettingsPath(t.TempDir())
	if document != "" {
		writeSettingsFile(t, path, document)
	}
	store := NewSettingsStore(path)
	_, reloadErr := store.Reload(t.Context())
	acceptNoError(t, reloadErr, "reload fast settings")
	fixture := &acceptFastFixture{store: store, path: path, clock: acceptFastClockStart(), ui: &acceptUI{interactive: true}}
	if handler == nil {
		handler = queryRoundTripper(func(*http.Request) (*http.Response, error) {
			t.Fatalf("unexpected Codex directory request")
			return nil, errors.New("unreachable")
		})
	}
	options := CodexFastOptions{
		Settings: store,
		Client: Client{Transport: queryRoundTripper(func(request *http.Request) (*http.Response, error) {
			fixture.requests.Add(1)
			return handler.RoundTrip(request)
		})},
		Current: func(context.Context) (FastScope, error) {
			return acceptFastScope(acceptFastSession, acceptFastModelID), nil
		},
		Identify:      func(context.Context) (CodexAccount, error) { return acceptFastAccount(), nil },
		Refresh:       func() { fixture.refreshes.Add(1) },
		ClientVersion: acceptFastVersion,
		Timeout:       acceptQueryTimeout,
		Now:           fixture.clock.Now,
	}
	if tune != nil {
		tune(&options)
	}
	fixture.fast = NewCodexFast(options)
	t.Cleanup(fixture.fast.Shutdown)
	return fixture
}

func (f *acceptFastFixture) warmCapability(t *testing.T) FastCapability {
	t.Helper()
	capability, ok := f.fast.RefreshCapabilities(t.Context(), true)
	acceptEqual(t, true, ok, "capability refresh")
	return capability
}

func (f *acceptFastFixture) enablePending(t *testing.T, modelID string) map[string]any {
	t.Helper()
	rewritten, ok := f.fast.BeforeProviderRequest(t.Context(), map[string]any{"model": modelID, "input": []any{}})
	acceptEqual(t, true, ok, "payload rewrite")
	return rewritten
}

func TestFastCommandHandlesArgumentsAndMode(t *testing.T) {
	for _, tt := range []struct {
		name        string
		args        string
		interactive bool
		wantErr     error
		wantLevels  []string
	}{
		{name: "arguments in a terminal", args: "priority", interactive: true, wantLevels: []string{"warning"}},
		{name: "arguments without a UI", args: "priority", interactive: false, wantErr: ErrFastArguments},
		{name: "no arguments without a UI", args: "", interactive: false, wantErr: ErrFastNeedsUI},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptFastNew(t, "", nil, nil)
			fixture.ui.interactive = tt.interactive

			err := fixture.fast.Run(t.Context(), fixture.ui, tt.args)

			acceptErrorIs(t, err, tt.wantErr, "run error")
			acceptEqual(t, tt.wantLevels, fixture.ui.levels, "notice levels")
			acceptNoFile(t, fixture.path, "settings file after a refused /fast")
			acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
		})
	}
}

func TestFastTreatsBlankArgumentsAsNoArguments(t *testing.T) {
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)

	err := fixture.fast.Run(t.Context(), fixture.ui, " \t\n ")

	acceptNoError(t, err, "blank argument run")
	acceptEqual(t, []string{"info"}, fixture.ui.levels, "notice levels")
	acceptEqual(t, true, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
	acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
}

func TestFastReportsIneligibleActiveModel(t *testing.T) {
	for _, tt := range []struct {
		name  string
		scope func(context.Context) (FastScope, error)
	}{
		{
			name:  "no selected model",
			scope: func(context.Context) (FastScope, error) { return FastScope{SessionID: acceptFastSession}, nil },
		},
		{
			name: "other provider",
			scope: func(context.Context) (FastScope, error) {
				return FastScope{SessionID: acceptFastSession, HasModel: true, Model: CodexModel{Provider: "deepseek", ID: "deepseek-chat", API: acceptFastAPI, BaseURL: acceptFastBaseURL}}, nil
			},
		},
		{
			name: "unofficial codex endpoint",
			scope: func(context.Context) (FastScope, error) {
				return FastScope{SessionID: acceptFastSession, HasModel: true, Model: CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://proxy.invalid/backend-api/codex"}}, nil
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptFastNew(t, "", nil, func(options *CodexFastOptions) { options.Current = tt.scope })

			err := fixture.fast.Run(t.Context(), fixture.ui, "")

			acceptNoError(t, err, "ineligible model run")
			acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
			requireSafeText(t, fixture.ui.notices[0])
			acceptNoFile(t, fixture.path, "settings file after an ineligible model")
			acceptEqual(t, false, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
			acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
		})
	}
}

func TestFastRefusesWithoutSupportedCapability(t *testing.T) {
	for _, tt := range []struct {
		name    string
		handler http.RoundTripper
	}{
		{name: "unsupported model", handler: acceptFastDirectory(acceptFastUnsupportedDirectory)},
		{name: "model absent from the directory", handler: acceptFastDirectory(acceptFastOtherModelDirectory)},
		{name: "directory without tier information", handler: acceptFastDirectory(acceptFastTierlessDirectory)},
		{name: "invalid directory", handler: acceptFastDirectory(acceptFastInvalidDirectory)},
		{name: "server failure", handler: acceptFastStatus(http.StatusInternalServerError, acceptFastTransientBody)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptFastNew(t, "", tt.handler, nil)

			err := fixture.fast.Run(t.Context(), fixture.ui, "")

			acceptNoError(t, err, "unsupported capability run")
			acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
			requireSafeText(t, fixture.ui.notices[0])
			acceptNoFile(t, fixture.path, "settings file after an unsupported capability")
			acceptEqual(t, false, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
			acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
		})
	}
}

func TestFastEnablePersistsAndRefreshes(t *testing.T) {
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)

	err := fixture.fast.Run(t.Context(), fixture.ui, "")

	acceptNoError(t, err, "enable flow")
	acceptEqual(t, []string{"info"}, fixture.ui.levels, "notice levels")
	requireSafeText(t, fixture.ui.notices[0])
	acceptEqual(t, true, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
	acceptEqual[any](t, true, decodeSettingsValue(t, readSettingsDocument(t, fixture.path)["codexFastMode"]), "saved fast mode")
	info, statErr := os.Stat(fixture.path)
	acceptNoError(t, statErr, "stat settings file")
	acceptEqual(t, os.FileMode(0o600), info.Mode().Perm(), "settings file mode")
	acceptEqual(t, 1, int(fixture.refreshes.Load()), "status refreshes")
	acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
}

func TestFastDisablePersistsChoice(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)

	err := fixture.fast.Run(t.Context(), fixture.ui, "")

	acceptNoError(t, err, "disable flow")
	acceptEqual(t, []string{"info"}, fixture.ui.levels, "notice levels")
	acceptEqual[any](t, false, decodeSettingsValue(t, readSettingsDocument(t, fixture.path)["codexFastMode"]), "saved fast mode")
	acceptEqual(t, false, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
	acceptEqual(t, 1, int(fixture.refreshes.Load()), "status refreshes")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestFastRefusesInvalidSettingsFile(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": "synthetic-flag"}`, nil, nil)
	before, readErr := os.ReadFile(fixture.path)
	acceptNoError(t, readErr, "read invalid settings")

	err := fixture.fast.Run(t.Context(), fixture.ui, "")

	acceptNoError(t, err, "invalid settings run")
	acceptEqual(t, []string{"error"}, fixture.ui.levels, "notice levels")
	acceptEqual(t, "pi-usage.json is invalid; repair it and run /reload before changing Fast mode.", fixture.ui.notices[0], "refused save notice")
	acceptEqual(t, "invalid", fixture.store.Get().Kind, "store kind")
	after, afterErr := os.ReadFile(fixture.path)
	acceptNoError(t, afterErr, "read invalid settings after refusal")
	acceptEqual(t, string(before), string(after), "invalid settings document")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestFastKeepsSettingWhenTheSaveFails(t *testing.T) {
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)
	acceptNoError(t, os.Chmod(filepath.Dir(fixture.path), os.ModeDir|0o500), "restrict the settings directory")
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Dir(fixture.path), os.ModeDir|0o700)
	})

	enabled := fixture.fast.Toggle(t.Context(), fixture.ui, true)

	acceptEqual(t, false, enabled, "toggle result")
	acceptEqual(t, []string{"error"}, fixture.ui.levels, "notice levels")
	requireSafeText(t, fixture.ui.notices[0])
	acceptEqual(t, false, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
	acceptNoFile(t, fixture.path, "settings file after a failed save")
	acceptEqual(t, 0, int(fixture.refreshes.Load()), "status refreshes")
}

func TestFastCancelledCapabilityCheckSavesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
		options.Identify = func(context.Context) (CodexAccount, error) {
			cancel()
			return acceptFastAccount(), nil
		}
	})

	err := fixture.fast.Run(ctx, fixture.ui, "")

	acceptNoError(t, err, "cancelled capability run")
	acceptEqual(t, 0, len(fixture.ui.notices), "notices")
	acceptNoFile(t, fixture.path, "settings file after a cancelled check")
	acceptEqual(t, false, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
}

func TestFastSessionChangeDuringCapabilityCheckSavesNothing(t *testing.T) {
	var calls atomic.Int32
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
		options.Current = func(context.Context) (FastScope, error) {
			if calls.Add(1) == 1 {
				return acceptFastScope("first-session", acceptFastModelID), nil
			}
			return acceptFastScope("second-session", acceptFastModelID), nil
		}
	})

	err := fixture.fast.Run(t.Context(), fixture.ui, "")

	acceptNoError(t, err, "changed session run")
	acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
	requireSafeText(t, fixture.ui.notices[0])
	acceptNoFile(t, fixture.path, "settings file after a changed session")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestFastToggleAffectsOnlyLaterRequests(t *testing.T) {
	t.Run("enabled request keeps priority and fast cost after a later disable", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
			options.Current = func(context.Context) (FastScope, error) {
				return acceptFastScope(acceptFastSession, acceptFastPremiumID), nil
			}
		})
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")

		rewritten := fixture.enablePending(t, acceptFastPremiumID)

		acceptEqual(t, "priority", rewritten["service_tier"], "request tier")
		acceptEqual(t, true, fixture.fast.Toggle(t.Context(), fixture.ui, false), "disable result")
		corrected, correctedOK := fixture.fast.MessageEnd(t.Context(), acceptFastAssistantMessage(acceptFastProvider, acceptFastPremiumID, acceptFastUsage(1e6, acceptFastZeroCost())))
		acceptEqual(t, true, correctedOK, "cost correction after a later disable")
		acceptFastCorrectedCost(t, acceptFastExpectedCost(2.5), corrected, "corrected cost")
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("standard request keeps its cost after a later enable", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
			options.Current = func(context.Context) (FastScope, error) {
				return acceptFastScope(acceptFastSession, acceptFastPremiumID), nil
			}
		})
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		message := acceptFastAssistantMessage(acceptFastProvider, acceptFastPremiumID, acceptFastUsage(1e6, acceptFastZeroCost()))
		snapshot := acceptFastSnapshot(t, message)

		rewritten := fixture.enablePending(t, acceptFastPremiumID)

		acceptEqual(t, "default", rewritten["service_tier"], "request tier")
		acceptEqual(t, true, fixture.fast.Toggle(t.Context(), fixture.ui, true), "enable result")
		_, correctedOK := fixture.fast.MessageEnd(t.Context(), message)
		acceptEqual(t, false, correctedOK, "cost correction after a later enable")
		acceptEqual[any](t, snapshot, message, "message after a standard request")
		acceptEqual(t, true, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
	})

	t.Run("later request follows the saved toggle", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		standard := fixture.enablePending(t, acceptFastModelID)

		acceptEqual(t, "default", standard["service_tier"], "request tier before the toggle")
		acceptEqual(t, true, fixture.fast.Toggle(t.Context(), fixture.ui, true), "enable result")
		fast := fixture.enablePending(t, acceptFastModelID)

		acceptEqual(t, "priority", fast["service_tier"], "request tier after the toggle")
	})
}

func TestFastMessageEndIgnoresUnmatchedMessages(t *testing.T) {
	t.Run("no pending request", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")

		corrected, ok := fixture.fast.MessageEnd(t.Context(), acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, acceptFastZeroCost())))

		acceptEqual(t, false, ok, "cost correction")
		acceptEqual(t, map[string]any(nil), corrected, "corrected message")
	})

	t.Run("non assistant message", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.enablePending(t, acceptFastModelID)

		corrected, ok := fixture.fast.MessageEnd(t.Context(), map[string]any{"role": "user", "provider": acceptFastProvider, "model": acceptFastModelID})

		acceptEqual(t, false, ok, "cost correction")
		acceptEqual(t, map[string]any(nil), corrected, "corrected message")
	})

	t.Run("non string identifiers", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.enablePending(t, acceptFastModelID)

		corrected, ok := fixture.fast.MessageEnd(t.Context(), map[string]any{"role": "assistant", "provider": acceptFastProvider, "model": float64(5)})

		acceptEqual(t, false, ok, "cost correction")
		acceptEqual(t, map[string]any(nil), corrected, "corrected message")
	})

	t.Run("foreign model keeps the pending request", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.enablePending(t, acceptFastModelID)

		foreign, foreignOK := fixture.fast.MessageEnd(t.Context(), acceptFastAssistantMessage(acceptFastProvider, acceptFastPremiumID, acceptFastUsage(1e6, acceptFastZeroCost())))
		corrected, ok := fixture.fast.MessageEnd(t.Context(), acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, acceptFastZeroCost())))

		acceptEqual(t, false, foreignOK, "foreign message correction")
		acceptEqual(t, map[string]any(nil), foreign, "foreign corrected message")
		acceptEqual(t, true, ok, "matching message correction")
		acceptFastCorrectedCost(t, acceptFastExpectedCost(2), corrected, "corrected cost")
	})

	t.Run("pending request consumed once", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		message := acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, acceptFastZeroCost()))
		fixture.enablePending(t, acceptFastModelID)
		_, firstOK := fixture.fast.MessageEnd(t.Context(), message)

		corrected, ok := fixture.fast.MessageEnd(t.Context(), message)

		acceptEqual(t, true, firstOK, "first message correction")
		acceptEqual(t, false, ok, "repeated message correction")
		acceptEqual(t, map[string]any(nil), corrected, "repeated corrected message")
	})
}

func TestFastPrepareSessionAndShutdownDiscardPendingRequests(t *testing.T) {
	t.Run("prepare session", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
		fixture.enablePending(t, acceptFastModelID)

		fixture.fast.PrepareSession(t.Context(), fixture.ui)

		corrected, ok := fixture.fast.MessageEnd(t.Context(), acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, acceptFastZeroCost())))
		acceptEqual(t, false, ok, "cost correction after a new session")
		acceptEqual(t, map[string]any(nil), corrected, "corrected message after a new session")
		acceptEqual(t, 0, len(fixture.ui.notices), "notices")
	})

	t.Run("shutdown", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
		fixture.enablePending(t, acceptFastModelID)

		fixture.fast.Shutdown()

		corrected, ok := fixture.fast.MessageEnd(t.Context(), acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, acceptFastZeroCost())))
		acceptEqual(t, false, ok, "cost correction after shutdown")
		acceptEqual(t, map[string]any(nil), corrected, "corrected message after shutdown")
		acceptEqual(t, FastUnknown, fixture.fast.Availability(acceptFastModel(acceptFastModelID)).Kind, "availability after shutdown")
	})
}

func TestFastCapabilityCacheReuse(t *testing.T) {
	t.Run("fresh entry is reused", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.clock.Advance(4 * time.Minute)

		capability, ok := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, true, ok, "cached capability")
		acceptEqual(t, FastSupported, capability.Kind, "cached capability kind")
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("entry expires after five minutes", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.clock.Advance(6 * time.Minute)

		capability, ok := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, true, ok, "refreshed capability")
		acceptEqual(t, FastSupported, capability.Kind, "refreshed capability kind")
		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("account change refetches", func(t *testing.T) {
		var rotated atomic.Bool
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
			options.Identify = func(context.Context) (CodexAccount, error) {
				if rotated.Load() {
					return CodexAccount{AccountID: "rotated-account-id", Headers: http.Header{"Authorization": {"Bearer rotated-access"}, "ChatGPT-Account-Id": {"rotated-account-id"}}}, nil
				}
				return acceptFastAccount(), nil
			}
		})
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		rotated.Store(true)

		capability, ok := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, true, ok, "refreshed capability")
		acceptEqual(t, FastSupported, capability.Kind, "refreshed capability kind")
		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests")
	})
}

func TestFastCapabilityFailureBackoff(t *testing.T) {
	t.Run("transient failure keeps the previous models", func(t *testing.T) {
		var calls atomic.Int32
		fixture := acceptFastNew(t, "", queryRoundTripper(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
			}
			return acceptFastResponse(http.StatusInternalServerError, acceptFastTransientBody), nil
		}), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.clock.Advance(6 * time.Minute)

		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, FastSupported, capability.Kind, "capability after a transient failure")
		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("failure backs off for thirty seconds", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastStatus(http.StatusInternalServerError, acceptFastTransientBody), nil)
		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)
		acceptEqual(t, FastUnknown, capability.Kind, "capability after a failed fetch")
		requireSafeText(t, capability.Reason)
		fixture.clock.Advance(10 * time.Second)

		backedOff, _ := fixture.fast.RefreshCapabilities(t.Context(), false)
		acceptEqual(t, FastUnknown, backedOff.Kind, "capability inside the backoff")
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests inside the backoff")
		fixture.clock.Advance(40 * time.Second)
		fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests after the backoff")
	})

	t.Run("client error drops the previous models", func(t *testing.T) {
		var calls atomic.Int32
		fixture := acceptFastNew(t, "", queryRoundTripper(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
			}
			return acceptFastResponse(http.StatusNotFound, acceptFastTransientBody), nil
		}), nil)
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.clock.Advance(6 * time.Minute)

		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, FastUnknown, capability.Kind, "capability after a client error")
		requireSafeText(t, capability.Reason)
		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests")
	})
}

func TestFastDirectoryRequestShape(t *testing.T) {
	var captured *http.Request
	fixture := acceptFastNew(t, "", queryRoundTripper(func(request *http.Request) (*http.Response, error) {
		captured = request
		return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
	}), nil)

	acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")

	acceptEqual(t, http.MethodGet, captured.Method, "directory method")
	acceptEqual(t, "https", captured.URL.Scheme, "directory scheme")
	acceptEqual(t, "chatgpt.com", captured.URL.Host, "directory host")
	acceptEqual(t, acceptFastDirectoryPath, captured.URL.Path, "directory path")
	acceptEqual(t, url.Values{"client_version": {acceptFastVersion}}, captured.URL.Query(), "directory query")
	acceptEqual(t, "Bearer synthetic-access", acceptFastHeaderValue(captured.Header, "Authorization"), "directory authorization")
	acceptEqual(t, "synthetic-account-id", acceptFastHeaderValue(captured.Header, "ChatGPT-Account-Id"), "directory account header")
	acceptEqual(t, []string{"Authorization", "Chatgpt-Account-Id"}, acceptFastHeaderNames(captured.Header), "directory headers")
}

func acceptFastHeaderValue(header http.Header, name string) string {
	for key, values := range header {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func acceptFastHeaderNames(header http.Header) []string {
	names := make([]string, 0, len(header))
	for name := range header {
		canonical := http.CanonicalHeaderKey(name)
		if canonical == "User-Agent" {
			continue
		}
		names = append(names, canonical)
	}
	slices.Sort(names)
	return names
}

func TestFastDirectoryRejectsUnsafeResponses(t *testing.T) {
	t.Run("redirect is refused", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastStatus(http.StatusFound, `{"models":[]}`), nil)

		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)

		acceptEqual(t, FastUnknown, capability.Kind, "capability after a redirect")
		acceptNotEqual(t, "", capability.Reason, "reason after a redirect")
		requireSafeText(t, capability.Reason)
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("oversized body is rejected", func(t *testing.T) {
		oversized := `{"models":[]}` + strings.Repeat(" ", 8<<20)
		fixture := acceptFastNew(t, "", acceptFastStatus(http.StatusOK, oversized), nil)

		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)

		acceptEqual(t, FastUnknown, capability.Kind, "capability after an oversized body")
		acceptNotEqual(t, "", capability.Reason, "reason after an oversized body")
		requireSafeText(t, capability.Reason)
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})
	t.Run("invalid json is rejected", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastStatus(http.StatusOK, `{"models":`), nil)

		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)

		acceptEqual(t, FastUnknown, capability.Kind, "capability after invalid JSON")
		acceptNotEqual(t, "", capability.Reason, "reason after invalid JSON")
		requireSafeText(t, capability.Reason)
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("non object payload is rejected", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastStatus(http.StatusOK, `["synthetic"]`), nil)

		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)

		acceptEqual(t, FastUnknown, capability.Kind, "capability after a non object payload")
		acceptNotEqual(t, "", capability.Reason, "reason after a non object payload")
		requireSafeText(t, capability.Reason)
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})
}

func TestFastAvailabilityReportsCachedCapability(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastUnsupportedDirectory), nil)
	acceptEqual(t, FastUnsupported, fixture.warmCapability(t).Kind, "warm capability")

	available := fixture.fast.Availability(acceptFastModel(acceptFastModelID))
	foreign := fixture.fast.Availability(&CodexModel{Provider: "deepseek", ID: "deepseek-chat", API: acceptFastAPI, BaseURL: acceptFastBaseURL})

	acceptEqual(t, FastUnavailable, available.Kind, "availability for an unsupported model")
	acceptNotEqual(t, "", available.Reason, "availability reason")
	requireSafeText(t, available.Reason)
	acceptEqual(t, FastNotCodex, foreign.Kind, "availability for a foreign model")
}

func TestFastDecorateStatusMarksUnknownCapability(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, nil)

	decorated := fixture.fast.DecorateStatus(acceptFastModel(acceptFastModelID), "codex weekly")

	acceptEqual(t, "codex (Fast unknown) weekly", decorated, "decorated status")
}

func TestFastDecorateStatusLabelsFastRouting(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastSupportedDirectory), nil)
	acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")

	acceptEqual(t, "codex fast weekly", fixture.fast.DecorateStatus(acceptFastModel(acceptFastModelID), "codex weekly"), "decorated status")
}

func TestFastDecorateStatusLeavesDisabledStatus(t *testing.T) {
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)
	acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")

	acceptEqual(t, "codex weekly", fixture.fast.DecorateStatus(acceptFastModel(acceptFastModelID), "codex weekly"), "decorated status")
}

func TestFastPrepareSessionReportsSettingsProblems(t *testing.T) {
	t.Run("invalid document", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": "synthetic-flag"}`, nil, nil)
		before, readErr := os.ReadFile(fixture.path)
		acceptNoError(t, readErr, "read invalid settings")

		fixture.fast.PrepareSession(t.Context(), fixture.ui)

		acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
		requireSafeText(t, fixture.ui.notices[0])
		acceptEqual(t, "invalid", fixture.store.Get().Kind, "store kind")
		acceptEqual(t, 1, int(fixture.refreshes.Load()), "status refreshes")
		after, afterErr := os.ReadFile(fixture.path)
		acceptNoError(t, afterErr, "read invalid settings after a session start")
		acceptEqual(t, string(before), string(after), "invalid settings document")
		acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("invalid document without a UI", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": "synthetic-flag"}`, nil, nil)
		before, readErr := os.ReadFile(fixture.path)
		acceptNoError(t, readErr, "read invalid settings")
		fixture.ui.interactive = false

		fixture.fast.PrepareSession(t.Context(), fixture.ui)

		acceptEqual(t, 0, len(fixture.ui.notices), "notices")
		after, afterErr := os.ReadFile(fixture.path)
		acceptNoError(t, afterErr, "read invalid settings after a session start")
		acceptEqual(t, string(before), string(after), "invalid settings document")
		acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("unreadable document classifies as invalid", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, nil)
		acceptNoError(t, os.Chmod(fixture.path, 0o000), "restrict the settings file")
		t.Cleanup(func() {
			_ = os.Chmod(fixture.path, 0o600)
		})

		fixture.fast.PrepareSession(t.Context(), fixture.ui)

		acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
		requireSafeText(t, fixture.ui.notices[0])
		acceptEqual(t, "invalid", fixture.store.Get().Kind, "store kind")
		acceptEqual(t, 1, int(fixture.refreshes.Load()), "status refreshes")
		acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("missing document", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), nil)

		fixture.fast.PrepareSession(t.Context(), fixture.ui)

		acceptEqual(t, 0, len(fixture.ui.notices), "notices")
		acceptEqual(t, "missing", fixture.store.Get().Kind, "store kind")
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})
}

func TestBeforeProviderRequestLeavesForeignModelsUntouched(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, func(options *CodexFastOptions) {
		options.Current = func(context.Context) (FastScope, error) {
			return FastScope{SessionID: acceptFastSession, HasModel: true, Model: CodexModel{Provider: "deepseek", ID: "deepseek-chat", API: acceptFastAPI, BaseURL: acceptFastBaseURL}}, nil
		}
	})
	payload := map[string]any{"model": "deepseek-chat", "input": []any{}}
	snapshot := acceptFastSnapshot(t, payload)

	rewritten, ok := fixture.fast.BeforeProviderRequest(t.Context(), payload)

	acceptEqual(t, false, ok, "payload rewrite")
	acceptEqual(t, map[string]any(nil), rewritten, "rewritten payload")
	acceptEqual[any](t, snapshot, payload, "payload after the hook")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestBeforeProviderRequestFallsBackWithoutCapability(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, nil)
	payload := map[string]any{"model": acceptFastModelID, "input": []any{}}

	rewritten, ok := fixture.fast.BeforeProviderRequest(t.Context(), payload)

	acceptEqual(t, true, ok, "payload rewrite")
	acceptEqual(t, "default", rewritten["service_tier"], "request tier")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestFastRefreshCapabilitiesSkipsForeignModels(t *testing.T) {
	fixture := acceptFastNew(t, "", nil, func(options *CodexFastOptions) {
		options.Current = func(context.Context) (FastScope, error) {
			return FastScope{SessionID: acceptFastSession, HasModel: true, Model: CodexModel{Provider: "deepseek", ID: "deepseek-chat", API: acceptFastAPI, BaseURL: acceptFastBaseURL}}, nil
		}
	})

	capability, ok := fixture.fast.RefreshCapabilities(t.Context(), true)

	acceptEqual(t, false, ok, "foreign refresh")
	acceptEqual(t, UnknownFastCapability, capability, "foreign capability")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func acceptFastUnreadableScope(options *CodexFastOptions) {
	options.Current = func(context.Context) (FastScope, error) {
		return FastScope{}, errors.New("synthetic session failure")
	}
}

func TestFastReportsUnreadableSession(t *testing.T) {
	fixture := acceptFastNew(t, "", nil, acceptFastUnreadableScope)

	err := fixture.fast.Run(t.Context(), fixture.ui, "")

	acceptNoError(t, err, "unreadable session run")
	acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
	requireSafeText(t, fixture.ui.notices[0])
	acceptNoFile(t, fixture.path, "settings file after an unreadable session")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestFastStopsWhenTheSessionCannotBeReread(t *testing.T) {
	var calls atomic.Int32
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
		options.Current = func(context.Context) (FastScope, error) {
			if calls.Add(1) > 1 {
				return FastScope{}, errors.New("synthetic session failure")
			}
			return acceptFastScope(acceptFastSession, acceptFastModelID), nil
		}
	})

	err := fixture.fast.Run(t.Context(), fixture.ui, "")

	acceptNoError(t, err, "unrereadable session run")
	acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
	requireSafeText(t, fixture.ui.notices[0])
	acceptNoFile(t, fixture.path, "settings file after an unrereadable session")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestFastReportsAccountChangeAfterCapabilityCheck(t *testing.T) {
	var calls atomic.Int32
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
		options.Current = func(context.Context) (FastScope, error) {
			if calls.Add(1) > 2 {
				return acceptFastScope("second-session", acceptFastModelID), nil
			}
			return acceptFastScope(acceptFastSession, acceptFastModelID), nil
		}
	})

	err := fixture.fast.Run(t.Context(), fixture.ui, "")

	acceptNoError(t, err, "changed account run")
	acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
	acceptEqual(t, "The active Codex account changed while checking Fast capability.", fixture.ui.notices[0], "changed account notice")
	acceptNoFile(t, fixture.path, "settings file after a changed account")
	acceptEqual(t, false, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
	acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
}

func TestFastSilencesSuccessWhenSessionChangesAfterSave(t *testing.T) {
	var calls atomic.Int32
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
		options.Current = func(context.Context) (FastScope, error) {
			if calls.Add(1) > 3 {
				return acceptFastScope("second-session", acceptFastModelID), nil
			}
			return acceptFastScope(acceptFastSession, acceptFastModelID), nil
		}
	})

	enabled := fixture.fast.Toggle(t.Context(), fixture.ui, true)

	acceptEqual(t, false, enabled, "toggle result")
	acceptEqual(t, true, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
	acceptEqual(t, 0, len(fixture.ui.notices), "notices")
	acceptEqual(t, 0, int(fixture.refreshes.Load()), "status refreshes")
}

func TestFastReportsUnverifiableAccount(t *testing.T) {
	for _, tt := range []struct {
		name     string
		identify func(context.Context) (CodexAccount, error)
	}{
		{name: "identify failure", identify: func(context.Context) (CodexAccount, error) {
			return CodexAccount{}, errors.New("raw-provider-canary account-canary-89342")
		}},
		{name: "missing account id", identify: func(context.Context) (CodexAccount, error) {
			return CodexAccount{Headers: http.Header{"Authorization": {"Bearer synthetic-access"}}}, nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptFastNew(t, "", nil, func(options *CodexFastOptions) { options.Identify = tt.identify })

			_, ok := fixture.fast.RefreshCapabilities(t.Context(), false)
			available := fixture.fast.Availability(acceptFastModel(acceptFastModelID))

			acceptEqual(t, false, ok, "capability refresh")
			acceptEqual(t, FastUnknown, available.Kind, "availability kind")
			acceptEqual(t, "Codex Fast capability is unknown: the active Codex account could not be verified.", available.Reason, "availability reason")
			requireSafeText(t, available.Reason)
			acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
		})
	}
}

func TestFastCatalogCancellationWhileWaitingForDirectory(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	fixture := acceptFastNew(t, "", queryRoundTripper(func(*http.Request) (*http.Response, error) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
	}), nil)
	first := make(chan struct{})
	go func() {
		defer close(first)
		fixture.fast.RefreshCapabilities(t.Context(), true)
	}()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	<-entered

	_, ok := fixture.fast.RefreshCapabilities(ctx, true)

	close(release)
	<-first
	acceptEqual(t, false, ok, "capability under a cancelled wait")
}

func TestFastDirectoryRejectsStaleCredentials(t *testing.T) {
	t.Run("revalidation before the request", func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
			options.Identify = func(context.Context) (CodexAccount, error) {
				account := acceptFastAccount()
				account.Revalidate = func(context.Context) error {
					return errors.New("synthetic stale credential")
				}
				return account, nil
			}
		})

		_, ok := fixture.fast.RefreshCapabilities(t.Context(), true)

		acceptEqual(t, false, ok, "capability refresh")
		acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("revalidation before storing the result", func(t *testing.T) {
		var revalidations atomic.Int32
		fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
			options.Identify = func(context.Context) (CodexAccount, error) {
				account := acceptFastAccount()
				account.Revalidate = func(context.Context) error {
					if revalidations.Add(1) == 2 {
						return errors.New("synthetic stale credential")
					}
					return nil
				}
				return account, nil
			}
		})

		_, ok := fixture.fast.RefreshCapabilities(t.Context(), true)

		acceptEqual(t, false, ok, "capability refresh")
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")

		second, secondOK := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, true, secondOK, "second capability refresh")
		acceptEqual(t, FastSupported, second.Kind, "second capability kind")
		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests after an unstored result")
	})
}

func TestFastCapabilityFailureKeepsModelsForTransientStatuses(t *testing.T) {
	for _, tt := range []struct {
		name    string
		failure http.RoundTripper
	}{
		{name: "rate limited", failure: acceptFastStatus(http.StatusTooManyRequests, acceptFastTransientBody)},
		{name: "transport failure", failure: queryRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("synthetic transport failure")
		})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			fixture := acceptFastNew(t, "", queryRoundTripper(func(request *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
				}
				return tt.failure.RoundTrip(request)
			}), nil)
			acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
			fixture.clock.Advance(6 * time.Minute)

			capability, _ := fixture.fast.RefreshCapabilities(t.Context(), false)

			acceptEqual(t, FastSupported, capability.Kind, "capability after a transient failure")
			acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests")
		})
	}
}

func TestFastCatalogEvictsOldestAccountEntries(t *testing.T) {
	accounts := make([]CodexAccount, 33)
	for index := range accounts {
		id := fmt.Sprintf("synthetic-account-%02d", index)
		accounts[index] = CodexAccount{AccountID: id, Headers: http.Header{"Authorization": {"Bearer " + id}, "ChatGPT-Account-Id": {id}}}
	}
	var active atomic.Int32
	fixture := acceptFastNew(t, "", acceptFastDirectory(acceptFastSupportedDirectory), func(options *CodexFastOptions) {
		options.Identify = func(context.Context) (CodexAccount, error) {
			return accounts[active.Load()], nil
		}
	})

	for index := range accounts {
		active.Store(int32(index))
		capability, ok := fixture.fast.RefreshCapabilities(t.Context(), false)
		acceptEqual(t, true, ok, "capability refresh for a new account")
		acceptEqual(t, FastSupported, capability.Kind, "capability kind for a new account")
	}
	active.Store(0)

	capability, ok := fixture.fast.RefreshCapabilities(t.Context(), false)

	acceptEqual(t, true, ok, "capability refresh for the oldest account")
	acceptEqual(t, FastSupported, capability.Kind, "capability kind for the oldest account")
	acceptEqual(t, 34, int(fixture.requests.Load()), "directory requests")
}

func TestFastCoalescesConcurrentDirectoryRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var enteredOnce sync.Once
		fixture := acceptFastNew(t, "", queryRoundTripper(func(*http.Request) (*http.Response, error) {
			enteredOnce.Do(func() { close(entered) })
			<-release
			return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
		}), nil)
		results := make(chan FastCapability, 2)

		go func() {
			capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)
			results <- capability
		}()
		<-entered
		go func() {
			capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)
			results <- capability
		}()
		synctest.Wait()
		close(release)

		first := <-results
		second := <-results

		acceptEqual(t, FastSupported, first.Kind, "first capability")
		acceptEqual(t, FastSupported, second.Kind, "second capability")
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests")
	})
}

func TestFastDecorateStatusLeavesStatusWhenNotFast(t *testing.T) {
	t.Run("unknown capability on a foreign status", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, nil)

		decorated := fixture.fast.DecorateStatus(acceptFastModel(acceptFastModelID), "deepseek weekly")

		acceptEqual(t, "deepseek weekly", decorated, "decorated status")
	})

	t.Run("unsupported capability on a codex status", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, acceptFastDirectory(acceptFastUnsupportedDirectory), nil)
		acceptEqual(t, FastUnsupported, fixture.warmCapability(t).Kind, "warm capability")

		decorated := fixture.fast.DecorateStatus(acceptFastModel(acceptFastModelID), "codex weekly")

		acceptEqual(t, "codex weekly", decorated, "decorated status")
	})
}

func TestBeforeProviderRequestIgnoresMissingModel(t *testing.T) {
	fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, func(options *CodexFastOptions) {
		options.Current = func(context.Context) (FastScope, error) {
			return FastScope{SessionID: acceptFastSession}, nil
		}
	})
	payload := map[string]any{"model": acceptFastModelID, "input": []any{}}
	snapshot := acceptFastSnapshot(t, payload)

	rewritten, ok := fixture.fast.BeforeProviderRequest(t.Context(), payload)

	acceptEqual(t, false, ok, "payload rewrite")
	acceptEqual(t, map[string]any(nil), rewritten, "rewritten payload")
	acceptEqual[any](t, snapshot, payload, "payload after the hook")
	acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
}

func TestFastHooksIgnoreUnreadableSession(t *testing.T) {
	t.Run("request hook", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, acceptFastUnreadableScope)

		rewritten, ok := fixture.fast.BeforeProviderRequest(t.Context(), map[string]any{"model": acceptFastModelID})

		acceptEqual(t, false, ok, "payload rewrite")
		acceptEqual(t, map[string]any(nil), rewritten, "rewritten payload")
		acceptEqual(t, 0, int(fixture.requests.Load()), "directory requests")
	})

	t.Run("message hook", func(t *testing.T) {
		fixture := acceptFastNew(t, `{"codexFastMode": true}`, nil, acceptFastUnreadableScope)

		corrected, ok := fixture.fast.MessageEnd(t.Context(), acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, acceptFastZeroCost())))

		acceptEqual(t, false, ok, "cost correction")
		acceptEqual(t, map[string]any(nil), corrected, "corrected message")
	})
}
