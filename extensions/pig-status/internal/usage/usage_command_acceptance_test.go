package usage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const acceptQueryTimeout = 5 * time.Second

const (
	acceptActionRefresh = "Refresh current provider"
	acceptActionAnother = "Another configured provider…"
	acceptActionAll     = "All configured providers"
	acceptActionTarget  = "Select billing target…"
	acceptActionCodex   = "Toggle OpenAI Codex companion usage"
	acceptActionClose   = "Close"
)

type acceptSelection struct {
	choice string
	ok     bool
	err    error
}

type acceptUI struct {
	interactive bool
	script      []acceptSelection
	titles      []string
	optionSets  [][]string
	notices     []string
	levels      []string
}

func (u *acceptUI) HasUI() bool { return u.interactive }

func (u *acceptUI) Select(_ context.Context, title string, options []string) (string, bool, error) {
	index := len(u.titles)
	u.titles = append(u.titles, title)
	u.optionSets = append(u.optionSets, options)
	if index >= len(u.script) {
		return "", false, errors.New("synthetic UI script exhausted")
	}
	step := u.script[index]
	return step.choice, step.ok, step.err
}

func (u *acceptUI) Notify(message, level string) {
	u.notices = append(u.notices, message)
	u.levels = append(u.levels, level)
}

type acceptHostLog struct {
	mu    sync.Mutex
	hosts []string
}

func (l *acceptHostLog) add(host string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hosts = append(l.hosts, host)
}

func (l *acceptHostLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.hosts)
}

type acceptCanaryHost struct {
	*lifecycleHost
}

func (acceptCanaryHost) SessionAuth(context.Context, map[string]any) (map[string]any, error) {
	return nil, errors.New(settingsCanary + " denied")
}

type acceptSelectedModelHost struct {
	*lifecycleHost
}

func (h acceptSelectedModelHost) SessionAuth(ctx context.Context, options map[string]any) (map[string]any, error) {
	base := h.lifecycleHost
	auth, err := base.SessionAuth(ctx, options)
	if err != nil {
		return nil, err
	}
	provider, _ := options["provider"].(string)
	if provider == "" || provider == base.current {
		return auth, nil
	}
	selected, err := base.SessionAuth(ctx, map[string]any{"provider": base.current})
	if err != nil {
		return nil, err
	}
	auth["selectedModel"] = selected["selectedModel"]
	return auth, nil
}

func acceptProviderBase(provider string) string {
	return map[string]string{
		"deepseek":  "https://api.deepseek.com/v1",
		"fireworks": "https://api.fireworks.ai/inference/v1",
	}[provider]
}

func acceptAccountsBody(t *testing.T, slugs ...string) []byte {
	t.Helper()
	accounts := make([]map[string]any, 0, len(slugs))
	for _, slug := range slugs {
		accounts = append(accounts, map[string]any{"name": "accounts/" + slug})
	}
	body, err := json.Marshal(map[string]any{"accounts": accounts})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func acceptSettingsTargets(t *testing.T, path string) map[string]string {
	t.Helper()
	state, err := LoadSettings(t.Context(), path)
	acceptNoError(t, err, "reload settings")
	return state.Settings.SelectedTargets
}

func TestUsageCommandRefusesArguments(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, " refresh ")

	acceptNoError(t, err, "argument refusal")
	acceptEqual(t, []string{"/usage takes no arguments; choose an action from its menu."}, ui.notices, "argument notice")
	acceptEqual(t, []string{"warning"}, ui.levels, "argument notice level")
	acceptEqual(t, 0, len(ui.titles), "menu prompts")
	acceptEqual(t, int32(0), requests.Load(), "provider requests")
}

func TestUsageCommandRequiresInteractiveUI(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: false}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptErrorIs(t, err, ErrUsageNeedsUI, "non-interactive run")
	acceptEqual(t, 0, len(ui.notices), "notices without a UI")
	acceptEqual(t, 0, len(ui.titles), "menu prompts without a UI")
	acceptEqual(t, int32(0), requests.Load(), "provider requests")
}

func TestUsageCommandMenuOptionsForVisibleOutcome(t *testing.T) {
	for _, tt := range []struct {
		name       string
		provider   string
		accounts   []string
		wantTitle  string
		wantOption []string
	}{
		{
			name:      "single fireworks outcome",
			provider:  "fireworks",
			accounts:  []string{"first", "second"},
			wantTitle: "fireworks: selection-required",
			wantOption: []string{
				acceptActionRefresh,
				acceptActionAnother,
				acceptActionAll,
				acceptActionTarget,
				acceptActionCodex,
				acceptActionClose,
			},
		},
		{
			name:      "single deepseek outcome",
			provider:  "deepseek",
			wantTitle: "DeepSeek: ok",
			wantOption: []string{
				acceptActionRefresh,
				acceptActionAnother,
				acceptActionAll,
				acceptActionCodex,
				acceptActionClose,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host := newLifecycleHost(tt.provider, acceptProviderBase(tt.provider))
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.URL.Path == "/v1/accounts" {
					writeQueryFixture(t, w, acceptAccountsBody(t, tt.accounts...))
					return
				}
				writeQueryFixture(t, w, []byte(deepSeekBalance))
			})
			ui := &acceptUI{interactive: true, script: []acceptSelection{{ok: false}}}
			command := UsageCommand{
				Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
				Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
				Timeout:    acceptQueryTimeout,
			}

			err := command.Run(t.Context(), ui, "")

			acceptNoError(t, err, "dismissed menu")
			acceptEqual(t, 1, len(ui.titles), "menu prompts")
			acceptContains(t, ui.titles[0], tt.wantTitle, "menu title")
			acceptEqual(t, tt.wantOption, ui.optionSets[0], "menu options")
			acceptEqual(t, int32(1), requests.Load(), "provider requests")
		})
	}
}

func TestUsageCommandAnotherProviderQueriesOnlyTheChosen(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	host.setCredential("fireworks", "fixture-secret", "https://api.fireworks.ai/inference/v1")
	host.setCredential("openrouter", "fixture-secret", "https://openrouter.ai/api/v1")
	log := new(acceptHostLog)
	client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		log.add(request.Host)
		if request.URL.Path == "/v1/accounts" {
			writeQueryFixture(t, w, acceptAccountsBody(t, "first", "second"))
			return
		}
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionAnother, ok: true},
		{choice: "fireworks", ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: acceptSelectedModelHost{lifecycleHost: host}}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "another-provider flow")
	providerOptions := slices.Clone(ui.optionSets[1])
	slices.Sort(providerOptions)
	acceptEqual(t, []string{"fireworks", "openrouter"}, providerOptions, "offered providers")
	hosts := log.snapshot()
	slices.Sort(hosts)
	acceptEqual(t, []string{"api.deepseek.com", "api.fireworks.ai"}, hosts, "queried origins")
}

func TestUsageCommandAnotherProviderWithoutCandidates(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionAnother, ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "another-provider flow without candidates")
	acceptEqual(t, []string{"No other supported provider has configured runtime auth."}, ui.notices, "missing-candidate notice")
	acceptEqual(t, []string{"info"}, ui.levels, "missing-candidate notice level")
	acceptEqual(t, 2, len(ui.titles), "menu prompts")
	acceptEqual(t, int32(1), requests.Load(), "provider requests")
}

func TestUsageCommandAllProvidersIncludeFailures(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	host.setCredential("fireworks", "fixture-secret", "https://api.fireworks.ai/inference/v1")
	host.setCredential("openrouter", "fixture-secret", "https://openrouter.ai/api/v1")
	client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/accounts" {
			writeQueryFixture(t, w, acceptAccountsBody(t, "first", "second"))
			return
		}
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionAll, ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: acceptSelectedModelHost{lifecycleHost: host}}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "all-provider flow")
	acceptEqual(t, 2, len(ui.titles), "menu prompts")
	acceptContains(t, ui.titles[1], "DeepSeek: ok", "all-provider report")
	acceptContains(t, ui.titles[1], "openrouter: ", "all-provider partial failure")
	acceptContains(t, ui.titles[1], "fireworks: ", "all-provider outcome after a failure")
}

func TestUsageCommandSelectsExactlyTheChosenTarget(t *testing.T) {
	host := newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1")
	client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/accounts" {
			writeQueryFixture(t, w, acceptAccountsBody(t, "first", "second"))
			return
		}
		writeQueryFixture(t, w, billingNamed(t, "fireworks:currencies-series-exact").Payload)
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionTarget, ok: true},
		{choice: "second (second)", ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	settingsPath := SettingsPath(t.TempDir())
	configured := make([]CollectionSettings, 0, 1)
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(settingsPath),
		Timeout:    acceptQueryTimeout,
		Configure:  func(settings CollectionSettings) { configured = append(configured, settings) },
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "target selection flow")
	acceptEqual(t, []string{"first (first)", "second (second)"}, ui.optionSets[1], "target options")
	acceptEqual(t, map[string]string{"fireworks": "second"}, acceptSettingsTargets(t, settingsPath), "saved target")
	acceptEqual(t, []CollectionSettings{{Targets: map[string]string{"fireworks": "second"}}}, configured, "configure callbacks")
}

func TestUsageCommandReportsChangedFireworksAccounts(t *testing.T) {
	host := newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1")
	var discoveries atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/accounts" {
			writeQueryFixture(t, w, billingNamed(t, "fireworks:currencies-series-exact").Payload)
			return
		}
		if discoveries.Add(1) == 1 {
			writeQueryFixture(t, w, acceptAccountsBody(t, "first", "second"))
			return
		}
		writeQueryFixture(t, w, acceptAccountsBody(t, "first", "third"))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionTarget, ok: true},
		{choice: "second (second)", ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	settingsPath := SettingsPath(t.TempDir())
	store := NewSettingsStore(settingsPath)
	configured := make([]CollectionSettings, 0, 1)
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   store,
		Timeout:    acceptQueryTimeout,
		Configure:  func(settings CollectionSettings) { configured = append(configured, settings) },
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "changed-accounts flow")
	acceptEqual(t, []string{"Fireworks accounts changed; choose again."}, ui.notices, "changed-accounts notice")
	acceptEqual(t, []string{"error"}, ui.levels, "changed-accounts notice level")
	acceptNoFile(t, settingsPath, "settings file after refused target")
	acceptEqual(t, "missing", store.Get().Kind, "store kind after refused target")
	acceptEqual(t, 0, len(configured), "configure callbacks")
}

func TestUsageCommandKeepsSettingsWhenSaveFails(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionCodex, ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	settingsPath := SettingsPath(t.TempDir())
	writeSettingsFile(t, settingsPath, `{"codexFastMode": "`+settingsCanary+`"}`)
	store := NewSettingsStore(settingsPath)
	configured := make([]CollectionSettings, 0, 1)
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   store,
		Timeout:    acceptQueryTimeout,
		Configure:  func(settings CollectionSettings) { configured = append(configured, settings) },
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "unrepairable settings flow")
	acceptEqual(t, 2, len(ui.notices), "notices")
	acceptPrefix(t, ui.notices[0], "pi-usage.json is invalid (", "invalid settings warning")
	acceptEqual(t, ErrSettingsInvalid.Error(), ui.notices[1], "refused save notice")
	acceptEqual(t, []string{"warning", "error"}, ui.levels, "notice levels")
	acceptEqual(t, 0, len(configured), "configure callbacks")
	acceptEqual(t, false, store.Get().Settings.OpenAIUseCodexUsage, "codex usage after refused save")
	acceptEqual(t, int32(1), requests.Load(), "provider requests")
}

func TestUsageCommandMenuReportOmitsProviderText(t *testing.T) {
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("failed acquisition reached the provider")
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{{ok: false}}}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: &acceptCanaryHost{lifecycleHost: newLifecycleHost("deepseek", "https://api.deepseek.com/v1")}}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "failed acquisition flow")
	acceptEqual(t, 0, len(ui.notices), "notices")
	acceptEqual(t, 1, len(ui.titles), "menu prompts")
	acceptContains(t, ui.titles[0], "deepseek: host-acquisition-failed", "sanitized failure outcome")
	acceptNotContains(t, ui.titles[0], settingsCanary, "menu title text")
	acceptNotContains(t, ui.titles[0], "denied", "menu title provider text")
}

func TestUsageCommandReportsUnavailableWhenHostAcquisitionFails(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	host.removeCredential("deepseek")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "unavailable flow")
	acceptEqual(t, []string{"Usage unavailable: host-acquisition-failed"}, ui.notices, "unavailable notice")
	acceptEqual(t, []string{"error"}, ui.levels, "unavailable notice level")
	acceptEqual(t, 0, len(ui.titles), "menu prompts")
	acceptEqual(t, int32(0), requests.Load(), "provider requests")
}

func TestFormatOutcomeReportsUnknownForMissingValues(t *testing.T) {
	outcome := Outcome{
		ProviderID: "deepseek",
		Status:     "ok",
		Report: &Report{
			ProviderID: "deepseek",
			Buckets:    []Bucket{{Label: "daily", Unit: "percent"}},
			Metrics:    []Metric{{Label: "quota", Unit: "percent"}},
		},
	}

	lines := FormatOutcome(outcome)

	acceptEqual(t, 3, len(lines), "outcome lines")
	acceptEqual(t, "deepseek: ok", lines[0], "provider line")
	acceptPrefix(t, lines[1], "daily: ", "bucket line label")
	acceptContains(t, lines[1], "unknown", "bucket line unknown")
	acceptNotContains(t, lines[1], "0", "bucket line never zero")
	acceptPrefix(t, lines[2], "quota: ", "metric line label")
	acceptContains(t, lines[2], "unknown", "metric line unknown")
}

func TestFormatOutcomeRendersBucketsAndMetrics(t *testing.T) {
	outcome := Outcome{
		ProviderID: "deepseek",
		Status:     "ok",
		Report: &Report{
			ProviderID: "deepseek",
			Buckets:    []Bucket{{Label: "daily", Used: new(20.0), Limit: new(100.0), Unit: "percent"}},
			Metrics: []Metric{
				{Label: "quota", Value: float64(5), Unit: "percent"},
				{Label: "spend", Value: float64(12), Unit: "currency", Currency: "EUR"},
			},
		},
	}

	lines := FormatOutcome(outcome)

	acceptEqual(t, 4, len(lines), "outcome lines")
	acceptEqual(t, "deepseek: ok", lines[0], "provider line")
	acceptEqual(t, "daily: 20 / 100 percent", lines[1], "bucket line")
	acceptPrefix(t, lines[2], "quota: ", "metric line label")
	acceptContains(t, lines[2], "percent", "metric line unit")
	acceptContains(t, lines[3], "EUR", "metric line currency")
}

func TestFormatOutcomeRendersFailureReason(t *testing.T) {
	outcome := Outcome{ProviderID: "openrouter", Status: "failed", Reason: "query-failed"}

	acceptEqual(t, []string{"openrouter: failed", "openrouter: query-failed"}, FormatOutcome(outcome), "failure lines")
}

func TestFormatOutcomeStripsControlCharacters(t *testing.T) {
	outcome := Outcome{
		ProviderID: "deepseek",
		Status:     "ok",
		Report: &Report{
			ProviderName: "Deep\u001b[31mSeek",
			Buckets:      []Bucket{{Label: "daily\u0007", Used: new(20.0), Limit: new(100.0), Unit: "percent"}},
			Metrics:      []Metric{{Label: "quota\u009b", Value: float64(5), Unit: "percent"}},
		},
	}

	lines := FormatOutcome(outcome)

	acceptEqual(t, "Deep[31mSeek: ok", lines[0], "provider line")
	acceptEqual(t, "daily: 20 / 100 percent", lines[1], "bucket line")
	acceptPrefix(t, lines[2], "quota: ", "metric line label")
	joined := strings.Join(lines, "\n")
	acceptNotContains(t, joined, "\x1b", "escape sequence stripped")
	acceptNotContains(t, joined, "\a", "bell stripped")
	acceptNotContains(t, joined, "\u009b", "C1 control stripped")
}

type acceptCancelUI struct {
	acceptUI
	cancel context.CancelFunc
}

func (u *acceptCancelUI) Select(ctx context.Context, title string, options []string) (string, bool, error) {
	choice, ok, err := u.acceptUI.Select(ctx, title, options)
	u.cancel()
	return choice, ok, err
}

func TestUsageCommandRefreshesCurrentProvider(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionRefresh, ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "refresh flow")
	acceptEqual(t, 2, len(ui.titles), "menu prompts")
	acceptContains(t, ui.titles[1], "DeepSeek: ok", "refreshed report")
	acceptEqual(t, int32(2), requests.Load(), "provider requests")
}

func TestUsageCommandReturnsSelectError(t *testing.T) {
	selectErr := errors.New("synthetic ui failure")
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{{err: selectErr}}}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptErrorIs(t, err, selectErr, "menu select failure")
}

func TestUsageCommandStopsOnCanceledContext(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := command.Run(ctx, ui, "")

	acceptErrorIs(t, err, context.Canceled, "cancelled run")
	acceptEqual(t, 0, len(ui.titles), "menu prompts")
	acceptEqual(t, int32(0), requests.Load(), "provider requests")
}

func TestUsageCommandAnotherProviderDismissedQueriesNothing(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	host.setCredential("fireworks", "fixture-secret", "https://api.fireworks.ai/inference/v1")
	log := new(acceptHostLog)
	client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		log.add(request.Host)
		if request.URL.Path == "/v1/accounts" {
			writeQueryFixture(t, w, acceptAccountsBody(t, "first", "second"))
			return
		}
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionAnother, ok: true},
		{ok: false},
		{choice: acceptActionClose, ok: true},
	}}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: acceptSelectedModelHost{lifecycleHost: host}}, Client: client},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "dismissed provider pick")
	acceptEqual(t, 3, len(ui.titles), "menu prompts")
	hosts := log.snapshot()
	slices.Sort(hosts)
	acceptEqual(t, []string{"api.deepseek.com"}, hosts, "queried origins")
}

func TestUsageCommandIgnoresUnchosenTargetOption(t *testing.T) {
	host := newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1")
	client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/accounts" {
			writeQueryFixture(t, w, acceptAccountsBody(t, "first", "second"))
			return
		}
		writeQueryFixture(t, w, billingNamed(t, "fireworks:currencies-series-exact").Payload)
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionTarget, ok: true},
		{choice: "not a listed choice", ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	settingsPath := SettingsPath(t.TempDir())
	store := NewSettingsStore(settingsPath)
	configured := make([]CollectionSettings, 0, 1)
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   store,
		Timeout:    acceptQueryTimeout,
		Configure:  func(settings CollectionSettings) { configured = append(configured, settings) },
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "unchosen target flow")
	acceptEqual(t, []string{"first (first)", "second (second)"}, ui.optionSets[1], "target options")
	acceptNoFile(t, settingsPath, "settings file after an unchosen target")
	acceptEqual(t, "missing", store.Get().Kind, "store kind after an unchosen target")
	acceptEqual(t, 0, len(configured), "configure callbacks")
}

func TestUsageCommandDiscoversTargetsWhenCurrentReportIsComplete(t *testing.T) {
	host := newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1")
	client := fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/accounts" {
			writeQueryFixture(t, w, acceptAccountsBody(t, "first", "second"))
			return
		}
		writeQueryFixture(t, w, billingNamed(t, "fireworks:currencies-series-exact").Payload)
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionTarget, ok: true},
		{choice: "second (second)", ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	settingsPath := SettingsPath(t.TempDir())
	writeSettingsFile(t, settingsPath, `{"selectedTargets": {"fireworks": "first"}}`)
	configured := make([]CollectionSettings, 0, 1)
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(settingsPath),
		Timeout:    acceptQueryTimeout,
		Configure:  func(settings CollectionSettings) { configured = append(configured, settings) },
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "target discovery flow")
	acceptEqual(t, 0, len(ui.notices), "notices")
	acceptEqual(t, []string{"first (first)", "second (second)"}, ui.optionSets[1], "discovered target options")
	acceptEqual(t, map[string]string{"fireworks": "second"}, acceptSettingsTargets(t, settingsPath), "saved target")
	acceptEqual(t, []CollectionSettings{{Targets: map[string]string{"fireworks": "second"}}}, configured, "configure callbacks")
}

func TestUsageCommandTogglesCodexUsage(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	ui := &acceptUI{interactive: true, script: []acceptSelection{
		{choice: acceptActionCodex, ok: true},
		{choice: acceptActionClose, ok: true},
	}}
	settingsPath := SettingsPath(t.TempDir())
	configured := make([]CollectionSettings, 0, 1)
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   NewSettingsStore(settingsPath),
		Timeout:    acceptQueryTimeout,
		Configure:  func(settings CollectionSettings) { configured = append(configured, settings) },
	}

	err := command.Run(t.Context(), ui, "")

	acceptNoError(t, err, "codex toggle flow")
	acceptEqual(t, 0, len(ui.notices), "notices")
	acceptEqual(t, 2, len(ui.titles), "menu prompts")
	acceptEqual[any](t, true, decodeSettingsValue(t, readSettingsDocument(t, settingsPath)["openaiUseCodexUsage"]), "saved codex usage")
	acceptEqual(t, []CollectionSettings{{Targets: map[string]string{}, UseCodexUsage: true}}, configured, "configure callbacks")
	acceptEqual(t, int32(2), requests.Load(), "provider requests")
}

func TestUsageCommandReportsGenericSaveFailure(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	settingsPath := SettingsPath(t.TempDir())
	store := NewSettingsStore(settingsPath)
	ctx, cancel := context.WithCancel(t.Context())
	ui := &acceptCancelUI{
		acceptUI: acceptUI{interactive: true, script: []acceptSelection{
			{choice: acceptActionCodex, ok: true},
			{choice: acceptActionClose, ok: true},
		}},
		cancel: cancel,
	}
	configured := make([]CollectionSettings, 0, 1)
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: client},
		Settings:   store,
		Timeout:    acceptQueryTimeout,
		Configure:  func(settings CollectionSettings) { configured = append(configured, settings) },
	}

	err := command.Run(ctx, ui, "")

	acceptNoError(t, err, "generic save failure flow")
	acceptEqual(t, []string{"Could not save usage settings."}, ui.notices, "generic save notice")
	acceptEqual(t, []string{"error"}, ui.levels, "generic save notice level")
	acceptEqual(t, 0, len(configured), "configure callbacks")
	acceptNoFile(t, settingsPath, "settings file after a failed save")
	acceptEqual(t, false, store.Get().Settings.OpenAIUseCodexUsage, "codex usage after a failed save")
}
