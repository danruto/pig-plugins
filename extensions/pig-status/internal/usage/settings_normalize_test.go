package usage

import (
	"encoding/json"
	"testing"
)

func settingsFields(t *testing.T, document string) map[string]json.RawMessage {
	t.Helper()
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(document), &fields); err != nil {
		t.Fatalf("decode settings document: %v", err)
	}
	return fields
}

func TestNormalizeSettingsAppliesDefaultsAndLegacyTarget(t *testing.T) {
	settings, issue := normalizeSettings(settingsFields(t, `{"fireworksAccountId":"account-1"}`))
	if issue != "" {
		t.Fatalf("legacy document rejected: %s", issue)
	}
	if !settings.CodexStatusResetCountdown || settings.CodexStatusPercentage != "remaining" {
		t.Fatalf("defaults not applied: %+v", settings)
	}
	if got := settings.SelectedTargets["fireworks"]; got != "account-1" {
		t.Fatalf("legacy account not migrated: %q", got)
	}
	explicit, issue := normalizeSettings(settingsFields(t, `{"selectedTargets":{"fireworks":"account-2"},"fireworksAccountId":"account-1"}`))
	if issue != "" {
		t.Fatalf("explicit selection rejected: %s", issue)
	}
	if got := explicit.SelectedTargets["fireworks"]; got != "account-2" {
		t.Fatalf("explicit selection lost to legacy account: %q", got)
	}
}

func TestNormalizeSettingsRejectsMalformedKnownFields(t *testing.T) {
	for _, test := range []struct {
		name     string
		document string
	}{
		{name: "null boolean", document: `{"codexFastMode":null}`},
		{name: "string boolean", document: `{"openaiUseCodexUsage":"true"}`},
		{name: "unknown percentage", document: `{"codexStatusPercentage":"left"}`},
		{name: "targets as array", document: `{"selectedTargets":[]}`},
		{name: "target as number", document: `{"selectedTargets":{"fireworks":1}}`},
		{name: "unsafe provider id", document: `{"selectedTargets":{"bad id":"account-1"}}`},
		{name: "unsafe target id", document: `{"selectedTargets":{"fireworks":"../account"}}`},
		{name: "statusline as string", document: `{"statuslineDisplay":"compact"}`},
		{name: "legacy account as number", document: `{"fireworksAccountId":7}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, issue := normalizeSettings(settingsFields(t, test.document)); issue == "" {
				t.Fatal("malformed field accepted")
			}
		})
	}
}
