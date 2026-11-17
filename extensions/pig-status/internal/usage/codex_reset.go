package usage

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	UsageActionReset     = "Redeem usage limit reset…"
	ResetActionConfirm   = "Yes, use reset"
	ResetActionCancel    = "No, go back"
	ResetActionRetry     = "Try again"
	ResetActionBack      = "Back"
	CodexResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits" //nolint:gosec // G101: the identifier matches "cred"; the value is a public endpoint URL
	CodexResetConsumeURL = CodexResetCreditsURL + "/consume"                               //nolint:gosec // G101: the identifier matches "cred"; the value is a public endpoint URL
)

const (
	maxResetCreditIDChars = 1024
	maxResetTextRunes     = 160
	maxResetOptions       = 32
	maxResetSafeInteger   = 1 << 53
	defaultResetTimeout   = 15 * time.Second
)

const (
	codexResetDefaultTitle       = "Full reset"
	codexResetDefaultDescription = "Reset your current usage limits."
	codexResetNoResetsNotice     = "No usage limit resets are available."
	codexResetProviderNotice     = "Usage limit resets require the current model to use OpenAI Codex."
	codexResetLoadPrefix         = "Couldn't load usage limit resets: "
	codexResetChangedNotice      = "The active Codex model or account changed while loading usage limit resets."
	codexResetUnusedNotice       = "The active Codex model or account changed; the reset was not used."
	codexResetRetryPrefix        = "Couldn't reset usage: "
	codexResetRetrySuffix        = ". Try again with the same request."
	codexResetUncreatedNotice    = "the reset request could not be created"
	codexResetConsumesNotice     = "This consumes one earned reset for the current OpenAI Codex account."
	codexResetListDescription    = "Codex usage-limit reset endpoint"
	codexResetConsumeDescription = "Codex usage-limit reset consume endpoint"
)

var (
	errCodexResetProvider = errors.New("usage limit resets require the current model to use openai-codex")
	errCodexResetAccount  = errors.New("the active codex account is unavailable")
	errCodexResetChanged  = errors.New("the active codex model or account changed")
	errCodexResetCredits  = errors.New("codex reset credits response is invalid")
	errCodexResetConsume  = errors.New("codex reset consume response is invalid")
)

type CodexResetOutcomeCode string

const (
	CodexResetDone            CodexResetOutcomeCode = "reset"
	CodexResetNothingToReset  CodexResetOutcomeCode = "nothing_to_reset"
	CodexResetNoCredit        CodexResetOutcomeCode = "no_credit"
	CodexResetAlreadyRedeemed CodexResetOutcomeCode = "already_redeemed"
)

type CodexResetOption struct {
	CreditID    string
	Title       string
	Description string
	ExpiresAt   *int64
}

type CodexResetAvailability struct {
	AvailableCount int
	Options        []CodexResetOption
}

type CodexResetOutcome struct {
	Code         CodexResetOutcomeCode
	WindowsReset int
}

type CodexResetsOptions struct {
	Client       Client
	Current      func(context.Context) (FastScope, error)
	Identify     func(context.Context) (CodexAccount, error)
	NewRequestID func() (string, error)
	Timeout      time.Duration
}

type CodexResets struct {
	client       Client
	current      func(context.Context) (FastScope, error)
	identify     func(context.Context) (CodexAccount, error)
	newRequestID func() (string, error)
	timeout      time.Duration
}

type codexResetIdentity struct {
	sessionID     string
	providerID    string
	modelID       string
	accountID     string
	authorization string
}

func NewCodexResets(options CodexResetsOptions) *CodexResets {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultResetTimeout
	}
	requestID := options.NewRequestID
	if requestID == nil {
		requestID = newResetRequestID
	}
	return &CodexResets{
		client:       options.Client,
		current:      options.Current,
		identify:     options.Identify,
		newRequestID: requestID,
		timeout:      timeout,
	}
}

func NormalizeCodexResetCredits(payload map[string]any) (CodexResetAvailability, error) {
	count, ok := resetInteger(payload["available_count"])
	if !ok {
		return CodexResetAvailability{}, fmt.Errorf("%w: available_count", errCodexResetCredits)
	}
	credits := []any{}
	if raw, present := payload["credits"]; present {
		list, isList := raw.([]any)
		if !isList {
			return CodexResetAvailability{}, fmt.Errorf("%w: credits", errCodexResetCredits)
		}
		credits = list
	}
	options := make([]CodexResetOption, 0, len(credits))
	for _, credit := range credits {
		entry := object(credit)
		if !resetCreditAvailable(entry) {
			continue
		}
		option, err := normalizeResetOption(entry)
		if err != nil {
			return CodexResetAvailability{}, err
		}
		options = append(options, option)
	}
	options = sortResetOptions(options)
	if limit := min(count, maxResetOptions); len(options) > limit {
		options = options[:limit]
	}
	if count > 0 && len(options) == 0 {
		options = append(options, genericResetOption())
	}
	return CodexResetAvailability{AvailableCount: count, Options: options}, nil
}

func (r *CodexResets) Run(ctx context.Context, ui CommandUI) error {
	expected, account, err := r.snapshot(ctx)
	if err != nil {
		return notifyReset(ctx, ui, "error", resetIdentityNotice(err))
	}
	availability, err := r.load(ctx, account)
	if err != nil {
		return notifyReset(ctx, ui, "error", codexResetLoadPrefix+resetFailureReason(err)+".")
	}
	if current, _, err := r.snapshot(ctx); err != nil || current != expected {
		return notifyReset(ctx, ui, "warning", codexResetChangedNotice)
	}
	if availability.AvailableCount == 0 {
		return notifyReset(ctx, ui, "info", codexResetNoResetsNotice)
	}
	option, ok, err := r.choose(ctx, ui, availability)
	if err != nil || !ok {
		return err
	}
	return r.redeem(ctx, ui, expected, option)
}

func boundedCall[T any](ctx context.Context, timeout time.Duration, call func(context.Context) (T, error)) (T, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return call(callCtx)
}

func (r *CodexResets) snapshot(ctx context.Context) (codexResetIdentity, CodexAccount, error) {
	scope, err := boundedCall(ctx, r.timeout, r.current)
	if err != nil {
		return codexResetIdentity{}, CodexAccount{}, errCodexResetAccount
	}
	if !scope.HasModel || providerID(scope.Model.Provider) != providerOpenAICodex {
		return codexResetIdentity{}, CodexAccount{}, errCodexResetProvider
	}
	account, err := boundedCall(ctx, r.timeout, r.identify)
	if err != nil {
		return codexResetIdentity{}, CodexAccount{}, errCodexResetAccount
	}
	identity := codexResetIdentity{
		sessionID:     scope.SessionID,
		providerID:    scope.Model.Provider,
		modelID:       scope.Model.ID,
		accountID:     account.AccountID,
		authorization: headerValue(account.Headers, "Authorization"),
	}
	if identity.accountID == "" || identity.authorization == "" {
		return codexResetIdentity{}, CodexAccount{}, errCodexResetAccount
	}
	return identity, account, nil
}

func (r *CodexResets) revalidate(ctx context.Context, account CodexAccount) error {
	if account.Revalidate == nil {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := account.Revalidate(callCtx); err != nil {
		return errCodexResetAccount
	}
	return nil
}

func (r *CodexResets) load(ctx context.Context, account CodexAccount) (CodexResetAvailability, error) {
	if err := r.revalidate(ctx, account); err != nil {
		return CodexResetAvailability{}, err
	}
	payload, err := r.request(ctx, usageRequest{
		endpoint:    CodexResetCreditsURL,
		description: codexResetListDescription,
		headers:     directoryHeaders(account),
		maxBytes:    maxSuccessBodyBytes,
	})
	if err != nil {
		return CodexResetAvailability{}, err
	}
	return NormalizeCodexResetCredits(payload)
}

func (r *CodexResets) choose(ctx context.Context, ui CommandUI, availability CodexResetAvailability) (CodexResetOption, bool, error) {
	labels := append(resetOptionLabels(availability.Options), ResetActionBack)
	title := resetAvailabilityTitle(availability.AvailableCount)
	for {
		choice, ok, err := selectReset(ctx, ui, title, labels)
		if err != nil {
			return CodexResetOption{}, false, err
		}
		if !ok || choice == ResetActionBack {
			return CodexResetOption{}, false, nil
		}
		option, found := resetOptionForLabel(availability.Options, labels, choice)
		if !found {
			return CodexResetOption{}, false, nil
		}
		confirmed, err := r.confirm(ctx, ui, option)
		if err != nil {
			return CodexResetOption{}, false, err
		}
		if confirmed {
			return option, true, nil
		}
	}
}

func (r *CodexResets) confirm(ctx context.Context, ui CommandUI, option CodexResetOption) (bool, error) {
	title := strings.Join([]string{option.Title, resetExpiration(option), option.Description, codexResetConsumesNotice}, "\n")
	choice, ok, err := selectReset(ctx, ui, title, []string{ResetActionConfirm, ResetActionCancel})
	if err != nil {
		return false, err
	}
	return ok && choice == ResetActionConfirm, nil
}

func (r *CodexResets) redeem(ctx context.Context, ui CommandUI, expected codexResetIdentity, option CodexResetOption) error {
	requestID, err := r.newRequestID()
	if err != nil {
		return notifyReset(ctx, ui, "error", codexResetRetryPrefix+codexResetUncreatedNotice+".")
	}
	for {
		outcome, err := r.consume(ctx, expected, option, requestID)
		if err == nil {
			return notifyResetOutcome(ctx, ui, outcome.Code)
		}
		if errors.Is(err, errCodexResetChanged) {
			return notifyReset(ctx, ui, "warning", codexResetUnusedNotice)
		}
		again, err := r.retry(ctx, ui, err)
		if err != nil || !again {
			return err
		}
	}
}

func (r *CodexResets) retry(ctx context.Context, ui CommandUI, cause error) (bool, error) {
	title := codexResetRetryPrefix + resetFailureReason(cause) + codexResetRetrySuffix
	choice, ok, err := selectReset(ctx, ui, title, []string{ResetActionRetry, ResetActionBack})
	if err != nil {
		return false, err
	}
	return ok && choice == ResetActionRetry, nil
}

func (r *CodexResets) consume(ctx context.Context, expected codexResetIdentity, option CodexResetOption, requestID string) (CodexResetOutcome, error) {
	current, account, err := r.snapshot(ctx)
	if err != nil || current != expected {
		return CodexResetOutcome{}, errCodexResetChanged
	}
	if err := r.revalidate(ctx, account); err != nil {
		return CodexResetOutcome{}, err
	}
	body, err := json.Marshal(resetConsumeBody(option, requestID))
	if err != nil {
		return CodexResetOutcome{}, err
	}
	payload, err := r.request(ctx, usageRequest{
		endpoint:    CodexResetConsumeURL,
		description: codexResetConsumeDescription,
		headers:     directoryHeaders(account),
		maxBytes:    maxSuccessBodyBytes,
		method:      http.MethodPost,
		body:        body,
	})
	if err != nil {
		return CodexResetOutcome{}, err
	}
	return normalizeCodexResetOutcome(payload)
}

func selectReset(ctx context.Context, ui CommandUI, title string, options []string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	choice, ok, err := ui.Select(ctx, title, options)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	return choice, ok, err
}

func (r *CodexResets) request(ctx context.Context, request usageRequest) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	payload, _, err := r.client.fetchBounded(requestCtx, request)
	return payload, err
}

func resetConsumeBody(option CodexResetOption, requestID string) map[string]any {
	body := map[string]any{"redeem_request_id": requestID}
	if option.CreditID != "" {
		body["credit_id"] = option.CreditID
	}
	return body
}

func normalizeCodexResetOutcome(payload map[string]any) (CodexResetOutcome, error) {
	code, ok := payload["code"].(string)
	if !ok || !validResetOutcomeCode(CodexResetOutcomeCode(code)) {
		return CodexResetOutcome{}, fmt.Errorf("%w: code", errCodexResetConsume)
	}
	outcome := CodexResetOutcome{Code: CodexResetOutcomeCode(code)}
	raw, present := payload["windows_reset"]
	if !present {
		return outcome, nil
	}
	windows, ok := resetInteger(raw)
	if !ok {
		return CodexResetOutcome{}, fmt.Errorf("%w: windows_reset", errCodexResetConsume)
	}
	outcome.WindowsReset = windows
	return outcome, nil
}

func validResetOutcomeCode(code CodexResetOutcomeCode) bool {
	switch code {
	case CodexResetDone, CodexResetNothingToReset, CodexResetNoCredit, CodexResetAlreadyRedeemed:
		return true
	default:
		return false
	}
}

func notifyResetOutcome(ctx context.Context, ui CommandUI, code CodexResetOutcomeCode) error {
	switch code {
	case CodexResetDone:
		return notifyReset(ctx, ui, "info", "Usage reset.")
	case CodexResetAlreadyRedeemed:
		return notifyReset(ctx, ui, "info", "Usage reset was already completed.")
	case CodexResetNothingToReset:
		return notifyReset(ctx, ui, "warning", "Your usage does not need a reset right now.")
	default:
		return notifyReset(ctx, ui, "warning", codexResetNoResetsNotice)
	}
}

func notifyReset(ctx context.Context, ui CommandUI, level, notice string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ui.Notify(notice, level)
	return nil
}

func resetIdentityNotice(err error) string {
	if errors.Is(err, errCodexResetProvider) {
		return codexResetProviderNotice
	}
	return codexResetLoadPrefix + resetFailureReason(err) + "."
}

func resetFailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the request timed out"
	case errors.Is(err, errCodexResetAccount), errors.Is(err, errCodexResetProvider):
		return "the account could not be verified"
	case errors.Is(err, errCodexResetCredits), errors.Is(err, errCodexResetConsume):
		return "the endpoint returned an invalid response"
	default:
		return "the request failed"
	}
}

func resetAvailabilityTitle(count int) string {
	if count == 1 {
		return "1 usage limit reset available."
	}
	return strconv.Itoa(count) + " usage limit resets available."
}

func resetOptionLabels(options []CodexResetOption) []string {
	labels := make([]string, 0, len(options))
	seen := make(map[string]int, len(options))
	for _, option := range options {
		label := option.Title + " — " + resetExpiration(option)
		seen[label]++
		if seen[label] > 1 {
			label += " (" + strconv.Itoa(seen[label]) + ")"
		}
		labels = append(labels, label)
	}
	return labels
}

func resetOptionForLabel(options []CodexResetOption, labels []string, label string) (CodexResetOption, bool) {
	index := slices.Index(labels, label)
	if index < 0 || index >= len(options) {
		return CodexResetOption{}, false
	}
	return options[index], true
}

func resetExpiration(option CodexResetOption) string {
	if option.ExpiresAt == nil {
		return "Does not expire."
	}
	return "Expires " + time.Unix(*option.ExpiresAt, 0).Local().Format(time.RFC3339) + "."
}

func resetCreditAvailable(entry map[string]any) bool {
	return entry != nil && entry["status"] == "available" && entry["reset_type"] == "codex_rate_limits"
}

func normalizeResetOption(entry map[string]any) (CodexResetOption, error) {
	id, ok := entry["id"].(string)
	if !ok || id == "" || len(id) > maxResetCreditIDChars {
		return CodexResetOption{}, fmt.Errorf("%w: credit ID", errCodexResetCredits)
	}
	option := CodexResetOption{
		CreditID:    id,
		Title:       resetText(entry["title"], codexResetDefaultTitle),
		Description: resetText(entry["description"], codexResetDefaultDescription),
	}
	raw, present := entry["expires_at"]
	if !present || raw == nil {
		return option, nil
	}
	text, ok := raw.(string)
	if !ok {
		return CodexResetOption{}, fmt.Errorf("%w: expiration time", errCodexResetCredits)
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return CodexResetOption{}, fmt.Errorf("%w: expiration time", errCodexResetCredits)
	}
	seconds := parsed.Unix()
	option.ExpiresAt = &seconds
	return option, nil
}

func genericResetOption() CodexResetOption {
	return CodexResetOption{Title: codexResetDefaultTitle, Description: codexResetDefaultDescription}
}

func sortResetOptions(options []CodexResetOption) []CodexResetOption {
	slices.SortStableFunc(options, func(left, right CodexResetOption) int {
		switch {
		case left.ExpiresAt == nil && right.ExpiresAt == nil:
			return 0
		case left.ExpiresAt == nil:
			return 1
		case right.ExpiresAt == nil:
			return -1
		default:
			return cmp.Compare(*left.ExpiresAt, *right.ExpiresAt)
		}
	})
	return options
}

func resetText(value any, fallback string) string {
	text, ok := value.(string)
	if !ok {
		return fallback
	}
	text = truncateRunes(sanitize(text), maxResetTextRunes)
	if text == "" {
		return fallback
	}
	return text
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func resetInteger(value any) (int, bool) {
	if integer, ok := value.(int); ok {
		value = float64(integer)
	}
	number, ok := jsNumberValue(value)
	if !ok || number < 0 || number >= maxResetSafeInteger || number != math.Trunc(number) {
		return 0, false
	}
	return int(number), true
}

func newResetRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
