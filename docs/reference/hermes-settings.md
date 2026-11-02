<!-- GENERATED FROM CODE — DO NOT EDIT. Run bash scripts/hermes-settings-reference.sh > docs/reference/hermes-settings.md to regenerate. -->

# Native Hermes settings

JSON keys and allowed strings are extracted as struct tags. Fields not assigned in the defaults retain their Go zero value. See the configuration migration guide for validation and path rules.

## Accepted input shape

```go
package config // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"

type Settings struct {
	LazyInitialization             bool     `json:"lazyInitialization"`
	MemoryMode                     string   `json:"memoryMode" values:"policy-only,legacy-inject"`
	MemoryPolicyStyle              string   `json:"memoryPolicyStyle" values:"full,compact,custom,none"`
	MemoryPolicyCustomText         string   `json:"memoryPolicyCustomText"`
	MemoryCharLimit                int      `json:"memoryCharLimit"`
	UserCharLimit                  int      `json:"userCharLimit"`
	ProjectCharLimit               int      `json:"projectCharLimit"`
	NudgeInterval                  int      `json:"nudgeInterval"`
	ReviewRecentMessages           int      `json:"reviewRecentMessages"`
	ReviewEnabled                  bool     `json:"reviewEnabled"`
	ReviewTransport                string   `json:"reviewTransport" values:"direct,subprocess"`
	FlushOnCompact                 bool     `json:"flushOnCompact"`
	FlushOnShutdown                bool     `json:"flushOnShutdown"`
	FlushMinTurns                  int      `json:"flushMinTurns"`
	FlushRecentMessages            int      `json:"flushRecentMessages"`
	FlushCompactTimeoutMS          int      `json:"flushCompactTimeoutMs" signed:"true"`
	MemoryOverflowStrategy         string   `json:"memoryOverflowStrategy" values:"auto-consolidate,reject,fifo-evict"`
	OverflowGraceMS                int      `json:"overflowGraceMs"`
	AutoConsolidate                bool     `json:"autoConsolidate"`
	CorrectionDetection            bool     `json:"correctionDetection"`
	CorrectionStrongPatterns       []string `json:"correctionStrongPatterns"`
	CorrectionWeakPatterns         []string `json:"correctionWeakPatterns"`
	CorrectionNegativePatterns     []string `json:"correctionNegativePatterns"`
	CorrectionDirectiveWords       []string `json:"correctionDirectiveWords"`
	FailureInjectionEnabled        bool     `json:"failureInjectionEnabled"`
	FailureInjectionMaxAgeDays     int      `json:"failureInjectionMaxAgeDays"`
	FailureInjectionMaxEntries     int      `json:"failureInjectionMaxEntries"`
	ConsolidationChunking          bool     `json:"consolidationChunking"`
	ConsolidationChunkChars        int      `json:"consolidationChunkChars"`
	ConsolidationTimeoutMS         int      `json:"consolidationTimeoutMs"`
	UsageHitTrackingEnabled        bool     `json:"usageHitTrackingEnabled"`
	ConsolidationUsageSignals      bool     `json:"consolidationUsageSignals"`
	AutoConsolidationWarnOnFailure bool     `json:"autoConsolidationWarnOnFailure"`
	NudgeToolCalls                 int      `json:"nudgeToolCalls"`
	StandingInstructionsEnabled    bool     `json:"standingInstructionsEnabled"`
	MemoryDir                      string   `json:"memoryDir"`
	ProjectsMemoryDir              string   `json:"projectsMemoryDir"`
	QuickCheckOnOpen               bool     `json:"quickCheckOnOpen"`
	SessionRetentionDays           int      `json:"sessionRetentionDays"`
	LLMModelOverride               string   `json:"llmModelOverride"`
	LLMFallbackModels              []string `json:"llmFallbackModels"`
	LLMThinkingOverride            string   `json:"llmThinkingOverride" values:",off,minimal,low,medium,high,xhigh,max"`
	ChildExtensionPaths            []string `json:"childExtensionPaths"`
	SessionSearch                  struct {
		Variant string `json:"variant" values:"legacy"`
	} `json:"sessionSearch"`
	SessionRoots SessionRoots `json:"sessionRoots"`
}

func Load(path string) (Settings, error)
func Parse(data []byte) (Settings, error)
func (s Settings) EnforcesCapacity() bool
package config // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"

type SessionRoots struct {
	PiG        string `json:"pig"`
	Pi         string `json:"pi"`
	ClaudeCode string `json:"claudeCode"`
}

```

## Defaults

```go
package config // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"

func defaults() Settings {
	s := Settings{
		MemoryMode: "policy-only", MemoryPolicyStyle: "full",
		MemoryCharLimit: 5000, UserCharLimit: 5000, ProjectCharLimit: 5000,
		NudgeInterval: 10, ReviewEnabled: true, ReviewTransport: "direct",
		FlushOnCompact: true, FlushOnShutdown: true, FlushMinTurns: 6,
		FlushCompactTimeoutMS: 60000, MemoryOverflowStrategy: "auto-consolidate",
		OverflowGraceMS: 180000, AutoConsolidate: true, CorrectionDetection: true,
		FailureInjectionEnabled: true, FailureInjectionMaxAgeDays: 7, FailureInjectionMaxEntries: 5,
		ConsolidationChunkChars: 4000, ConsolidationTimeoutMS: 180000,
		UsageHitTrackingEnabled: true, ConsolidationUsageSignals: true,
		AutoConsolidationWarnOnFailure: true, NudgeToolCalls: 15, StandingInstructionsEnabled: true,
		ProjectsMemoryDir: "projects-memory", QuickCheckOnOpen: true,
	}
	s.SessionSearch.Variant = "legacy"
	return s
}
```
