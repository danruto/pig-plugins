package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOverflowMigrationAndModeAuthority(t *testing.T) {
	cases := []struct {
		input, strategy     string
		automatic, capacity bool
	}{
		{`{}`, "auto-consolidate", true, false},
		{`{"autoConsolidate":false,"memoryMode":"legacy-inject"}`, "reject", false, true},
		{`{"autoConsolidate":true,"memoryOverflowStrategy":"fifo-evict"}`, "fifo-evict", false, false},
		{`{"autoConsolidate":false,"memoryOverflowStrategy":"auto-consolidate"}`, "auto-consolidate", true, false},
		{`{"autoConsolidate":true,"memoryOverflowStrategy":"reject"}`, "reject", false, false},
		{`{"memoryOverflowStrategy":"fifo-evict","autoConsolidate":false,"autoConsolidate":true}`, "fifo-evict", false, false},
		{`{"autoConsolidate":true,"autoConsolidate":false,"memoryOverflowStrategy":"auto-consolidate"}`, "auto-consolidate", true, false},
	}
	for _, test := range cases {
		t.Run(test.input, func(t *testing.T) {
			settings, err := Parse([]byte(test.input))
			if err != nil {
				t.Fatal(err)
			}
			if settings.MemoryOverflowStrategy != test.strategy || settings.AutoConsolidate != test.automatic || settings.EnforcesCapacity() != test.capacity {
				t.Fatalf("strategy=%s auto=%t capacity=%t", settings.MemoryOverflowStrategy, settings.AutoConsolidate, settings.EnforcesCapacity())
			}
		})
	}
}

func TestStrictSettingsValidation(t *testing.T) {
	valid := `{"sessionRetentionDays":0,"flushCompactTimeoutMs":-1,"sessionRoots":{"pig":"/synthetic/pig","pi":"/synthetic/pi","claudeCode":"/synthetic/claude"}}`
	settings, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if settings.SessionRoots.Pi == settings.SessionRoots.PiG || settings.FlushCompactTimeoutMS != -1 {
		t.Fatal("accepted values lost")
	}
	t.Run("canonical keys", func(t *testing.T) {
		canonical, err := Parse([]byte(`{"memoryCharLimit":123,"sessionRoots":{"pig":"p","pi":"i","claudeCode":"c"},"sessionSearch":{"variant":"legacy"},"autoConsolidate":false,"memoryOverflowStrategy":"fifo-evict"}`))
		assertSettingsNoError(t, err)
		assertSettingsEqual(t, []any{canonical.MemoryCharLimit, canonical.SessionRoots, canonical.SessionSearch.Variant, canonical.MemoryOverflowStrategy, canonical.AutoConsolidate}, []any{123, SessionRoots{PiG: "p", Pi: "i", ClaudeCode: "c"}, "legacy", "fifo-evict", false})
	})
	t.Run("canonical duplicate objects", func(t *testing.T) {
		canonical, err := Parse([]byte(`{"sessionRoots":{"pig":"p"},"sessionRoots":{"pi":"i"},"sessionSearch":{"variant":"legacy"},"sessionSearch":{"variant":"legacy"}}`))
		assertSettingsNoError(t, err)
		assertSettingsEqual(t, []string{canonical.SessionRoots.PiG, canonical.SessionRoots.Pi, canonical.SessionSearch.Variant}, []string{"p", "i", "legacy"})
	})
	for _, tt := range []struct{ name, input string }{
		{"NULL-roots earlier duplicate root null", `{"sessionRoots":{"pig":null},"sessionRoots":{"pi":"i"}}`},
		{"NULL-mode earlier duplicate mode null", `{"memoryMode":null,"memoryMode":"policy-only"}`},
		{"NULL-search earlier duplicate search null", `{"sessionSearch":{"variant":null},"sessionSearch":{"variant":"legacy"}}`},
		{"NULL-legacy earlier duplicate flag null", `{"autoConsolidate":null,"autoConsolidate":false}`},
		{"NULL-array earlier duplicate array null element", `{"llmFallbackModels":[null],"llmFallbackModels":["synthetic-model"]}`},
		{"top level scalar casing", `{"MemoryCharLimit":123}`},
		{"top level roots casing", `{"SessionRoots":{"pig":"p"}}`},
		{"nested pig root casing", `{"sessionRoots":{"PiG":"p"}}`},
		{"nested claude root casing", `{"sessionRoots":{"ClaudeCode":"c"}}`},
		{"top level search casing", `{"SessionSearch":{"variant":"legacy"}}`},
		{"nested search variant casing", `{"sessionSearch":{"Variant":"legacy"}}`},
		{"overflow strategy casing with legacy migration", `{"autoConsolidate":false,"MemoryOverflowStrategy":"fifo-evict"}`},
		{"legacy overflow flag casing", `{"AutoConsolidate":false,"memoryOverflowStrategy":"fifo-evict"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.input))
			assertSettingsError(t, err)
		})
	}
	for _, input := range []string{
		`null`, `[]`, `{} {}`, `{"memoryMode":"sqlite-only"}`, `{"memoryCharLimit":-1}`,
		`{"memoryCharLimit":1.5}`, `{"reviewEnabled":"true"}`, `{"memoryMode":null}`,
		`{"consolidationChunkChars":499}`, `{"sessionRetentionDays":-1}`,
		`{"sessionRoots":{"codex":"/unsupported"}}`, `{"sessionSearch":{"variant":"anchors"}}`,
		`{"unknown":true}`, `{"sessionRoots":null}`, `{"sessionRoots":{"pig":null}}`,
		`{"llmModelOverride":["primary"]}`, `{"llmModelFallbacks":["fallback"]}`,
		`{"llmFallbackModels":[1]}`, `{"llmFallbackModels":null}`, `{"reviewTransport":"other"}`,
		`{"memoryPolicyStyle":"other"}`, `{"llmThinkingOverride":"other"}`,
	} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("accepted invalid settings %s", input)
		}
	}
}

func TestLoadSettingsDefaultsAndExplicitInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	missing, err := Load(path)
	assertSettingsNoError(t, err)
	assertSettingsEqual(t, []any{missing.MemoryMode, missing.MemoryPolicyStyle, missing.MemoryCharLimit, missing.UserCharLimit, missing.ProjectCharLimit, missing.ReviewEnabled, missing.FlushOnCompact, missing.FlushOnShutdown, missing.FlushMinTurns, missing.FlushCompactTimeoutMS, missing.ConsolidationChunkChars, missing.ProjectsMemoryDir, missing.SessionSearch.Variant}, []any{"policy-only", "full", 5000, 5000, 5000, true, true, true, 6, 60000, 4000, "projects-memory", "legacy"})
	assertSettingsNoError(t, os.WriteFile(path, []byte(`{"memoryCharLimit":123,"memoryMode":"legacy-inject","reviewEnabled":false,"flushCompactTimeoutMs":0,"consolidationChunkChars":500,"llmModelOverride":"primary","llmFallbackModels":["fallback"],"sessionRoots":{"pig":"p","pi":"i","claudeCode":"c"}}`), 0o600))

	explicit, err := Load(path)
	assertSettingsNoError(t, err)
	assertSettingsEqual(t, []any{explicit.MemoryCharLimit, explicit.EnforcesCapacity(), explicit.ReviewEnabled, explicit.FlushCompactTimeoutMS, explicit.ConsolidationChunkChars, explicit.LLMModelOverride, explicit.LLMFallbackModels, explicit.SessionRoots}, []any{123, true, false, 0, 500, "primary", []string{"fallback"}, SessionRoots{PiG: "p", Pi: "i", ClaudeCode: "c"}})
}

func TestLoadSettingsRefusesMalformedOrUnreadableFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "settings.json")
	assertSettingsNoError(t, os.WriteFile(path, []byte(`{"reviewEnabled":`), 0o600))

	_, malformed := Load(path)
	_, unreadable := Load(root)
	assertSettingsError(t, malformed)
	assertSettingsError(t, unreadable)
}

func assertSettingsNoError(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertSettingsError(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected rejection")
	}
}

func assertSettingsEqual(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}

func ExampleParse() {
	settings, err := Parse([]byte(`{"autoConsolidate":false,"memoryMode":"legacy-inject"}`))
	if err != nil {
		panic(err)
	}
	fmt.Println(settings.MemoryOverflowStrategy, settings.EnforcesCapacity())
	// Output: reject true
}
