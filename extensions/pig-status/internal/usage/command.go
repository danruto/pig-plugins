package usage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	UsageActionRefresh = "Refresh current provider"
	UsageActionAnother = "Another configured provider…"
	UsageActionAll     = "All configured providers"
	UsageActionTarget  = "Select billing target…"
	UsageActionCodex   = "Toggle OpenAI Codex companion usage"
	UsageActionClose   = "Close"
)

var ErrUsageNeedsUI = errors.New("/usage requires TUI or RPC mode")

type CommandUI interface {
	HasUI() bool
	Select(ctx context.Context, title string, options []string) (string, bool, error)
	Notify(message, level string)
}

type UsageCommand struct {
	Dispatcher Dispatcher
	Settings   *SettingsStore
	Timeout    time.Duration
	Configure  func(CollectionSettings)
	Resets     *CodexResets
}

type menuSession struct {
	state    SettingsState
	outcomes []Outcome
}

func (c UsageCommand) Run(ctx context.Context, ui CommandUI, args string) error {
	if strings.TrimSpace(args) != "" {
		ui.Notify("/usage takes no arguments; choose an action from its menu.", "warning")
		return nil
	}
	if !ui.HasUI() {
		return ErrUsageNeedsUI
	}
	state, err := c.Settings.Reload(ctx)
	if err != nil {
		return err
	}
	if state.Kind == settingsKindInvalid {
		ui.Notify("pi-usage.json is invalid ("+state.Issue+"); settings are read-only until it is repaired.", "warning")
	}
	outcomes, ok := c.queryNotice(ctx, ui, c.selection(state, "current", ""))
	if !ok {
		return nil
	}
	return c.menu(ctx, ui, menuSession{state: state, outcomes: outcomes})
}

func (c UsageCommand) menu(ctx context.Context, ui CommandUI, session menuSession) error {
	for {
		options := c.menuOptions(session.outcomes)
		choice, ok, err := ui.Select(ctx, strings.Join(formatOutcomes(session.outcomes), "\n"), options)
		if err != nil {
			return err
		}
		if !ok || choice == UsageActionClose {
			return nil
		}
		session, err = c.act(ctx, ui, session, choice)
		if err != nil {
			return err
		}
	}
}

func (c UsageCommand) act(ctx context.Context, ui CommandUI, session menuSession, choice string) (menuSession, error) {
	switch choice {
	case UsageActionRefresh:
		return c.refresh(ctx, ui, session)
	case UsageActionAnother:
		return c.another(ctx, ui, session)
	case UsageActionAll:
		return c.all(ctx, ui, session)
	case UsageActionTarget:
		return c.target(ctx, ui, session)
	case UsageActionCodex:
		return c.toggleCodex(ctx, ui, session)
	case UsageActionReset:
		return c.reset(ctx, ui, session)
	case UsageActionClose:
		return session, nil
	default:
		return session, nil
	}
}

func (c UsageCommand) refresh(ctx context.Context, ui CommandUI, session menuSession) (menuSession, error) {
	outcomes, ok := c.queryNotice(ctx, ui, c.selection(session.state, "current", ""))
	if !ok {
		return session, nil
	}
	return menuSession{state: session.state, outcomes: outcomes}, nil
}

func (c UsageCommand) all(ctx context.Context, ui CommandUI, session menuSession) (menuSession, error) {
	outcomes, ok := c.queryNotice(ctx, ui, c.selection(session.state, "all", ""))
	if !ok {
		return session, nil
	}
	return menuSession{state: session.state, outcomes: outcomes}, nil
}

func (c UsageCommand) another(ctx context.Context, ui CommandUI, session menuSession) (menuSession, error) {
	providerIDs, err := c.otherProviders(ctx)
	if err != nil {
		notifyQueryFailure(ui, err)
		return session, nil
	}
	if len(providerIDs) == 0 {
		ui.Notify("No other supported provider has configured runtime auth.", "info")
		return session, nil
	}
	choice, ok, err := ui.Select(ctx, "Another configured provider", providerIDs)
	if err != nil {
		return session, err
	}
	if !ok {
		return session, nil
	}
	outcomes, ok := c.queryNotice(ctx, ui, c.selection(session.state, "provider", choice))
	if !ok {
		return session, nil
	}
	return menuSession{state: session.state, outcomes: outcomes}, nil
}

func (c UsageCommand) target(ctx context.Context, ui CommandUI, session menuSession) (menuSession, error) {
	choices, err := c.targetChoices(ctx, session)
	if err != nil {
		notifyQueryFailure(ui, err)
		return session, nil
	}
	if len(choices) == 0 {
		ui.Notify("Fireworks billing target discovery returned no choices.", "info")
		return session, nil
	}
	options := targetOptions(choices)
	picked, ok, err := ui.Select(ctx, "Select billing target", options)
	if err != nil {
		return session, err
	}
	targetID, found := selectedTargetOption(choices, options, picked)
	if !ok || !found {
		return session, nil
	}
	return c.commitTarget(ctx, ui, session, targetID)
}

func (c UsageCommand) commitTarget(ctx context.Context, ui CommandUI, session menuSession, targetID string) (menuSession, error) {
	confirm := func(ctx context.Context) error {
		choices, err := c.discoverTargets(ctx, session.state)
		if err != nil {
			return err
		}
		if !containsTarget(choices, targetID) {
			return ErrSettingsChanged
		}
		return nil
	}
	state, err := c.Settings.SelectTarget(ctx, "fireworks", targetID, confirm)
	if err != nil {
		notifySaveFailure(ui, err, "Fireworks accounts changed; choose again.")
		return session, nil
	}
	return c.afterSave(ctx, ui, state)
}

func (c UsageCommand) toggleCodex(ctx context.Context, ui CommandUI, session menuSession) (menuSession, error) {
	state, err := c.Settings.SetCodexUsage(ctx, !session.state.Settings.OpenAIUseCodexUsage)
	if err != nil {
		notifySaveFailure(ui, err, ErrSettingsChanged.Error())
		return session, nil
	}
	return c.afterSave(ctx, ui, state)
}

func (c UsageCommand) reset(ctx context.Context, ui CommandUI, session menuSession) (menuSession, error) {
	if err := c.Resets.Run(ctx, ui); err != nil {
		return session, err
	}
	return c.refresh(ctx, ui, session)
}

func (c UsageCommand) afterSave(ctx context.Context, ui CommandUI, state SettingsState) (menuSession, error) {
	if c.Configure != nil {
		c.Configure(state.Settings.Collection())
	}
	outcomes, ok := c.queryNotice(ctx, ui, c.selection(state, "current", ""))
	if !ok {
		return menuSession{state: state}, nil
	}
	return menuSession{state: state, outcomes: outcomes}, nil
}

func (c UsageCommand) targetChoices(ctx context.Context, session menuSession) ([]Target, error) {
	if len(session.outcomes) == 1 {
		visible := session.outcomes[0]
		if visible.ProviderID == "fireworks" && visible.Status == "selection-required" {
			return visible.Choices, nil
		}
	}
	return c.discoverTargets(ctx, session.state)
}

func (c UsageCommand) discoverTargets(ctx context.Context, state SettingsState) ([]Target, error) {
	selection := c.selection(state, "provider", "fireworks")
	delete(selection.Targets, "fireworks")
	outcomes, err := c.Dispatcher.Query(ctx, selection)
	if err != nil {
		return nil, err
	}
	for _, outcome := range outcomes {
		if outcome.ProviderID == "fireworks" {
			return outcome.Choices, nil
		}
	}
	return nil, nil
}

func (c UsageCommand) otherProviders(ctx context.Context) ([]string, error) {
	metadata, err := c.Dispatcher.Resolver.metadata(ctx)
	if err != nil {
		return nil, err
	}
	inventory, err := c.Dispatcher.Resolver.inventory(ctx)
	if err != nil {
		return nil, err
	}
	providerIDs := make([]string, 0, len(usageProviderIDs))
	for _, providerID := range usageProviderIDs {
		if providerID != metadata.ProviderID && inventory.Providers[providerID].Configured {
			providerIDs = append(providerIDs, providerID)
		}
	}
	return providerIDs, nil
}

func (c UsageCommand) selection(state SettingsState, mode, providerID string) Selection {
	return Selection{
		Mode:          mode,
		ProviderID:    providerID,
		Targets:       maps.Clone(state.Settings.SelectedTargets),
		UseCodexUsage: state.Settings.OpenAIUseCodexUsage,
		Timeout:       c.Timeout,
	}
}

func (c UsageCommand) queryNotice(ctx context.Context, ui CommandUI, selection Selection) ([]Outcome, bool) {
	outcomes, err := c.Dispatcher.Query(ctx, selection)
	if err != nil {
		notifyQueryFailure(ui, err)
		return nil, false
	}
	return outcomes, true
}

func notifyQueryFailure(ui CommandUI, err error) {
	if errors.Is(err, ErrUsageChanged) {
		ui.Notify("The active model or account changed; reopen /usage to retry.", "warning")
		return
	}
	ui.Notify("Usage unavailable: "+failedOutcome("", err).Reason, "error")
}

func notifySaveFailure(ui CommandUI, err error, changed string) {
	switch {
	case errors.Is(err, ErrSettingsInvalid):
		ui.Notify(ErrSettingsInvalid.Error(), "error")
	case errors.Is(err, ErrSettingsChanged):
		ui.Notify(changed, "error")
	default:
		ui.Notify("Could not save usage settings.", "error")
	}
}

func (c UsageCommand) menuOptions(outcomes []Outcome) []string {
	options := []string{UsageActionRefresh, UsageActionAnother, UsageActionAll}
	if targetSelectable(outcomes) {
		options = append(options, UsageActionTarget)
	}
	if c.Resets != nil && resetSelectable(outcomes) {
		options = append(options, UsageActionReset)
	}
	return append(options, UsageActionCodex, UsageActionClose)
}

func resetSelectable(outcomes []Outcome) bool {
	if len(outcomes) != 1 || outcomes[0].Report == nil {
		return false
	}
	return outcomes[0].Report.ProviderID == "openai-codex"
}

func targetSelectable(outcomes []Outcome) bool {
	if len(outcomes) != 1 {
		return false
	}
	visible := outcomes[0]
	return visible.ProviderID == "fireworks" && (visible.Status == "ok" || visible.Status == "selection-required")
}

func targetOptions(choices []Target) []string {
	options := make([]string, len(choices))
	for index, choice := range choices {
		options[index] = sanitize(choice.Label) + " (" + sanitize(choice.ID) + ")"
	}
	return options
}

func selectedTargetOption(choices []Target, options []string, picked string) (string, bool) {
	for index, option := range options {
		if option == picked {
			return choices[index].ID, true
		}
	}
	return "", false
}

func containsTarget(choices []Target, targetID string) bool {
	for _, choice := range choices {
		if choice.ID == targetID {
			return true
		}
	}
	return false
}

func FormatOutcome(outcome Outcome) []string {
	provider := providerLabel(outcome)
	lines := []string{provider + ": " + outcome.Status}
	if outcome.Status != "ok" || outcome.Report == nil {
		if outcome.Reason == "" {
			return lines
		}
		return append(lines, provider+": "+sanitize(outcome.Reason))
	}
	report := outcome.Report
	if account := accountName(outcome); account != "" {
		lines = append(lines, provider+" account: "+account)
	}
	seen := make(map[string]bool, len(report.Buckets)+len(report.Metrics))
	for _, bucket := range report.Buckets {
		lines = append(lines, uniqueLabel(seen, bucketLabel(bucket), bucket.ID)+": "+bucketAmounts(bucket))
	}
	for _, metric := range report.Metrics {
		lines = append(lines, uniqueLabel(seen, sanitize(metric.Label), metric.ID)+": "+metricAmount(metric))
	}
	return lines
}

func formatOutcomes(outcomes []Outcome) []string {
	lines := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		lines = append(lines, FormatOutcome(outcome)...)
	}
	return lines
}

func providerLabel(outcome Outcome) string {
	if outcome.Report != nil && outcome.Report.ProviderName != "" {
		return sanitize(outcome.Report.ProviderName)
	}
	return sanitize(outcome.ProviderID)
}

func accountName(outcome Outcome) string {
	label, target := sanitize(outcome.Report.AccountLabel), sanitize(outcome.TargetID)
	switch {
	case target == "" || target == label:
		return label
	case label == "":
		return target
	default:
		return label + " (" + target + ")"
	}
}

func uniqueLabel(seen map[string]bool, label, id string) string {
	if seen[label] {
		label += " (" + sanitize(id) + ")"
	}
	seen[label] = true
	return label
}

func bucketLabel(bucket Bucket) string {
	group := bucket.GroupLabel
	if group == "" {
		group = bucket.GroupID
	}
	parts := []string{group, bucket.Label}
	if bucket.WindowMinutes != nil && *bucket.WindowMinutes > 0 && !math.IsInf(*bucket.WindowMinutes, 0) {
		parts = append(parts, windowLabel(*bucket.WindowMinutes))
	}
	parts = append(parts, bucket.Period)
	qualifiers := make([]string, 0, len(parts))
	for _, part := range parts {
		part = sanitize(part)
		if part != "" && !slices.Contains(qualifiers, part) {
			qualifiers = append(qualifiers, part)
		}
	}
	return strings.Join(qualifiers, " · ")
}

func bucketAmounts(bucket Bucket) string {
	amounts := formatAmount(bucket.Used) + " / " + formatAmount(bucket.Limit)
	if bucket.Unit == "" {
		return amounts
	}
	return amounts + " " + sanitize(bucket.Unit)
}

func formatAmount(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func metricAmount(metric Metric) string {
	amount := formatMetricValue(metric.Value)
	suffix := metric.Currency
	if suffix == "" {
		suffix = metric.Unit
	}
	if suffix == "" {
		return amount
	}
	return amount + " " + sanitize(suffix)
}

func formatMetricValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "unknown"
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return sanitize(typed)
	default:
		return sanitize(fmt.Sprint(typed))
	}
}

func sanitize(text string) string {
	return strings.Map(func(character rune) rune {
		if character < ' ' || (character >= 0x7f && character <= 0x9f) {
			return -1
		}
		return character
	}, text)
}
