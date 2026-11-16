package usage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"syscall"
)

const SettingsFileName = "pi-usage.json"

const maxSettingsBytes = 64 * 1024

const (
	settingsKindMissing = "missing"
	settingsKindLoaded  = "loaded"
	settingsKindInvalid = "invalid"
)

var usageIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

var (
	ErrSettingsInvalid = errors.New("pi-usage.json is invalid; repair it before saving")
	ErrSettingsChanged = errors.New("pi-usage.json changed while saving; retry")
	ErrInvalidTarget   = errors.New("invalid usage target selection")
)

type Settings struct {
	OpenAIUseCodexUsage       bool
	CodexFastMode             bool
	CodexStatusResetCountdown bool
	CodexStatusPercentage     string
	SelectedTargets           map[string]string
}

type SettingsState struct {
	Kind     string
	Path     string
	Settings Settings
	Issue    string
}

type SettingsStore struct {
	mu    sync.Mutex
	path  string
	state SettingsState
}

func SettingsPath(agentDir string) string {
	return filepath.Join(agentDir, SettingsFileName)
}

func DefaultSettings() Settings {
	return Settings{
		CodexStatusResetCountdown: true,
		CodexStatusPercentage:     "remaining",
		SelectedTargets:           map[string]string{},
	}
}

func (s Settings) Collection() CollectionSettings {
	return CollectionSettings{Targets: maps.Clone(s.SelectedTargets), UseCodexUsage: s.OpenAIUseCodexUsage}
}

func LoadSettings(ctx context.Context, path string) (SettingsState, error) {
	document, err := readSettings(ctx, path)
	return document.state(path), err
}

func NewSettingsStore(path string) *SettingsStore {
	return &SettingsStore{
		path:  path,
		state: SettingsState{Kind: settingsKindMissing, Path: path, Settings: DefaultSettings()},
	}
}

func (s *SettingsStore) Get() SettingsState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettingsState(s.state)
}

func (s *SettingsStore) Reload(ctx context.Context) (SettingsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := readSettings(ctx, s.path)
	if err != nil {
		return cloneSettingsState(s.state), err
	}
	s.state = document.state(s.path)
	return cloneSettingsState(s.state), nil
}

//nolint:revive // the boolean is the value being stored, not a branch selector
func (s *SettingsStore) SetCodexUsage(ctx context.Context, enabled bool) (SettingsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.loadForSave(ctx)
	if err != nil {
		return cloneSettingsState(s.state), err
	}
	next := previous.clone()
	next.fields["openaiUseCodexUsage"] = json.RawMessage("false")
	if enabled {
		next.fields["openaiUseCodexUsage"] = json.RawMessage("true")
	}
	if _, err := s.persist(ctx, previous, next); err != nil {
		return cloneSettingsState(s.state), err
	}
	return cloneSettingsState(s.state), nil
}

func (s *SettingsStore) SetCodexFast(ctx context.Context, enabled bool) (SettingsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.loadForSave(ctx)
	if err != nil {
		return cloneSettingsState(s.state), err
	}
	next := previous.clone()
	next.fields["codexFastMode"] = json.RawMessage(strconv.FormatBool(enabled))
	if _, err := s.persist(ctx, previous, next); err != nil {
		return cloneSettingsState(s.state), err
	}
	return cloneSettingsState(s.state), nil
}

func (s *SettingsStore) SelectTarget(ctx context.Context, providerID, targetID string, confirm func(context.Context) error) (SettingsState, error) {
	if !usageIDPattern.MatchString(providerID) || !usageIDPattern.MatchString(targetID) {
		return s.Get(), ErrInvalidTarget
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.loadForSave(ctx)
	if err != nil {
		return cloneSettingsState(s.state), err
	}
	selected, err := selectionField(previous.state(s.path).Settings.SelectedTargets, providerID, targetID)
	if err != nil {
		return cloneSettingsState(s.state), err
	}
	next := previous.clone()
	next.fields["selectedTargets"] = selected
	if providerID == "fireworks" {
		delete(next.fields, "fireworksAccountId")
	}
	saved, err := s.persist(ctx, previous, next)
	if err != nil {
		return cloneSettingsState(s.state), err
	}
	if confirm == nil {
		return cloneSettingsState(s.state), nil
	}
	confirmErr := confirmed(ctx, confirm)
	if confirmErr == nil {
		return cloneSettingsState(s.state), nil
	}
	rollbackErr := s.restore(context.WithoutCancel(ctx), previous, saved)
	return cloneSettingsState(s.state), errors.Join(confirmErr, rollbackErr)
}

func confirmed(ctx context.Context, confirm func(context.Context) error) error {
	if err := confirm(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *SettingsStore) loadForSave(ctx context.Context) (settingsDocument, error) {
	document, err := readSettings(ctx, s.path)
	if err != nil {
		return document, err
	}
	if document.kind == settingsKindInvalid {
		return document, ErrSettingsInvalid
	}
	return document, nil
}

func (s *SettingsStore) persist(ctx context.Context, previous, next settingsDocument) (settingsDocument, error) {
	next.kind = settingsKindLoaded
	state := next.state(s.path)
	if state.Kind != settingsKindLoaded {
		return previous, ErrSettingsInvalid
	}
	if err := writeSettings(ctx, s.path, previous, next); err != nil {
		return previous, err
	}
	s.state = state
	return next, nil
}

func (s *SettingsStore) restore(ctx context.Context, previous, saved settingsDocument) error {
	if previous.kind == settingsKindMissing {
		current, err := readSettings(ctx, s.path)
		if err != nil {
			return err
		}
		same, err := sameSettingsDocument(current, saved)
		if err != nil {
			return err
		}
		if !same {
			return ErrSettingsChanged
		}
		if err := os.Remove(s.path); err != nil {
			return fmt.Errorf("remove usage settings: %w", err)
		}
		s.state = previous.state(s.path)
		return nil
	}
	if err := writeSettings(ctx, s.path, saved, previous); err != nil {
		return err
	}
	s.state = previous.state(s.path)
	return nil
}

func cloneSettingsState(state SettingsState) SettingsState {
	state.Settings.SelectedTargets = maps.Clone(state.Settings.SelectedTargets)
	if state.Settings.SelectedTargets == nil {
		state.Settings.SelectedTargets = map[string]string{}
	}
	return state
}

type settingsDocument struct {
	kind   string
	fields map[string]json.RawMessage
	issue  string
}

func (d settingsDocument) clone() settingsDocument {
	return settingsDocument{kind: d.kind, fields: maps.Clone(d.fields), issue: d.issue}
}

func (d settingsDocument) state(path string) SettingsState {
	switch d.kind {
	case settingsKindLoaded:
		settings, issue := normalizeSettings(d.fields)
		if issue != "" {
			return SettingsState{Kind: settingsKindInvalid, Path: path, Settings: DefaultSettings(), Issue: issue}
		}
		return SettingsState{Kind: settingsKindLoaded, Path: path, Settings: settings}
	case settingsKindInvalid:
		return SettingsState{Kind: settingsKindInvalid, Path: path, Settings: DefaultSettings(), Issue: d.issue}
	default:
		return SettingsState{Kind: settingsKindMissing, Path: path, Settings: DefaultSettings()}
	}
}

func invalidSettingsDocument(issue string) settingsDocument {
	return settingsDocument{kind: settingsKindInvalid, issue: issue}
}

func readSettings(ctx context.Context, path string) (settingsDocument, error) {
	empty := settingsDocument{kind: settingsKindMissing, fields: map[string]json.RawMessage{}}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // the agent directory supplies the settings path
	switch {
	case errors.Is(err, os.ErrNotExist):
		return empty, nil
	case errors.Is(err, syscall.ELOOP):
		return invalidSettingsDocument("symbolic links are not accepted"), nil
	case err != nil:
		return invalidSettingsDocument("settings file is not readable"), nil
	}
	defer func() { _ = file.Close() }()
	return readSettingsBody(ctx, file)
}

//nolint:nilerr // an unreadable or malformed file is an invalid settings state, not a failure
func readSettingsBody(ctx context.Context, file *os.File) (settingsDocument, error) {
	info, err := file.Stat()
	if err != nil {
		return invalidSettingsDocument("settings file is not readable"), nil
	}
	if !info.Mode().IsRegular() {
		return invalidSettingsDocument("settings path is not a regular file"), nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return invalidSettingsDocument("settings file is not readable"), nil
	}
	if len(data) > maxSettingsBytes {
		return invalidSettingsDocument("settings file exceeds 64 KiB"), nil
	}
	if err := ctx.Err(); err != nil {
		return settingsDocument{}, err
	}
	return parseSettingsDocument(data), nil
}

func parseSettingsDocument(data []byte) settingsDocument {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return invalidSettingsDocument("settings file must contain a JSON object")
	}
	if _, issue := normalizeSettings(fields); issue != "" {
		return invalidSettingsDocument(issue)
	}
	return settingsDocument{kind: settingsKindLoaded, fields: fields}
}

func normalizeSettings(fields map[string]json.RawMessage) (Settings, string) {
	settings := DefaultSettings()
	for _, entry := range []struct {
		key    string
		target *bool
	}{
		{key: "openaiUseCodexUsage", target: &settings.OpenAIUseCodexUsage},
		{key: "codexFastMode", target: &settings.CodexFastMode},
		{key: "codexStatusResetCountdown", target: &settings.CodexStatusResetCountdown},
	} {
		value, present, issue := settingsBoolean(fields, entry.key)
		if issue != "" {
			return DefaultSettings(), issue
		}
		if present {
			*entry.target = value
		}
	}
	percentage, present, issue := settingsChoice(fields, "codexStatusPercentage", `codexStatusPercentage must be "remaining" or "used"`, "remaining", "used")
	if issue != "" {
		return DefaultSettings(), issue
	}
	if present {
		settings.CodexStatusPercentage = percentage
	}
	targets, issue := settingsTargets(fields)
	if issue != "" {
		return DefaultSettings(), issue
	}
	settings.SelectedTargets = targets
	if issue := settingsStatusline(fields); issue != "" {
		return DefaultSettings(), issue
	}
	if issue := migrateLegacyTarget(fields, &settings); issue != "" {
		return DefaultSettings(), issue
	}
	return settings, ""
}

func settingsBoolean(fields map[string]json.RawMessage, key string) (bool, bool, string) {
	raw, present := fields[key]
	if !present {
		return false, false, ""
	}
	var value *bool
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return false, true, key + " must be a boolean"
	}
	return *value, true, ""
}

func settingsChoice(fields map[string]json.RawMessage, key, issue string, allowed ...string) (string, bool, string) {
	raw, present := fields[key]
	if !present {
		return "", false, ""
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil || !slices.Contains(allowed, *value) {
		return "", true, issue
	}
	return *value, true, ""
}

func settingsTargets(fields map[string]json.RawMessage) (map[string]string, string) {
	targets := map[string]string{}
	raw, present := fields["selectedTargets"]
	if !present {
		return targets, ""
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		return targets, "selectedTargets must map provider ids to target ids"
	}
	for providerID, entry := range entries {
		var targetID *string
		invalid := json.Unmarshal(entry, &targetID) != nil || targetID == nil || !usageIDPattern.MatchString(*targetID)
		if invalid || !usageIDPattern.MatchString(providerID) {
			return targets, "selectedTargets contains an invalid provider or target id"
		}
		targets[providerID] = *targetID
	}
	return targets, ""
}

func settingsStatusline(fields map[string]json.RawMessage) string {
	raw, present := fields["statuslineDisplay"]
	if !present {
		return ""
	}
	var display map[string]json.RawMessage
	if err := json.Unmarshal(raw, &display); err != nil || display == nil {
		return "statuslineDisplay must be a JSON object"
	}
	return ""
}

func migrateLegacyTarget(fields map[string]json.RawMessage, settings *Settings) string {
	raw, present := fields["fireworksAccountId"]
	if !present {
		return ""
	}
	var accountID *string
	if err := json.Unmarshal(raw, &accountID); err != nil || accountID == nil || !usageIDPattern.MatchString(*accountID) {
		return "fireworksAccountId must be a target id"
	}
	if _, selected := settings.SelectedTargets["fireworks"]; !selected {
		settings.SelectedTargets["fireworks"] = *accountID
	}
	return ""
}

func selectionField(targets map[string]string, providerID, targetID string) (json.RawMessage, error) {
	selected := maps.Clone(targets)
	if selected == nil {
		selected = map[string]string{}
	}
	selected[providerID] = targetID
	data, err := json.Marshal(selected)
	if err != nil {
		return nil, fmt.Errorf("encode usage target selection: %w", err)
	}
	return data, nil
}

func sameSettingsDocument(left, right settingsDocument) (bool, error) {
	if left.kind != right.kind {
		return false, nil
	}
	leftValue, err := canonicalSettingsFields(left.fields)
	if err != nil {
		return false, err
	}
	rightValue, err := canonicalSettingsFields(right.fields)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftValue, rightValue), nil
}

func canonicalSettingsFields(fields map[string]json.RawMessage) (any, error) {
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode usage settings: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode usage settings: %w", err)
	}
	return value, nil
}

func writeSettings(ctx context.Context, path string, expected, next settingsDocument) error {
	data, err := renderSettingsDocument(next)
	if err != nil {
		return err
	}
	temporary, err := createSettingsTemporary(ctx, path, data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	if err := confirmSettingsUnchanged(ctx, path, expected); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace usage settings: %w", err)
	}
	return nil
}

func renderSettingsDocument(document settingsDocument) ([]byte, error) {
	data, err := json.MarshalIndent(document.fields, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode usage settings: %w", err)
	}
	return append(data, '\n'), nil
}

func createSettingsTemporary(ctx context.Context, path string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create usage settings directory: %w", err)
	}
	suffix, err := randomSettingsSuffix()
	if err != nil {
		return "", err
	}
	temporary := filepath.Join(directory, "."+SettingsFileName+"."+suffix+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the temporary file sits beside the settings path
	if err != nil {
		return "", fmt.Errorf("create usage settings file: %w", err)
	}
	if err := writeSettingsTemporary(file, data); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return "", fmt.Errorf("restrict usage settings file: %w", err)
	}
	return temporary, nil
}

func writeSettingsTemporary(file *os.File, data []byte) (err error) {
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write usage settings file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync usage settings file: %w", err)
	}
	return nil
}

func confirmSettingsUnchanged(ctx context.Context, path string, expected settingsDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := readSettings(ctx, path)
	if err != nil {
		return err
	}
	same, err := sameSettingsDocument(current, expected)
	if err != nil {
		return err
	}
	if !same {
		return ErrSettingsChanged
	}
	return nil
}

func randomSettingsSuffix() (string, error) {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("name usage settings file: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}
