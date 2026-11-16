package usage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const settingsCanary = "account-canary-89342"

func acceptNoError(t *testing.T, err error, detail string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error %v", detail, err)
	}
}

func acceptErrorIs(t *testing.T, err, want error, detail string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: error = %v, want %v", detail, err, want)
	}
}

func acceptEqual[T any](t *testing.T, want, got T, detail string) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s = %#v, want %#v", detail, got, want)
	}
}

func acceptNotEqual[T any](t *testing.T, unwanted, got T, detail string) {
	t.Helper()
	if reflect.DeepEqual(unwanted, got) {
		t.Fatalf("%s = %#v, want anything else", detail, got)
	}
}

func acceptContains(t *testing.T, got, want string, detail string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", detail, got, want)
	}
}

func acceptNotContains(t *testing.T, got, unwanted string, detail string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Fatalf("%s = %q, want it to omit %q", detail, got, unwanted)
	}
}

func acceptPrefix(t *testing.T, got, prefix string, detail string) {
	t.Helper()
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("%s = %q, want it to start with %q", detail, got, prefix)
	}
}

func acceptNoFile(t *testing.T, path string, detail string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: %s still present (stat error %v)", detail, path, err)
	}
}

func acceptDefaults(t *testing.T, state SettingsState, detail string) {
	t.Helper()
	acceptEqual(t, Settings{CodexStatusResetCountdown: true, CodexStatusPercentage: "remaining", SelectedTargets: map[string]string{}}, state.Settings, detail)
}

func writeSettingsFile(t *testing.T, path, document string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSettingsDocument(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	encoded, err := os.ReadFile(path) //nolint:gosec // the test owns this temporary settings path
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("settings file is not a JSON object: %v", err)
	}
	return document
}

func decodeSettingsValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode settings value: %v", err)
	}
	return value
}

func settingsDirectoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func TestLoadSettingsDefaultsWhenFileMissing(t *testing.T) {
	dir := t.TempDir()

	state, err := LoadSettings(t.Context(), SettingsPath(dir))

	acceptNoError(t, err, "load missing settings")
	acceptEqual(t, "missing", state.Kind, "missing settings kind")
	acceptEqual(t, filepath.Join(dir, "pi-usage.json"), state.Path, "missing settings path")
	acceptEqual(t, "pi-usage.json", SettingsFileName, "settings file name")
	acceptDefaults(t, state, "missing settings defaults")
}

func TestLoadSettingsReadsKnownFields(t *testing.T) {
	path := SettingsPath(t.TempDir())
	writeSettingsFile(t, path, `{
  "openaiUseCodexUsage": true,
  "codexFastMode": true,
  "codexStatusResetCountdown": false,
  "codexStatusPercentage": "used",
  "selectedTargets": {"fireworks": "acct-one", "deepseek": "ds-1"},
  "statuslineDisplay": {"model": true}
}`)

	state, err := LoadSettings(t.Context(), path)

	acceptNoError(t, err, "load settings")
	acceptEqual(t, "loaded", state.Kind, "loaded settings kind")
	acceptEqual(t, Settings{
		OpenAIUseCodexUsage:       true,
		CodexFastMode:             true,
		CodexStatusResetCountdown: false,
		CodexStatusPercentage:     "used",
		SelectedTargets:           map[string]string{"fireworks": "acct-one", "deepseek": "ds-1"},
	}, state.Settings, "loaded settings")
}

func TestSettingsSaveKeepsUnknownFieldsAndDisplay(t *testing.T) {
	path := SettingsPath(t.TempDir())
	writeSettingsFile(t, path, `{
  "codexFastMode": true,
  "statuslineDisplay": {"model": true, "cwd": false},
  "vendorExtension": {"nested": [1, 2, 3]}
}`)
	store := NewSettingsStore(path)

	state, err := store.SetCodexUsage(t.Context(), true)

	acceptNoError(t, err, "save codex usage")
	acceptEqual(t, true, state.Settings.OpenAIUseCodexUsage, "saved codex usage")
	document := readSettingsDocument(t, path)
	acceptEqual[any](t, map[string]any{"nested": []any{1.0, 2.0, 3.0}}, decodeSettingsValue(t, document["vendorExtension"]), "unknown field after save")
	acceptEqual[any](t, map[string]any{"model": true, "cwd": false}, decodeSettingsValue(t, document["statuslineDisplay"]), "statusline display after save")
	info, err := os.Stat(path)
	acceptNoError(t, err, "stat settings file")
	acceptEqual(t, os.FileMode(0o600), info.Mode().Perm(), "settings file mode")
}

func TestSettingsSaveCreatesPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	path := SettingsPath(dir)

	_, err := NewSettingsStore(path).SetCodexUsage(t.Context(), true)

	acceptNoError(t, err, "save into a missing directory")
	info, err := os.Stat(dir)
	acceptNoError(t, err, "stat settings directory")
	acceptEqual(t, os.FileMode(0o700), info.Mode().Perm(), "settings directory mode")
	file, err := os.Stat(path)
	acceptNoError(t, err, "stat settings file")
	acceptEqual(t, os.FileMode(0o600), file.Mode().Perm(), "settings file mode")
}

func TestLoadSettingsMigratesLegacyFireworksAccount(t *testing.T) {
	for _, tt := range []struct {
		name     string
		document string
		want     map[string]string
	}{
		{
			name:     "without a selected target",
			document: `{"fireworksAccountId": "legacy-account"}`,
			want:     map[string]string{"fireworks": "legacy-account"},
		},
		{
			name:     "with an explicit target",
			document: `{"fireworksAccountId": "legacy-account", "selectedTargets": {"fireworks": "chosen-account"}}`,
			want:     map[string]string{"fireworks": "chosen-account"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := SettingsPath(t.TempDir())
			writeSettingsFile(t, path, tt.document)

			state, err := LoadSettings(t.Context(), path)

			acceptNoError(t, err, "load legacy settings")
			acceptEqual(t, tt.want, state.Settings.SelectedTargets, "legacy targets")
		})
	}
}

func TestSelectTargetDropsLegacyFieldOnlyForFireworks(t *testing.T) {
	for _, tt := range []struct {
		name        string
		providerID  string
		targetID    string
		wantTargets map[string]string
		wantLegacy  bool
	}{
		{
			name:        "fireworks target",
			providerID:  "fireworks",
			targetID:    "acct-2",
			wantTargets: map[string]string{"fireworks": "acct-2"},
			wantLegacy:  false,
		},
		{
			name:        "other provider target",
			providerID:  "deepseek",
			targetID:    "ds-2",
			wantTargets: map[string]string{"deepseek": "ds-2", "fireworks": "legacy-account"},
			wantLegacy:  true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := SettingsPath(t.TempDir())
			writeSettingsFile(t, path, `{"fireworksAccountId": "legacy-account"}`)
			store := NewSettingsStore(path)

			_, err := store.SelectTarget(t.Context(), tt.providerID, tt.targetID, nil)

			acceptNoError(t, err, "select target")
			reloaded, err := LoadSettings(t.Context(), path)
			acceptNoError(t, err, "reload after selection")
			acceptEqual(t, "loaded", reloaded.Kind, "kind after selection")
			acceptEqual(t, tt.wantTargets, reloaded.Settings.SelectedTargets, "targets after selection")
			_, hasLegacy := readSettingsDocument(t, path)["fireworksAccountId"]
			acceptEqual(t, tt.wantLegacy, hasLegacy, "legacy field retained")
		})
	}
}

func TestLoadSettingsRejectsInvalidDocuments(t *testing.T) {
	for _, tt := range []struct {
		name     string
		document string
	}{
		{name: "broken json", document: `{"codexFastMode": true, "broken": "` + settingsCanary},
		{name: "not an object", document: `["` + settingsCanary + `"]`},
		{name: "wrong boolean type", document: `{"codexFastMode": "` + settingsCanary + `"}`},
		{name: "unknown percentage", document: `{"codexStatusPercentage": "` + settingsCanary + `"}`},
		{name: "unsafe provider id", document: `{"selectedTargets": {"../` + settingsCanary + `": "acct-one"}}`},
		{name: "unsafe target id", document: `{"selectedTargets": {"fireworks": "../` + settingsCanary + `"}}`},
		{name: "unsafe legacy account id", document: `{"fireworksAccountId": "../` + settingsCanary + `"}`},
		{name: "display is not an object", document: `{"statuslineDisplay": "` + settingsCanary + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := SettingsPath(t.TempDir())
			writeSettingsFile(t, path, tt.document)

			state, err := LoadSettings(t.Context(), path)

			acceptNoError(t, err, "load invalid settings")
			acceptEqual(t, "invalid", state.Kind, "invalid settings kind")
			acceptNotEqual(t, "", state.Issue, "invalid settings issue")
			acceptNotContains(t, state.Issue, settingsCanary, "invalid settings issue text")
			acceptDefaults(t, state, "invalid settings defaults")
		})
	}
}

func TestLoadSettingsRefusesSymbolicLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.json")
	writeSettingsFile(t, target, `{"codexFastMode": true}`)
	path := SettingsPath(t.TempDir())
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	state, err := LoadSettings(t.Context(), path)

	acceptNoError(t, err, "load symlinked settings")
	acceptEqual(t, "invalid", state.Kind, "symlinked settings kind")
	acceptEqual(t, "symbolic links are not accepted", state.Issue, "symlinked settings issue")
}

func TestLoadSettingsRefusesOversizedFile(t *testing.T) {
	path := SettingsPath(t.TempDir())
	writeSettingsFile(t, path, `{"pad": "`+strings.Repeat("a", 70*1024)+settingsCanary+`"}`)

	state, err := LoadSettings(t.Context(), path)

	acceptNoError(t, err, "load oversized settings")
	acceptEqual(t, "invalid", state.Kind, "oversized settings kind")
	acceptNotEqual(t, "", state.Issue, "oversized settings issue")
	acceptNotContains(t, state.Issue, settingsCanary, "oversized settings issue text")
}

func TestLoadSettingsRefusesNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := SettingsPath(dir)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	state, err := LoadSettings(t.Context(), path)

	acceptNoError(t, err, "load non-regular settings")
	acceptEqual(t, "invalid", state.Kind, "non-regular settings kind")
	acceptNotEqual(t, "", state.Issue, "non-regular settings issue")
}

func TestSettingsSaveRefusesInvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := SettingsPath(dir)
	before := `{"codexFastMode": "` + settingsCanary + `"}`
	writeSettingsFile(t, path, before)
	store := NewSettingsStore(path)

	_, err := store.SetCodexUsage(t.Context(), true)

	acceptErrorIs(t, err, ErrSettingsInvalid, "save over an invalid file")
	after, readErr := os.ReadFile(path) //nolint:gosec // the test owns this temporary settings path
	acceptNoError(t, readErr, "read invalid settings after save")
	acceptEqual(t, before, string(after), "invalid settings content after save")
	acceptEqual(t, []string{"pi-usage.json"}, settingsDirectoryNames(t, dir), "settings directory after refused save")
}

func TestSelectTargetRemovesNewFileWhenConfirmationFails(t *testing.T) {
	path := SettingsPath(t.TempDir())
	store := NewSettingsStore(path)
	confirmationErr := errors.New("synthetic confirmation refused")

	_, err := store.SelectTarget(t.Context(), "fireworks", "acct-1", func(context.Context) error { return confirmationErr })

	acceptErrorIs(t, err, confirmationErr, "refused confirmation")
	acceptNoFile(t, path, "settings file after rollback")
	acceptEqual(t, "missing", store.Get().Kind, "store kind after rollback")
}

func TestSelectTargetRestoresPreviousDocumentWhenConfirmationFails(t *testing.T) {
	path := SettingsPath(t.TempDir())
	writeSettingsFile(t, path, `{"codexStatusPercentage": "used", "selectedTargets": {"deepseek": "ds-1"}}`)
	store := NewSettingsStore(path)
	confirmationErr := errors.New("synthetic confirmation refused")

	_, err := store.SelectTarget(t.Context(), "fireworks", "acct-1", func(context.Context) error { return confirmationErr })

	acceptErrorIs(t, err, confirmationErr, "refused confirmation")
	reloaded, loadErr := LoadSettings(t.Context(), path)
	acceptNoError(t, loadErr, "reload after rollback")
	acceptEqual(t, "loaded", reloaded.Kind, "kind after rollback")
	acceptEqual(t, map[string]string{"deepseek": "ds-1"}, reloaded.Settings.SelectedTargets, "targets after rollback")
	acceptEqual(t, "used", reloaded.Settings.CodexStatusPercentage, "percentage after rollback")
	acceptEqual(t, map[string]string{"deepseek": "ds-1"}, store.Get().Settings.SelectedTargets, "store targets after rollback")
}

func TestSettingsSaveStopsOnCanceledContext(t *testing.T) {
	path := SettingsPath(t.TempDir())
	store := NewSettingsStore(path)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.SetCodexUsage(ctx, true)

	acceptErrorIs(t, err, context.Canceled, "cancelled save")
	acceptNoFile(t, path, "settings file after a cancelled save")
}

func TestSelectTargetRejectsMalformedIdentifiers(t *testing.T) {
	for _, tt := range []struct {
		name       string
		providerID string
		targetID   string
	}{
		{name: "empty provider", providerID: "", targetID: "acct-1"},
		{name: "empty target", providerID: "fireworks", targetID: ""},
		{name: "traversal provider", providerID: "../fireworks", targetID: "acct-1"},
		{name: "traversal target", providerID: "fireworks", targetID: "../acct-1"},
		{name: "oversized target", providerID: "fireworks", targetID: strings.Repeat("a", 129)},
		{name: "leading punctuation target", providerID: "fireworks", targetID: "-acct-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := SettingsPath(t.TempDir())
			store := NewSettingsStore(path)

			_, err := store.SelectTarget(t.Context(), tt.providerID, tt.targetID, nil)

			acceptErrorIs(t, err, ErrInvalidTarget, "malformed target selection")
			acceptNoFile(t, path, "settings file after malformed selection")
		})
	}
}

func TestSettingsReloadStopsOnCanceledContext(t *testing.T) {
	store := NewSettingsStore(SettingsPath(t.TempDir()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.Reload(ctx)

	acceptErrorIs(t, err, context.Canceled, "cancelled reload")
	acceptEqual(t, "missing", store.Get().Kind, "store kind after a cancelled reload")
}

func TestSettingsStoreGetReturnsCopy(t *testing.T) {
	path := SettingsPath(t.TempDir())
	writeSettingsFile(t, path, `{"selectedTargets": {"deepseek": "ds-1"}}`)
	store := NewSettingsStore(path)
	_, err := store.Reload(t.Context())
	acceptNoError(t, err, "reload settings")
	state := store.Get()

	state.Settings.SelectedTargets["deepseek"] = "mutated"
	state.Settings.SelectedTargets["fireworks"] = "mutated"

	acceptEqual(t, map[string]string{"deepseek": "ds-1"}, store.Get().Settings.SelectedTargets, "targets after mutating a copy")
}

func TestSettingsCollectionMirrorsTargetsAndCodexUsage(t *testing.T) {
	settings := Settings{OpenAIUseCodexUsage: true, SelectedTargets: map[string]string{"fireworks": "acct-one"}}
	collection := settings.Collection()
	acceptEqual(t, CollectionSettings{Targets: map[string]string{"fireworks": "acct-one"}, UseCodexUsage: true}, collection, "collection settings")

	collection.Targets["fireworks"] = "mutated"

	acceptEqual(t, CollectionSettings{Targets: map[string]string{"fireworks": "acct-one"}, UseCodexUsage: true}, settings.Collection(), "collection settings after mutating a copy")
}

func TestSettingsSetCodexUsageDisables(t *testing.T) {
	path := SettingsPath(t.TempDir())
	writeSettingsFile(t, path, `{"openaiUseCodexUsage": true}`)
	store := NewSettingsStore(path)

	state, err := store.SetCodexUsage(t.Context(), false)

	acceptNoError(t, err, "disable codex usage")
	acceptEqual(t, false, state.Settings.OpenAIUseCodexUsage, "stored codex usage")
	acceptEqual[any](t, false, decodeSettingsValue(t, readSettingsDocument(t, path)["openaiUseCodexUsage"]), "saved codex usage")
}
