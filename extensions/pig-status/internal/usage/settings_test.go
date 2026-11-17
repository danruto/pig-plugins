package usage

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSettingsStoreWithoutAPathUsesDefaultsAndRefusesSaves(t *testing.T) {
	t.Chdir(t.TempDir())
	store := NewSettingsStore("")
	state, err := store.Reload(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if state.Kind != settingsKindMissing || len(state.Settings.SelectedTargets) != 0 {
		t.Fatalf("state = %+v, want defaults without a settings path", state)
	}
	saves := []struct {
		name string
		save func() error
	}{
		{name: "codex companion toggle", save: func() error {
			_, err := store.SetCodexUsage(context.Background(), true)
			return err
		}},
		{name: "codex fast mode", save: func() error {
			_, err := store.SetCodexFast(context.Background(), true)
			return err
		}},
		{name: "billing target", save: func() error {
			_, err := store.SelectTarget(context.Background(), "fireworks", "account-1", nil)
			return err
		}},
	}
	for _, save := range saves {
		t.Run(save.name, func(t *testing.T) {
			if err := save.save(); !errors.Is(err, ErrSettingsUnavailable) {
				t.Fatalf("save error = %v, want %v", err, ErrSettingsUnavailable)
			}
		})
	}
	if _, err := os.Stat(SettingsFileName); !os.IsNotExist(err) {
		t.Fatalf("a save without a settings path wrote %s", SettingsFileName)
	}
}
