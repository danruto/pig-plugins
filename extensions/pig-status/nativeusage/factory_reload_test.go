package nativeusage

import (
	"os"
	"testing"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

func TestASettingsReloadConfiguresTheCollectorWhenTheCollectionChanges(t *testing.T) {
	path := usage.SettingsPath(t.TempDir())
	writeCollectionSettings(t, path, `{"selectedTargets":{"fireworks":"acct-one"}}`)
	store := usage.NewSettingsStore(path)
	initial, err := store.Reload(t.Context())
	if err != nil {
		t.Fatalf("reload the seeded settings: %v", err)
	}
	published := 0
	runtime, err := Register(sdk.New("reload"), Options{Settings: initial.Settings.Collection(), Publish: func(usage.Cached) { published++ }})
	if err != nil {
		t.Fatalf("register the collector: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	configureOnEveryReload(store, runtime)

	writeCollectionSettings(t, path, `{"selectedTargets":{"fireworks":"acct-two"}}`)
	if _, err := store.Reload(t.Context()); err != nil {
		t.Fatalf("reload the edited settings: %v", err)
	}

	if published != 1 {
		t.Fatalf("the collector published %d views after a reload with a changed billing target, want 1", published)
	}

	if _, err := store.Reload(t.Context()); err != nil {
		t.Fatalf("reload the unchanged settings: %v", err)
	}

	if published != 1 {
		t.Fatalf("the collector published %d views after an unchanged reload, want 1: it reconfigured for the settings it already held", published)
	}
}

func configureOnEveryReload(store *usage.SettingsStore, runtime *Runtime) {
	store.OnReload(func(state usage.SettingsState) {
		configure(runtime, sdk.Context{})(state.Settings.Collection())
	})
}

func writeCollectionSettings(t *testing.T, path, document string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
