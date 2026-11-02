package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type mirrorEntry struct {
	memoryEntry
	category, failureReason, toolState, correctedTo string
}

func parseMirror(raw string, scope Scope, today string) (mirrorEntry, error) {
	entry, err := decodeEntry(raw, today)
	if err != nil {
		return mirrorEntry{}, err
	}
	if scope.Target != "failure" {
		entry.project = scope.Project
	}
	result := mirrorEntry{memoryEntry: entry}
	if scope.Target != "failure" {
		return result, nil
	}
	result.category = failureCategory(entry.text)
	parts := strings.Split(entry.text, " — ")
	for _, part := range parts[1:] {
		fillFailureDetail(part, "Failed: ", &result.failureReason)
		fillFailureDetail(part, "Tool state: ", &result.toolState)
		fillFailureDetail(part, "Corrected to: ", &result.correctedTo)
	}
	return result, nil
}

func failureCategory(text string) string {
	category, _, ok := strings.Cut(strings.TrimPrefix(text, "["), "] ")
	if !strings.HasPrefix(text, "[") || !ok || !validCategory(category) {
		return ""
	}
	return category
}

func fillFailureDetail(part, prefix string, target *string) {
	if *target != "" {
		return
	}
	if value, ok := strings.CutPrefix(part, prefix); ok {
		*target = trimMemory(value)
	}
}

func sharedJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return unescapeJSONSeparators(strings.TrimSuffix(buffer.String(), "\n")), nil
}

func unescapeJSONSeparators(encoded string) string {
	var result strings.Builder
	for i := 0; i < len(encoded); i++ {
		if encoded[i] != '\\' || i+1 >= len(encoded) {
			result.WriteByte(encoded[i])
			continue
		}
		if strings.HasPrefix(encoded[i:], `\u2028`) {
			result.WriteRune('\u2028')
			i += 5
			continue
		}
		if strings.HasPrefix(encoded[i:], `\u2029`) {
			result.WriteRune('\u2029')
			i += 5
			continue
		}
		result.WriteString(encoded[i : i+2])
		i++
	}
	return result.String()
}

func (m *Memory) reconcile(ctx context.Context, scope Scope, raw []string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grouped := map[string][]string{scope.Project: {}}
	if scope.Target == "failure" {
		grouped = map[string][]string{"": {}}
		if err := existingFailureProjects(ctx, tx, grouped); err != nil {
			return err
		}
	}
	for _, text := range raw {
		entry, err := parseMirror(text, scope, m.today())
		if err != nil {
			return err
		}
		grouped[entry.project] = append(grouped[entry.project], text)
	}
	for project, entries := range grouped {
		if err := reconcileScope(ctx, tx, Scope{scope.Target, project}, reconcileInput{entries, m.today()}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func existingFailureProjects(ctx context.Context, tx *sql.Tx, grouped map[string][]string) error {
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT project FROM memories WHERE target='failure'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var project sql.NullString
		if err := rows.Scan(&project); err != nil {
			return err
		}
		grouped[project.String] = []string{}
	}
	return rows.Err()
}

type reconcileInput struct {
	raw   []string
	today string
}

func reconcileScope(ctx context.Context, tx *sql.Tx, scope Scope, input reconcileInput) error {
	retained := map[int64]bool{}
	for _, raw := range input.raw {
		entry, err := parseMirror(raw, scope, input.today)
		if err != nil {
			return err
		}
		id, err := syncMirrorEntry(ctx, tx, scope.Target, entry)
		if err != nil {
			return err
		}
		retained[id] = true
	}
	if err := deleteOrphanRows(ctx, tx, scope, retained); err != nil {
		return err
	}
	key, err := sharedJSON([]any{scope.Target, nullable(scope.Project)})
	if err != nil {
		return err
	}
	if len(retained) == 0 {
		_, err := tx.ExecContext(ctx, "DELETE FROM extension_metadata WHERE key=?", "mdsync:v1:"+key)
		return err
	}
	raw := input.raw
	if raw == nil {
		raw = []string{}
	}
	encoded, err := sharedJSON(raw)
	if err != nil {
		return err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(encoded)))
	state, err := sharedJSON(struct {
		SHA256     string `json:"sha256"`
		EntryCount int    `json:"entryCount"`
	}{fingerprint, len(retained)})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO extension_metadata(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "mdsync:v1:"+key, state)
	return err
}

func syncMirrorEntry(ctx context.Context, tx *sql.Tx, target string, entry mirrorEntry) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM memories WHERE target=? AND project IS ? AND category IS ? AND content=? ORDER BY id LIMIT 1", target, nullable(entry.project), nullable(entry.category), entry.text).Scan(&id)
	if err == sql.ErrNoRows {
		return insertMirrorEntry(ctx, tx, target, entry)
	}
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE memories SET created=min(created,?),last_referenced=max(last_referenced,?),failure_reason=coalesce(failure_reason,?),tool_state=coalesce(tool_state,?),corrected_to=coalesce(corrected_to,?) WHERE id=?`, entry.created, entry.last, nullable(entry.failureReason), nullable(entry.toolState), nullable(entry.correctedTo), id)
	return id, err
}

func insertMirrorEntry(ctx context.Context, tx *sql.Tx, target string, entry mirrorEntry) (int64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO memories(project,target,category,content,failure_reason,tool_state,corrected_to,created,last_referenced) VALUES (?,?,?,?,?,?,?,?,?)`, nullable(entry.project), target, nullable(entry.category), entry.text, nullable(entry.failureReason), nullable(entry.toolState), nullable(entry.correctedTo), entry.created, entry.last)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func deleteOrphanRows(ctx context.Context, tx *sql.Tx, scope Scope, retained map[int64]bool) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM memories WHERE target=? AND project IS ?", scope.Target, nullable(scope.Project))
	if err != nil {
		return err
	}
	var orphanIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		if !retained[id] {
			orphanIDs = append(orphanIDs, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range orphanIDs {
		if _, err := tx.ExecContext(ctx, "DELETE FROM memories WHERE id=?", id); err != nil {
			return err
		}
	}
	return nil
}
