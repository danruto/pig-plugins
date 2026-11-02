// Package config validates the source-led Hermes settings without reading host credentials or settings.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
)

type SessionRoots struct {
	PiG        string `json:"pig"`
	Pi         string `json:"pi"`
	ClaudeCode string `json:"claudeCode"`
}

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

func Load(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Parse([]byte(`{}`))
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read Hermes configuration: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (Settings, error) {
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return Settings{}, fmt.Errorf("decode Hermes configuration: %w", err)
	}
	if fields == nil {
		return Settings{}, errors.New("Hermes configuration must be an object")
	}
	if err := rejectNulls(data); err != nil {
		return Settings{}, err
	}
	if err := validateKeys(data, reflect.TypeFor[Settings]()); err != nil {
		return Settings{}, err
	}
	s := defaults()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return Settings{}, fmt.Errorf("decode Hermes settings: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Settings{}, errors.New("unexpected trailing configuration")
	}
	migrateOverflow(&s, fields)
	if err := validate(s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

func rejectNulls(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if token == nil {
			return errors.New("configuration values must not be null")
		}
	}
}

func validateKeys(data []byte, typ reflect.Type) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return nil
	}
	allowed := make(map[string]reflect.Type, typ.NumField())
	for field := range typ.Fields() {
		allowed[field.Tag.Get("json")] = field.Type
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		fieldType, ok := allowed[key.(string)]
		if !ok {
			return fmt.Errorf("unknown configuration field %q", key)
		}
		var child json.RawMessage
		if err := decoder.Decode(&child); err != nil {
			return err
		}
		if fieldType.Kind() == reflect.Struct {
			if err := validateKeys(child, fieldType); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return nil
}

func migrateOverflow(s *Settings, fields map[string]any) {
	if _, ok := fields["memoryOverflowStrategy"]; ok {
		s.AutoConsolidate = s.MemoryOverflowStrategy == "auto-consolidate"
		return
	}
	if !s.AutoConsolidate {
		s.MemoryOverflowStrategy = "reject"
	}
}

func validate(s Settings) error {
	if err := validateFields(reflect.ValueOf(s)); err != nil {
		return err
	}
	if s.ConsolidationChunkChars < 500 {
		return errors.New("consolidationChunkChars must be at least 500")
	}
	if strings.TrimSpace(s.ProjectsMemoryDir) == "" {
		return errors.New("projectsMemoryDir must not be blank")
	}
	return nil
}

func validateFields(value reflect.Value) error {
	for i := range value.NumField() {
		field := value.Field(i)
		tags := value.Type().Field(i).Tag
		name := tags.Get("json")
		if field.Kind() == reflect.Struct {
			if err := validateFields(field); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		if field.Kind() == reflect.Int && field.Int() < 0 && tags.Get("signed") != "true" {
			return fmt.Errorf("%s must be nonnegative", name)
		}
		if allowed := tags.Get("values"); allowed != "" && !slices.Contains(strings.Split(allowed, ","), field.String()) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}

func (s Settings) EnforcesCapacity() bool { return s.MemoryMode == "legacy-inject" }
