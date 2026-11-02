package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
)

type Scope struct{ Target, Project string }
type Mutation struct{ Action, OldText, Content, Category, FailureReason, ToolState, CorrectedTo, Project string }
type MutationPlan struct {
	Operations    []Mutation
	RequireShrink bool
}
type MutationResult struct {
	Success             bool
	DocumentSaved       bool
	SearchReconciled    bool
	Cancelled           bool
	Attempts            int
	Code                string
	Warning             string
	RepairAdvice        string
	Entries             []string
	Usage               int
	ReconciliationError error
}

type duplicatePolicy int

const (
	rejectDuplicates duplicatePolicy = iota
	allowDuplicateNoop
)

type Memory struct {
	db       *sql.DB
	database *Database
	settings config.Settings
	paths    paths.Resolved
}

func NewMemory(db *sql.DB, settings config.Settings, resolved paths.Resolved) (*Memory, error) {
	if db == nil {
		return nil, errors.New("memory requires an open knowledge database")
	}
	for _, path := range []string{resolved.Knowledge, resolved.Projects} {
		if !filepath.IsAbs(path) {
			return nil, errors.New("memory roots must be absolute")
		}
	}
	return &Memory{db: db, settings: settings, paths: resolved}, nil
}

func NewManagedMemory(ctx context.Context, database *Database, settings config.Settings, resolved paths.Resolved) (*Memory, error) {
	if database == nil {
		return nil, errors.New("memory requires a database owner")
	}
	var memory *Memory
	err := database.Do(ctx, func(db *sql.DB) error {
		var err error
		memory, err = NewMemory(db, settings, resolved)
		return err
	})
	if err != nil {
		return nil, err
	}
	memory.database = database
	memory.db = nil
	return memory, nil
}

func (m *Memory) Reconcile(ctx context.Context, scope Scope) (result MutationResult, err error) {
	identity, err := m.documentPath(scope)
	if err != nil {
		return result, err
	}
	coordination, err := MutationCoordination(identity)
	if err != nil {
		return result, err
	}
	lease, err := coordination.Acquire(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Release()) }()
	current, err := readDocument(identity)
	if err != nil {
		return result, err
	}
	result.Code = "reconciled"
	result, err = m.finalize(ctx, scope, documentView{identity, current}, result)
	result.Success = result.SearchReconciled
	return result, err
}

func (m *Memory) Add(ctx context.Context, scope Scope, mutation Mutation) (MutationResult, error) {
	mutation.Action = "add"
	return m.apply(ctx, scope, MutationPlan{Operations: []Mutation{mutation}}, allowDuplicateNoop)
}

func (m *Memory) Replace(ctx context.Context, scope Scope, mutation Mutation) (MutationResult, error) {
	mutation.Action = "replace"
	return m.Apply(ctx, scope, MutationPlan{Operations: []Mutation{mutation}})
}

func (m *Memory) Remove(ctx context.Context, scope Scope, mutation Mutation) (MutationResult, error) {
	mutation.Action = "remove"
	return m.Apply(ctx, scope, MutationPlan{Operations: []Mutation{mutation}})
}

func (m *Memory) Apply(ctx context.Context, scope Scope, plan MutationPlan) (MutationResult, error) {
	return m.apply(ctx, scope, plan, rejectDuplicates)
}

func (m *Memory) Reload(ctx context.Context, scope Scope) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := m.documentPath(scope)
	if err != nil {
		return nil, err
	}
	state, err := readDocument(identity)
	return slices.Clone(state.entries), err
}

func (m *Memory) documentPath(scope Scope) (string, error) {
	name := map[string]string{"memory": "MEMORY.md", "user": "USER.md", "failure": "failures.md"}[scope.Target]
	if name == "" {
		return "", errors.New("unsupported memory target")
	}
	root := m.paths.Knowledge
	if scope.Project != "" && scope.Target != "failure" {
		if scope.Target != "memory" {
			return "", errors.New("user profile is global only")
		}
		if !safeProjectName(scope.Project) {
			return "", errors.New("project must be a single nontraversing directory name")
		}
		root = filepath.Join(m.paths.Projects, scope.Project)
		if scope.Project == m.paths.ProjectName && m.paths.Project != "" {
			root = m.paths.Project
		}
	}
	return paths.Canonical(filepath.Join(root, name))
}

func safeProjectName(name string) bool {
	return strings.TrimSpace(name) == name && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func (m *Memory) capacity(scope Scope) int {
	if !m.settings.EnforcesCapacity() {
		return 0
	}
	if scope.Target == "failure" {
		if m.settings.MemoryCharLimit > int(^uint(0)>>1)/2 {
			return int(^uint(0) >> 1)
		}
		return 2 * m.settings.MemoryCharLimit
	}
	if scope.Target == "user" {
		return m.settings.UserCharLimit
	}
	if scope.Project != "" {
		return m.settings.ProjectCharLimit
	}
	return m.settings.MemoryCharLimit
}

func (m *Memory) today() string { return time.Now().UTC().Format("2006-01-02") }

func (m *Memory) apply(ctx context.Context, scope Scope, plan MutationPlan, duplicates duplicatePolicy) (result MutationResult, err error) {
	defer func() { result.Cancelled = ctx.Err() != nil }()
	identity, err := m.documentPath(scope)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Dir(identity), 0700); err != nil {
		return result, err
	}
	coordination, err := MutationCoordination(identity)
	if err != nil {
		return result, err
	}
	lease, err := coordination.Acquire(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		err = errors.Join(err, lease.Release())
		if err != nil && result.DocumentSaved {
			result.Warning = "Document publication occurred before this error; reload disk state before retrying."
			result.RepairAdvice = "Inspect the current document and retained recovery/conflict files; repair search using /memory-sync-markdown with the selected backend."
		}
	}()
	return m.runMutation(ctx, identity, mutationRequest{scope, plan, duplicates == allowDuplicateNoop})
}

func (m *Memory) runMutation(ctx context.Context, identity string, request mutationRequest) (result MutationResult, err error) {
	for attempt := 1; attempt <= 3; attempt++ {
		result.Attempts = attempt
		if ctx.Err() != nil {
			result.Cancelled, result.Code = true, "cancelled"
			return result, ctx.Err()
		}
		current, stepErr := m.attempt(ctx, identity, request, &result)
		if !errors.Is(stepErr, errExternalConflict) {
			return result, stepErr
		}
		if attempt == 3 {
			result.Success, result.Code = false, "conflict"
			result.Warning = "Memory document changed repeatedly; current disk state was reloaded. Inspect retained recovery/conflict files before retrying."
			result.RepairAdvice = "Retry after external edits stop; run /memory-sync-markdown with the selected backend to repair search."
			return m.finalize(ctx, request.scope, documentView{identity, current}, result)
		}
	}
	panic("unreachable memory mutation attempt")
}

type mutationRequest struct {
	scope         Scope
	plan          MutationPlan
	duplicateNoop bool
}

func (m *Memory) attempt(ctx context.Context, identity string, request mutationRequest, result *MutationResult) (documentState, error) {
	before, err := readDocument(identity)
	if err != nil {
		return before, err
	}
	planned, planErr := planEntries(before.entries, planRequest{
		scope: request.scope, plan: request.plan, today: m.today(),
		cap: m.capacity(request.scope), capEnforced: m.settings.EnforcesCapacity(),
	})
	if planErr != nil {
		result.Code = "rejected"
		result.Warning = planErr.Error()
		result.Success = request.duplicateNoop && errors.Is(planErr, errDuplicate)
		finalized, err := m.finalize(ctx, request.scope, documentView{identity, before}, *result)
		*result = finalized
		return before, err
	}
	if err := ctx.Err(); err != nil {
		result.Cancelled, result.Code = true, "cancelled"
		return before, err
	}
	content := strings.Join(planned, entryDelimiter)
	published, publishErr := publishDocument(identity, before, content)
	result.DocumentSaved = result.DocumentSaved || published
	current, readErr := readDocument(identity)
	if readErr != nil {
		return current, errors.Join(publishErr, readErr)
	}
	if publishErr != nil {
		result.Entries, result.Usage = slices.Clone(current.entries), characterCount(strings.Join(current.entries, entryDelimiter))
		return current, publishErr
	}
	result.Success, result.Code = true, "saved"
	expected := documentState{fingerprint: fingerprint([]byte(content))}
	finalized, err := m.finalize(ctx, request.scope, documentView{identity: identity, expected: expected}, *result)
	*result = finalized
	return current, err
}

type documentView struct {
	identity string
	expected documentState
}

func (m *Memory) finalize(ctx context.Context, scope Scope, view documentView, result MutationResult) (MutationResult, error) {
	current, err := readDocument(view.identity)
	if err != nil {
		return result, err
	}
	result.Entries, result.Usage = slices.Clone(current.entries), characterCount(strings.Join(current.entries, entryDelimiter))
	if result.Success && current.fingerprint != view.expected.fingerprint {
		result.Success = false
		return result, errExternalConflict
	}
	err = m.reconcile(ctx, scope, current.entries)
	result.SearchReconciled = err == nil
	result.Cancelled = ctx.Err() != nil
	if err != nil {
		result.ReconciliationError = err
		result.Warning = strings.TrimSpace(result.Warning + " Markdown is authoritative; search reconciliation failed and search may be stale.")
		result.RepairAdvice = "Keep the saved document. After the database is available, run /memory-sync-markdown with the selected backend, or retry this mutation to reconcile from disk."
	}
	return result, nil
}
