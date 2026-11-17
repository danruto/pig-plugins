package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	acceptResetToken     = "synthetic-reset-access"
	acceptResetAccountID = "synthetic-account"
	acceptResetSession   = "synthetic-session"
	acceptResetCreditID  = "credit-synthetic-1"
	acceptResetBonusID   = "credit-synthetic-2"
	acceptResetPath      = "/backend-api/wham/rate-limit-reset-credits"
	acceptResetExpires   = "2026-12-18T00:00:00Z"
)

const acceptResetListBody = `{"available_count":2,"credits":[` +
	`{"id":"credit-synthetic-1","status":"available","reset_type":"codex_rate_limits","title":"Full reset","description":"Reset your current usage limits.","expires_at":"2026-12-18T00:00:00Z"},` +
	`{"id":"credit-synthetic-2","status":"available","reset_type":"codex_rate_limits","title":"Bonus reset","description":"One extra reset.","expires_at":null}` +
	`]}`

type acceptResetCall struct {
	Method        string
	Host          string
	Path          string
	Body          string
	Authorization string
	Account       string
	ContentType   string
	Deadline      bool
}

type acceptResetReply struct {
	Status int
	Body   string
	Err    error
}

type acceptResetServer struct {
	mu        sync.Mutex
	calls     []acceptResetCall
	lists     int
	posts     int
	list      func(int) acceptResetReply
	consume   func(int) acceptResetReply
	onConsume func()
}

func (s *acceptResetServer) RoundTrip(request *http.Request) (*http.Response, error) {
	body := ""
	if request.Body != nil {
		read, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		body = string(read)
	}
	_, hasDeadline := request.Context().Deadline()
	call := acceptResetCall{
		Method:        request.Method,
		Host:          request.URL.Host,
		Path:          request.URL.Path,
		Body:          body,
		Authorization: acceptResetHeader(request.Header, "Authorization"),
		Account:       acceptResetHeader(request.Header, "ChatGPT-Account-Id"),
		ContentType:   acceptResetHeader(request.Header, "Content-Type"),
		Deadline:      hasDeadline,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	if request.Method == http.MethodPost {
		s.posts++
		reply := s.consume(s.posts)
		if s.onConsume != nil {
			s.onConsume()
		}
		if reply.Err != nil {
			return nil, reply.Err
		}
		return acceptFastResponse(reply.Status, reply.Body), nil
	}
	s.lists++
	reply := s.list(s.lists)
	if reply.Err != nil {
		return nil, reply.Err
	}
	return acceptFastResponse(reply.Status, reply.Body), nil
}

func acceptResetHeader(header http.Header, name string) string {
	for _, key := range slices.Sorted(maps.Keys(header)) {
		if strings.EqualFold(key, name) {
			return strings.Join(header[key], ", ")
		}
	}
	return ""
}

func (s *acceptResetServer) callsByMethod(method string) []acceptResetCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := make([]acceptResetCall, 0, len(s.calls))
	for _, call := range s.calls {
		if call.Method == method {
			found = append(found, call)
		}
	}
	return found
}

type acceptResetIdentity struct {
	scope   FastScope
	account CodexAccount
}

type acceptResetFixture struct {
	identity *acceptResetIdentity
	ui       *acceptUI
	server   *acceptResetServer
	resets   *CodexResets
	ids      atomic.Int32
}

type acceptResetOptions struct {
	scope        func(context.Context) (FastScope, error)
	identify     func(context.Context) (CodexAccount, error)
	newRequestID func() (string, error)
}

func acceptResetScope(session, modelID string) FastScope {
	return FastScope{SessionID: session, HasModel: true, Model: *acceptFastModel(modelID)}
}

func acceptResetAccount() CodexAccount {
	return acceptResetAccountWithRevalidate(func(context.Context) error { return nil })
}

func acceptResetAccountWithRevalidate(revalidate func(context.Context) error) CodexAccount {
	return CodexAccount{
		AccountID:  acceptResetAccountID,
		Headers:    http.Header{"Authorization": {"Bearer " + acceptResetToken}, "ChatGPT-Account-Id": {acceptResetAccountID}},
		Revalidate: revalidate,
	}
}

func acceptResetFixed(status int, body string) func(int) acceptResetReply {
	return func(int) acceptResetReply { return acceptResetReply{Status: status, Body: body} }
}

func acceptResetList(status int, body string) func(int) acceptResetReply {
	return acceptResetFixed(status, body)
}

func acceptResetConsume(status int, body string) func(int) acceptResetReply {
	return acceptResetFixed(status, body)
}

func acceptResetNew(t *testing.T, tune func(*acceptResetOptions)) *acceptResetFixture {
	t.Helper()
	fixture := &acceptResetFixture{
		identity: &acceptResetIdentity{scope: acceptResetScope(acceptResetSession, acceptFastModelID), account: acceptResetAccount()},
		ui:       &acceptUI{interactive: true},
		server: &acceptResetServer{
			list:    acceptResetList(http.StatusOK, acceptResetListBody),
			consume: acceptResetConsume(http.StatusOK, `{"code":"reset","windows_reset":4}`),
		},
	}
	options := acceptResetOptions{
		scope:        func(context.Context) (FastScope, error) { return fixture.identity.scope, nil },
		identify:     func(context.Context) (CodexAccount, error) { return fixture.identity.account, nil },
		newRequestID: func() (string, error) { return fmt.Sprintf("redemption-synthetic-%d", fixture.ids.Add(1)), nil },
	}
	if tune != nil {
		tune(&options)
	}
	fixture.resets = NewCodexResets(CodexResetsOptions{
		Client:       Client{Transport: fixture.server},
		Current:      options.scope,
		Identify:     options.identify,
		NewRequestID: options.newRequestID,
		Timeout:      acceptQueryTimeout,
	})
	return fixture
}

type acceptResetUI struct {
	*acceptUI
	hook func(int)
}

func (u *acceptResetUI) Select(ctx context.Context, title string, options []string) (string, bool, error) {
	index := len(u.titles)
	if u.hook != nil {
		u.hook(index)
	}
	return u.acceptUI.Select(ctx, title, options)
}

func acceptResetScript(fixture *acceptResetFixture, hook func(int), steps ...acceptSelection) *acceptResetUI {
	fixture.ui.script = steps
	return &acceptResetUI{acceptUI: fixture.ui, hook: hook}
}

func acceptResetLabel(t *testing.T, title, expires string) string {
	t.Helper()
	if expires == "" {
		return title + " — Does not expire."
	}
	stamp, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		t.Fatalf("parse fixture expiration %q: %v", expires, err)
	}
	return title + " — Expires " + stamp.Local().Format(time.RFC3339) + "."
}

func acceptResetBody(t *testing.T, call acceptResetCall) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(call.Body), &decoded); err != nil {
		t.Fatalf("consume body is not a JSON object: %v (%q)", err, call.Body)
	}
	return decoded
}

func acceptResetSafe(t *testing.T, ui *acceptUI, secrets ...string) {
	t.Helper()
	for _, notice := range ui.notices {
		requireSafeText(t, notice)
		for _, secret := range secrets {
			acceptNotContains(t, notice, secret, "notice disclosure of "+secret)
		}
	}
}

func TestCodexResetsRunSendsNothingWhenTheAccountCannotBeRevalidated(t *testing.T) {
	fixture := acceptResetNew(t, func(options *acceptResetOptions) {
		options.identify = func(context.Context) (CodexAccount, error) {
			return acceptResetAccountWithRevalidate(func(context.Context) error {
				return errors.New("raw-provider-canary " + acceptResetToken)
			}), nil
		}
	})
	ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "unverifiable account")
	acceptEqual(t, 1, len(fixture.ui.notices), "notices")
	acceptPrefix(t, fixture.ui.notices[0], "Couldn't load usage limit resets:", "revalidation failure notice")
	acceptEqual(t, []string{"error"}, fixture.ui.levels, "revalidation failure notice level")
	acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, "raw-provider-canary")
}

func TestCodexResetsRunRetriesWhenTheAccountCannotBeRevalidatedBeforeConsuming(t *testing.T) {
	var reject bool
	fixture := acceptResetNew(t, func(options *acceptResetOptions) {
		options.identify = func(context.Context) (CodexAccount, error) {
			return acceptResetAccountWithRevalidate(func(context.Context) error {
				if reject {
					return errors.New("raw-provider-canary")
				}
				return nil
			}), nil
		}
	})
	ui := acceptResetScript(fixture, func(index int) {
		if index == 1 {
			reject = true
		}
	},
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
		acceptSelection{choice: ResetActionBack, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "abandoned retry")
	acceptEqual(t, 3, len(fixture.ui.titles), "prompts")
	acceptContains(t, fixture.ui.titles[2], "Couldn't reset usage: ", "retry prompt")
	acceptContains(t, fixture.ui.titles[2], "Try again with the same request.", "retry prompt")
	acceptEqual(t, []string{ResetActionRetry, ResetActionBack}, fixture.ui.optionSets[2], "retry options")
	acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
	acceptEqual(t, int32(1), fixture.ids.Load(), "redemption identifiers created")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID, "redemption-synthetic-1", "raw-provider-canary")
}

func TestCodexResetsRunAcceptsAnAccountWithoutARevalidator(t *testing.T) {
	fixture := acceptResetNew(t, func(options *acceptResetOptions) {
		options.identify = func(context.Context) (CodexAccount, error) {
			return acceptResetAccountWithRevalidate(nil), nil
		}
	})
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "account without a revalidator")
	acceptEqual(t, []string{"Usage reset."}, fixture.ui.notices, "outcome notice")
	acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	posts := fixture.server.callsByMethod(http.MethodPost)
	acceptEqual(t, 1, len(posts), "consume requests")
	acceptEqual[any](t, map[string]any{"redeem_request_id": "redemption-synthetic-1", "credit_id": acceptResetCreditID}, acceptResetBody(t, posts[0]), "consume body")
}

func TestCodexResetsRunSendsNothingWhenTheRedemptionIDIsUnavailable(t *testing.T) {
	fixture := acceptResetNew(t, func(options *acceptResetOptions) {
		options.newRequestID = func() (string, error) {
			return "", errors.New("synthetic identifier failure")
		}
	})
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "unavailable redemption identifier")
	acceptEqual(t, 1, len(fixture.ui.notices), "notices")
	acceptPrefix(t, fixture.ui.notices[0], "Couldn't reset usage: ", "unavailable identifier notice")
	acceptEqual(t, []string{"error"}, fixture.ui.levels, "unavailable identifier notice level")
	acceptEqual(t, 2, len(fixture.ui.titles), "prompts")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
}

func TestCodexResetsRunStopsOnCancellationDuringTheConsume(t *testing.T) {
	for _, tt := range []struct {
		name  string
		reply acceptResetReply
	}{
		{name: "successful consume", reply: acceptResetReply{Status: http.StatusOK, Body: `{"code":"reset","windows_reset":4}`}},
		{name: "failed consume", reply: acceptResetReply{Status: http.StatusInternalServerError, Body: `{"error":"raw-provider-canary"}`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fixture.server.onConsume = cancel
			fixture.server.consume = func(int) acceptResetReply { return tt.reply }
			ui := acceptResetScript(fixture, nil,
				acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
				acceptSelection{choice: ResetActionConfirm, ok: true},
			)

			err := fixture.resets.Run(ctx, ui)

			acceptErrorIs(t, err, context.Canceled, "cancelled consume")
			acceptEqual(t, 0, len(fixture.ui.notices), "notices")
			acceptEqual(t, 2, len(fixture.ui.titles), "prompts")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
			acceptEqual(t, int32(1), fixture.ids.Load(), "redemption identifiers created")
		})
	}
}

func TestCodexResetsRunRefusesAnAccountWithoutCredentials(t *testing.T) {
	for _, tt := range []struct {
		name    string
		account CodexAccount
	}{
		{name: "no account id", account: CodexAccount{Headers: http.Header{"Authorization": {"Bearer " + acceptResetToken}}}},
		{name: "no authorization", account: CodexAccount{AccountID: acceptResetAccountID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, func(options *acceptResetOptions) {
				options.identify = func(context.Context) (CodexAccount, error) { return tt.account, nil }
			})
			ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

			err := fixture.resets.Run(t.Context(), ui)

			acceptNoError(t, err, "unverifiable account")
			acceptEqual(t, 1, len(fixture.ui.notices), "notices")
			acceptEqual(t, []string{"error"}, fixture.ui.levels, "notice level")
			acceptNotEqual(t, "", fixture.ui.notices[0], "notice text")
			acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
		})
	}
}

func TestCodexResetsRunRedeemsTheSelectedCredit(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "confirmed redemption")
	acceptEqual(t, []string{"Usage reset."}, fixture.ui.notices, "outcome notice")
	acceptEqual(t, []string{"info"}, fixture.ui.levels, "outcome notice level")
	acceptEqual(t, 2, len(fixture.ui.titles), "prompts")
	acceptContains(t, fixture.ui.titles[0], "2 usage limit reset", "picker title")
	acceptEqual(t, []string{
		acceptResetLabel(t, "Full reset", acceptResetExpires),
		acceptResetLabel(t, "Bonus reset", ""),
		ResetActionBack,
	}, fixture.ui.optionSets[0], "picker options")
	acceptEqual(t, []string{ResetActionConfirm, ResetActionCancel}, fixture.ui.optionSets[1], "confirmation options")
	acceptContains(t, fixture.ui.titles[1], "Full reset", "confirmation title")
	acceptContains(t, fixture.ui.titles[1], "This consumes one earned reset for the current OpenAI Codex account.", "confirmation title")
	results := fixture.server.callsByMethod(http.MethodGet)
	posts := fixture.server.callsByMethod(http.MethodPost)
	acceptEqual(t, 1, len(results), "list requests")
	acceptEqual(t, 1, len(posts), "consume requests")
	acceptEqual(t, "chatgpt.com", results[0].Host, "list host")
	acceptEqual(t, acceptResetPath, results[0].Path, "list path")
	acceptEqual(t, "Bearer "+acceptResetToken, results[0].Authorization, "list authorization")
	acceptEqual(t, true, results[0].Deadline, "list request deadline")
	acceptEqual(t, acceptResetPath+"/consume", posts[0].Path, "consume path")
	acceptEqual(t, "application/json", posts[0].ContentType, "consume content type")
	acceptEqual(t, "Bearer "+acceptResetToken, posts[0].Authorization, "consume authorization")
	acceptEqual(t, true, posts[0].Deadline, "consume request deadline")
	acceptEqual[any](t, map[string]any{"redeem_request_id": "redemption-synthetic-1", "credit_id": acceptResetCreditID}, acceptResetBody(t, posts[0]), "consume body")
	acceptEqual(t, int32(1), fixture.ids.Load(), "redemption identifiers created")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID, "redemption-synthetic-1")
}

func TestCodexResetsRunSendsTheAccountHeader(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "confirmed redemption")
	acceptEqual(t, 2, len(fixture.ui.titles), "prompts")
	results := fixture.server.callsByMethod(http.MethodGet)
	posts := fixture.server.callsByMethod(http.MethodPost)
	acceptEqual(t, 1, len(results), "list requests")
	acceptEqual(t, 1, len(posts), "consume requests")
	acceptEqual(t, acceptResetAccountID, results[0].Account, "list account header")
	acceptEqual(t, acceptResetAccountID, posts[0].Account, "consume account header")
}

func TestCodexResetsRunOmitsTheCreditIDForTheGenericOption(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	fixture.server.list = acceptResetList(http.StatusOK, `{"available_count":1,"credits":[]}`)
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", ""), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "generic redemption")
	acceptEqual(t, []string{"Usage reset."}, fixture.ui.notices, "outcome notice")
	acceptEqual(t, 2, len(fixture.ui.titles), "prompts")
	acceptEqual(t, []string{acceptResetLabel(t, "Full reset", ""), ResetActionBack}, fixture.ui.optionSets[0], "generic picker options")
	posts := fixture.server.callsByMethod(http.MethodPost)
	acceptEqual(t, 1, len(posts), "consume requests")
	acceptEqual[any](t, map[string]any{"redeem_request_id": "redemption-synthetic-1"}, acceptResetBody(t, posts[0]), "consume body without a credit id")
}

func TestCodexResetsRunLabelsDuplicateOptionsUniquely(t *testing.T) {
	body := `{"available_count":2,"credits":[` +
		`{"id":"credit-synthetic-1","status":"available","reset_type":"codex_rate_limits","title":"Full reset","description":"Reset your current usage limits.","expires_at":null},` +
		`{"id":"credit-synthetic-2","status":"available","reset_type":"codex_rate_limits","title":"Full reset","description":"Reset your current usage limits.","expires_at":null}` +
		`]}`
	fixture := acceptResetNew(t, nil)
	fixture.server.list = acceptResetList(http.StatusOK, body)
	ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "duplicate labels")
	acceptEqual(t, 1, len(fixture.ui.titles), "prompts")
	acceptEqual(t, []string{
		acceptResetLabel(t, "Full reset", ""),
		acceptResetLabel(t, "Full reset", "") + " (2)",
		ResetActionBack,
	}, fixture.ui.optionSets[0], "unique picker options")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
}

func TestCodexResetsRunGeneratesAUUIDRedemptionID(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	fixture.resets = NewCodexResets(CodexResetsOptions{
		Client:   Client{Transport: fixture.server},
		Current:  func(context.Context) (FastScope, error) { return fixture.identity.scope, nil },
		Identify: func(context.Context) (CodexAccount, error) { return fixture.identity.account, nil },
		Timeout:  acceptQueryTimeout,
	})
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "generated redemption identifier")
	posts := fixture.server.callsByMethod(http.MethodPost)
	acceptEqual(t, 1, len(posts), "consume requests")
	identifier, ok := acceptResetBody(t, posts[0])["redeem_request_id"].(string)
	acceptEqual(t, true, ok, "redeem_request_id is a string")
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	acceptEqual(t, true, pattern.MatchString(identifier), "redeem_request_id is a UUIDv4")
}

func TestCodexResetsRunKeepsTheRedemptionIDAcrossRetries(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	fixture.server.consume = func(attempt int) acceptResetReply {
		if attempt == 1 {
			return acceptResetReply{Status: http.StatusInternalServerError, Body: `{"error":"raw-provider-canary"}`}
		}
		return acceptResetReply{Status: http.StatusOK, Body: `{"code":"reset","windows_reset":4}`}
	}
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
		acceptSelection{choice: ResetActionRetry, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "retried redemption")
	acceptEqual(t, 3, len(fixture.ui.titles), "prompts")
	acceptContains(t, fixture.ui.titles[2], "Try again with the same request.", "retry prompt")
	acceptEqual(t, []string{ResetActionRetry, ResetActionBack}, fixture.ui.optionSets[2], "retry options")
	posts := fixture.server.callsByMethod(http.MethodPost)
	acceptEqual(t, 2, len(posts), "consume requests")
	first := acceptResetBody(t, posts[0])
	second := acceptResetBody(t, posts[1])
	acceptEqual[any](t, first["redeem_request_id"], second["redeem_request_id"], "retried redemption identifier")
	acceptEqual[any](t, "redemption-synthetic-1", first["redeem_request_id"], "first redemption identifier")
	acceptEqual(t, int32(1), fixture.ids.Load(), "redemption identifiers created")
	acceptNotEqual(t, 0, len(fixture.ui.notices), "notices")
	acceptEqual(t, "Usage reset.", fixture.ui.notices[len(fixture.ui.notices)-1], "final outcome notice")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID, "redemption-synthetic-1", "raw-provider-canary")
}

func TestCodexResetsRunStartsAFreshRedemptionAfterBack(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	fixture.server.consume = acceptResetConsume(http.StatusInternalServerError, `{"error":"synthetic"}`)
	failed := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
		acceptSelection{choice: ResetActionBack, ok: true},
	)

	failedErr := fixture.resets.Run(t.Context(), failed)
	fixture.server.consume = acceptResetConsume(http.StatusOK, `{"code":"reset","windows_reset":4}`)
	fixture.ui.titles = nil
	fixture.ui.optionSets = nil
	renewed := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)
	renewedErr := fixture.resets.Run(t.Context(), renewed)

	acceptNoError(t, failedErr, "abandoned redemption")
	acceptNoError(t, renewedErr, "renewed redemption")
	posts := fixture.server.callsByMethod(http.MethodPost)
	acceptEqual(t, 2, len(posts), "consume requests")
	acceptNotEqual[any](t, acceptResetBody(t, posts[0])["redeem_request_id"], acceptResetBody(t, posts[1])["redeem_request_id"], "redemption identifier after Back")
	acceptEqual(t, int32(2), fixture.ids.Load(), "redemption identifiers created")
	acceptEqual(t, []string{"Usage reset."}, fixture.ui.notices, "renewed outcome notice")
}

func TestCodexResetsRunSendsNothingWhenConfirmationIsCancelled(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	ui := acceptResetScript(fixture, nil,
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionCancel, ok: true},
		acceptSelection{ok: false},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "cancelled confirmation")
	acceptEqual(t, 3, len(fixture.ui.titles), "prompts")
	acceptEqual(t, []string{
		acceptResetLabel(t, "Full reset", acceptResetExpires),
		acceptResetLabel(t, "Bonus reset", ""),
		ResetActionBack,
	}, fixture.ui.optionSets[2], "picker options after a cancel")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
	acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptEqual(t, 0, fixture.ids.Load(), "redemption identifiers created")
	acceptEqual(t, 0, len(fixture.ui.notices), "notices")
}

func TestCodexResetsRunSendsNothingWhenTheScopeChangesWhileLoading(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*acceptResetFixture)
	}{
		{name: "session", change: func(f *acceptResetFixture) { f.identity.scope.SessionID = "successor-session" }},
		{name: "model provider", change: func(f *acceptResetFixture) { f.identity.scope.Model.Provider = "deepseek" }},
		{name: "model id", change: func(f *acceptResetFixture) { f.identity.scope.Model.ID = acceptFastPremiumID }},
		{name: "account id", change: func(f *acceptResetFixture) { f.identity.account.AccountID = "successor-account" }},
		{name: "authorization", change: func(f *acceptResetFixture) {
			f.identity.account.Headers = http.Header{"Authorization": {"Bearer rotated-reset-access"}, "ChatGPT-Account-Id": {acceptResetAccountID}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, nil)
			listed := acceptResetList(http.StatusOK, acceptResetListBody)
			fixture.server.list = func(attempt int) acceptResetReply {
				tt.change(fixture)
				return listed(attempt)
			}
			ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

			err := fixture.resets.Run(t.Context(), ui)

			acceptNoError(t, err, "changed scope while loading")
			acceptEqual(t, []string{"The active Codex model or account changed while loading usage limit resets."}, fixture.ui.notices, "changed scope notice")
			acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
		})
	}
}

func TestCodexResetsRunSendsNothingWhenTheAccountChangesBeforeConsuming(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*acceptResetFixture)
	}{
		{name: "session", change: func(f *acceptResetFixture) { f.identity.scope.SessionID = "successor-session" }},
		{name: "model", change: func(f *acceptResetFixture) { f.identity.scope.Model = *acceptFastModel(acceptFastPremiumID) }},
		{name: "account id", change: func(f *acceptResetFixture) { f.identity.account.AccountID = "successor-account" }},
		{name: "authorization", change: func(f *acceptResetFixture) {
			f.identity.account.Headers = http.Header{"Authorization": {"Bearer rotated-reset-access"}, "ChatGPT-Account-Id": {acceptResetAccountID}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, nil)
			ui := acceptResetScript(fixture, func(index int) {
				if index == 1 {
					tt.change(fixture)
				}
			},
				acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
				acceptSelection{choice: ResetActionConfirm, ok: true},
			)

			err := fixture.resets.Run(t.Context(), ui)

			acceptNoError(t, err, "changed account before consuming")
			acceptEqual(t, []string{"The active Codex model or account changed; the reset was not used."}, fixture.ui.notices, "changed account notice")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
			acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID)
		})
	}
}

func TestCodexResetsRunRefusesANonCodexScope(t *testing.T) {
	for _, tt := range []struct {
		name  string
		scope func(context.Context) (FastScope, error)
	}{
		{name: "no selected model", scope: func(context.Context) (FastScope, error) {
			return FastScope{SessionID: acceptResetSession}, nil
		}},
		{name: "other provider", scope: func(context.Context) (FastScope, error) {
			return FastScope{SessionID: acceptResetSession, HasModel: true, Model: CodexModel{Provider: "deepseek", ID: "deepseek-chat"}}, nil
		}},
		{name: "unreadable scope", scope: func(context.Context) (FastScope, error) {
			return FastScope{}, errors.New("raw-provider-canary " + acceptResetToken)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, func(options *acceptResetOptions) { options.scope = tt.scope })
			ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

			err := fixture.resets.Run(t.Context(), ui)

			acceptNoError(t, err, "ineligible scope")
			acceptEqual(t, 1, len(fixture.ui.notices), "notices")
			acceptEqual(t, []string{"error"}, fixture.ui.levels, "notice level")
			acceptNotEqual(t, "", fixture.ui.notices[0], "notice text")
			acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
			acceptResetSafe(t, fixture.ui, acceptResetToken, "raw-provider-canary")
		})
	}
}

func TestCodexResetsRunReportsAnUnverifiableAccount(t *testing.T) {
	fixture := acceptResetNew(t, func(options *acceptResetOptions) {
		options.identify = func(context.Context) (CodexAccount, error) {
			return CodexAccount{}, errors.New("raw-provider-canary " + acceptResetToken + " " + acceptResetAccountID)
		}
	})
	ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "unverifiable account")
	acceptEqual(t, 1, len(fixture.ui.notices), "notices")
	acceptEqual(t, []string{"error"}, fixture.ui.levels, "notice level")
	acceptNotEqual(t, "", fixture.ui.notices[0], "notice text")
	acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, "raw-provider-canary")
}

func TestCodexResetsRunReportsListFailures(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		transport error
	}{
		{name: "server failure", status: http.StatusInternalServerError, body: `{"error":"raw-provider-canary "` + acceptResetToken + `"}`},
		{name: "transport failure", transport: errors.New("raw-provider-canary transport failure")},
		{name: "redirect", status: http.StatusFound, body: `{"available_count":1}`},
		{name: "oversized body", status: http.StatusOK, body: `{"available_count":1}` + strings.Repeat(" ", 8<<20)},
		{name: "malformed json", status: http.StatusOK, body: `{"available_count":`},
		{name: "non object payload", status: http.StatusOK, body: `["synthetic"]`},
		{name: "negative count", status: http.StatusOK, body: `{"available_count":-1,"credits":[]}`},
		{name: "credits not an array", status: http.StatusOK, body: `{"available_count":1,"credits":{}}`},
		{name: "invalid credit id", status: http.StatusOK, body: `{"available_count":1,"credits":[{"id":"","status":"available","reset_type":"codex_rate_limits"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, nil)
			fixture.server.list = func(int) acceptResetReply {
				return acceptResetReply{Status: tt.status, Body: tt.body, Err: tt.transport}
			}
			ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

			err := fixture.resets.Run(t.Context(), ui)

			acceptNoError(t, err, "list failure")
			acceptEqual(t, 1, len(fixture.ui.notices), "notices")
			acceptPrefix(t, fixture.ui.notices[0], "Couldn't load usage limit resets:", "list failure notice")
			acceptEqual(t, []string{"error"}, fixture.ui.levels, "list failure notice level")
			acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
			acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID, "raw-provider-canary", "synthetic")
		})
	}
}

func TestCodexResetsRunReportsNoAvailableResets(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	fixture.server.list = acceptResetList(http.StatusOK, `{"available_count":0,"credits":[]}`)
	ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

	err := fixture.resets.Run(t.Context(), ui)

	acceptNoError(t, err, "no available resets")
	acceptEqual(t, []string{"No usage limit resets are available."}, fixture.ui.notices, "empty notice")
	acceptEqual(t, []string{"info"}, fixture.ui.levels, "empty notice level")
	acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
	acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
}

func TestCodexResetsRunReportsEachOutcome(t *testing.T) {
	for _, tt := range []struct {
		name   string
		body   string
		notice string
		level  string
	}{
		{name: "reset", body: `{"code":"reset","windows_reset":4}`, notice: "Usage reset.", level: "info"},
		{name: "reset without a window count", body: `{"code":"reset"}`, notice: "Usage reset.", level: "info"},
		{name: "reset with a numeric string window count", body: `{"code":"reset","windows_reset":"2"}`, notice: "Usage reset.", level: "info"},
		{name: "already redeemed", body: `{"code":"already_redeemed","windows_reset":0}`, notice: "Usage reset was already completed.", level: "info"},
		{name: "nothing to reset", body: `{"code":"nothing_to_reset"}`, notice: "Your usage does not need a reset right now.", level: "warning"},
		{name: "no credit", body: `{"code":"no_credit"}`, notice: "No usage limit resets are available.", level: "warning"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, nil)
			fixture.server.consume = acceptResetConsume(http.StatusOK, tt.body)
			ui := acceptResetScript(fixture, nil,
				acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
				acceptSelection{choice: ResetActionConfirm, ok: true},
			)

			err := fixture.resets.Run(t.Context(), ui)

			acceptNoError(t, err, "outcome reporting")
			acceptEqual(t, []string{tt.notice}, fixture.ui.notices, "outcome notice")
			acceptEqual(t, []string{tt.level}, fixture.ui.levels, "outcome notice level")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
		})
	}
}

func TestCodexResetsRunRetriesConsumeFailuresWithTheSameRequest(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		transport error
	}{
		{name: "server failure", status: http.StatusInternalServerError, body: `{"error":"raw-provider-canary"}`},
		{name: "transport failure", transport: errors.New("raw-provider-canary transport failure")},
		{name: "redirect", status: http.StatusFound, body: `{"code":"reset"}`},
		{name: "oversized body", status: http.StatusOK, body: `{"code":"reset"}` + strings.Repeat(" ", 8<<20)},
		{name: "malformed json", status: http.StatusOK, body: `{"code":`},
		{name: "unknown code", status: http.StatusOK, body: `{"code":"synthetic_unknown"}`},
		{name: "missing code", status: http.StatusOK, body: `{"windows_reset":1}`},
		{name: "negative window count", status: http.StatusOK, body: `{"code":"reset","windows_reset":-2}`},
		{name: "fractional window count", status: http.StatusOK, body: `{"code":"reset","windows_reset":1.5}`},
		{name: "non numeric window count", status: http.StatusOK, body: `{"code":"reset","windows_reset":"many"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, nil)
			fixture.server.consume = func(int) acceptResetReply {
				return acceptResetReply{Status: tt.status, Body: tt.body, Err: tt.transport}
			}
			ui := acceptResetScript(fixture, nil,
				acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
				acceptSelection{choice: ResetActionConfirm, ok: true},
				acceptSelection{choice: ResetActionBack, ok: true},
			)

			err := fixture.resets.Run(t.Context(), ui)

			acceptNoError(t, err, "abandoned retry")
			acceptEqual(t, 3, len(fixture.ui.titles), "prompts")
			acceptContains(t, fixture.ui.titles[2], "Couldn't reset usage: ", "retry prompt")
			acceptContains(t, fixture.ui.titles[2], "Try again with the same request.", "retry prompt")
			acceptEqual(t, []string{ResetActionRetry, ResetActionBack}, fixture.ui.optionSets[2], "retry options")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
			acceptEqual(t, int32(1), fixture.ids.Load(), "redemption identifiers created")
			acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID, "redemption-synthetic-1", "raw-provider-canary")
		})
	}
}

func TestCodexResetsRunStopsOnCancellation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		index int
	}{
		{name: "while picking", index: 0},
		{name: "at confirmation", index: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetNew(t, nil)
			ctx, cancel := context.WithCancel(t.Context())
			ui := acceptResetScript(fixture, func(index int) {
				if index == tt.index {
					cancel()
				}
			},
				acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
				acceptSelection{choice: ResetActionConfirm, ok: true},
			)

			err := fixture.resets.Run(ctx, ui)

			acceptErrorIs(t, err, context.Canceled, "cancelled run")
			acceptEqual(t, 0, len(fixture.ui.notices), "notices")
			acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
			acceptEqual(t, 0, fixture.ids.Load(), "redemption identifiers created")
		})
	}
}

type acceptResetCodexHost struct {
	*lifecycleHost
	extra map[string]any
}

func (h acceptResetCodexHost) SessionAuth(ctx context.Context, options map[string]any) (map[string]any, error) {
	auth, err := acceptSelectedModelHost{lifecycleHost: h.lifecycleHost}.SessionAuth(ctx, options)
	if err != nil || auth == nil {
		return auth, err
	}
	provider, _ := auth["provider"].(string)
	if provider != acceptFastProvider {
		return auth, nil
	}
	maps.Copy(auth, h.extra)
	return auth, nil
}

func acceptResetCodexHostNew(t *testing.T) acceptResetCodexHost {
	t.Helper()
	base := "https://chatgpt.com/backend-api/codex"
	host := newLifecycleHost(acceptFastProvider, base)
	host.setCredential(acceptFastProvider, syntheticCodexAccess, base)
	runtime := map[string]any{
		"auth":   map[string]any{"apiKey": syntheticCodexAccess, "baseUrl": "https://chatgpt.com", "headers": map[string]*string{"Authorization": new("Bearer " + syntheticCodexAccess), "ChatGPT-Account-Id": new(acceptResetAccountID)}},
		"source": "OAuth",
	}
	credential := map[string]any{"type": "oauth", "access": syntheticCodexAccess, "refresh": "synthetic-refresh", "expires": float64(4000000000), "accountId": acceptResetAccountID}
	return acceptResetCodexHost{lifecycleHost: host, extra: map[string]any{"providerAuth": runtime, "effectiveAuth": runtime, "credential": credential}}
}

func acceptResetCodexOnly(t *testing.T) HostPort {
	t.Helper()
	return acceptResetCodexHostNew(t)
}

func acceptResetFailedCodexHost(t *testing.T) HostPort {
	t.Helper()
	return acceptCanaryHost{lifecycleHost: acceptResetCodexHostNew(t).lifecycleHost}
}

func acceptResetCodexAndDeepSeekHostNew(t *testing.T) HostPort {
	t.Helper()
	host := acceptResetCodexHostNew(t)
	host.setCredential("deepseek", "synthetic-deepseek-access", "https://api.deepseek.com/v1")
	return host
}

func acceptResetDeepSeekHostNew(t *testing.T) HostPort {
	t.Helper()
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	host.setCredential("deepseek", "synthetic-deepseek-access", "https://api.deepseek.com/v1")
	return host
}

func acceptResetMenuResets() *CodexResets {
	return NewCodexResets(CodexResetsOptions{})
}

func acceptResetMenuClient(t *testing.T, requests *atomic.Int32) Client {
	t.Helper()
	codex := subscriptionGood(t, acceptFastProvider)
	return fixtureClient(t, func(w http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/backend-api/wham/usage") {
			requests.Add(1)
			encoded, err := json.Marshal(codex.Payload)
			if err != nil {
				t.Error(err)
				return
			}
			writeQueryFixture(t, w, encoded)
			return
		}
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
}

func acceptResetMenuRun(t *testing.T, host HostPort, resets *CodexResets, selections []acceptSelection) (*acceptUI, *atomic.Int32) {
	t.Helper()
	requests := new(atomic.Int32)
	ui := &acceptUI{interactive: true, script: selections}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: acceptResetMenuClient(t, requests)},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
		Resets:     resets,
	}
	acceptNoError(t, command.Run(t.Context(), ui, ""), "usage menu run")
	return ui, requests
}

func TestUsageCommandOffersCodexResetForASingleCodexOutcome(t *testing.T) {
	for _, tt := range []struct {
		name   string
		host   func(*testing.T) HostPort
		resets func() *CodexResets
		want   []string
	}{
		{
			name:   "single codex outcome",
			host:   acceptResetCodexOnly,
			resets: acceptResetMenuResets,
			want:   []string{acceptActionRefresh, acceptActionAnother, acceptActionAll, UsageActionReset, acceptActionCodex, acceptActionClose},
		},
		{
			name:   "no reset runner",
			host:   acceptResetCodexOnly,
			resets: func() *CodexResets { return nil },
			want:   []string{acceptActionRefresh, acceptActionAnother, acceptActionAll, acceptActionCodex, acceptActionClose},
		},
		{
			name:   "another provider",
			host:   acceptResetDeepSeekHostNew,
			resets: acceptResetMenuResets,
			want:   []string{acceptActionRefresh, acceptActionAnother, acceptActionAll, acceptActionCodex, acceptActionClose},
		},
		{
			name:   "outcome without a report",
			host:   acceptResetFailedCodexHost,
			resets: acceptResetMenuResets,
			want:   []string{acceptActionRefresh, acceptActionAnother, acceptActionAll, acceptActionCodex, acceptActionClose},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ui, _ := acceptResetMenuRun(t, tt.host(t), tt.resets(), []acceptSelection{{ok: false}})

			acceptEqual(t, 1, len(ui.optionSets), "menu prompts")
			acceptEqual(t, tt.want, ui.optionSets[0], "menu options")
		})
	}
}

func TestUsageCommandOmitsCodexResetForSeveralOutcomes(t *testing.T) {
	host := acceptResetCodexAndDeepSeekHostNew(t)

	ui, _ := acceptResetMenuRun(t, host, acceptResetMenuResets(), []acceptSelection{
		{choice: acceptActionAll, ok: true},
		{ok: false},
	})

	acceptEqual(t, 2, len(ui.optionSets), "menu prompts")
	acceptContains(t, ui.titles[1], "OpenAI Codex: ok", "all-provider report")
	acceptContains(t, ui.titles[1], "DeepSeek: ok", "all-provider report")
	acceptEqual(t, []string{acceptActionRefresh, acceptActionAnother, acceptActionAll, acceptActionCodex, acceptActionClose}, ui.optionSets[1], "menu options with several outcomes")
}

func TestUsageCommandRunsCodexResetFromTheMenu(t *testing.T) {
	host := acceptResetCodexOnly(t)
	resets := acceptResetNew(t, nil).resets

	ui, requests := acceptResetMenuRun(t, host, resets, []acceptSelection{
		{choice: UsageActionReset, ok: true},
		{ok: false},
		{ok: false},
	})

	acceptEqual(t, 3, len(ui.titles), "prompts")
	acceptEqual(t, []string{
		acceptResetLabel(t, "Full reset", acceptResetExpires),
		acceptResetLabel(t, "Bonus reset", ""),
		ResetActionBack,
	}, ui.optionSets[1], "reset picker options")
	acceptEqual(t, []string{acceptActionRefresh, acceptActionAnother, acceptActionAll, UsageActionReset, acceptActionCodex, acceptActionClose}, ui.optionSets[2], "menu options after a reset run")
	acceptEqual(t, 0, len(ui.notices), "notices")
	acceptEqual(t, int32(2), requests.Load(), "codex usage requests")
}

func TestUsageCommandEndsTheMenuWhenTheResetIsCancelled(t *testing.T) {
	host := acceptResetCodexOnly(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ui := &acceptResetUI{
		acceptUI: &acceptUI{interactive: true, script: []acceptSelection{
			{choice: UsageActionReset, ok: true},
			{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
			{choice: ResetActionConfirm, ok: true},
		}},
		hook: func(index int) {
			if index == 2 {
				cancel()
			}
		},
	}
	command := UsageCommand{
		Dispatcher: Dispatcher{Resolver: Resolver{Host: host}, Client: acceptResetMenuClient(t, new(atomic.Int32))},
		Settings:   NewSettingsStore(SettingsPath(t.TempDir())),
		Timeout:    acceptQueryTimeout,
		Resets:     acceptResetNew(t, nil).resets,
	}

	err := command.Run(ctx, ui, "")

	acceptErrorIs(t, err, context.Canceled, "cancelled reset from the menu")
	acceptEqual(t, 3, len(ui.titles), "prompts")
	acceptEqual(t, 0, len(ui.notices), "notices")
}
