package store

import (
	"encoding/base64"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const entryDelimiter = "\n§\n"

var entryMetadata = regexp.MustCompile(`(?s)^(.*?)\s*<!--\s*created=([^,]+),\s*last=([^,>]+)(?:,\s*project64=([A-Za-z0-9_-]+))?\s*-->\s*$`)
var lookupScope = regexp.MustCompile(`^\S+\s+scope=(?:global|project:[^\s]+)\s+\[target=(?:memory|user|project|failure)\]\s+`)
var lookupLegacy = regexp.MustCompile(`^\S+\s+\[[^\]]+\]\s+`)
var lookupTarget = regexp.MustCompile(`^\[target=(?:memory|user|project|failure)\]\s+`)

type memoryEntry struct{ text, created, last, project string }

func decodeEntry(raw, today string) (memoryEntry, error) {
	match := entryMetadata.FindStringSubmatch(raw)
	if match == nil {
		return memoryEntry{text: trimMemory(raw), created: today, last: today}, nil
	}
	project, err := base64.RawURLEncoding.DecodeString(match[4])
	if err != nil || !utf8.Valid(project) {
		return memoryEntry{}, errors.New("invalid memory project metadata")
	}
	return memoryEntry{trimMemory(match[1]), trimMemory(match[2]), trimMemory(match[3]), trimMemory(string(project))}, nil
}

func (e memoryEntry) encode() string {
	project := ""
	if e.project != "" {
		project = ", project64=" + base64.RawURLEncoding.EncodeToString([]byte(e.project))
	}
	return e.text + " <!-- created=" + e.created + ", last=" + e.last + project + " -->"
}

func lookupText(text string) string {
	for _, line := range strings.Split(trimMemory(text), "\n") {
		if trimMemory(line) != "" {
			text = trimMemory(line)
			break
		}
	}
	text = lookupScope.ReplaceAllString(text, "")
	text = lookupLegacy.ReplaceAllString(text, "")
	text = lookupTarget.ReplaceAllString(text, "")
	if end := strings.Index(text, "]"); strings.HasPrefix(text, "[") && end > 0 {
		tag := text[:end+1]
		if rest, ok := strings.CutPrefix(trimMemory(text[end+1:]), tag); ok {
			text = tag + " " + trimMemory(rest)
		}
	}
	return trimMemory(text)
}

func trimMemory(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
			return true
		}
		return r >= '\u2000' && r <= '\u200a'
	})
}

func characterCount(text string) int { return len(utf16.Encode([]rune(text))) }

func validCategory(category string) bool {
	return slices.Contains([]string{"failure", "correction", "insight", "preference", "convention", "tool-quirk"}, category)
}

func mutationText(op Mutation, target string) (string, error) {
	text := trimMemory(op.Content)
	if text == "" || !utf8.ValidString(text) {
		return "", errors.New("content must be nonempty UTF-8")
	}
	if op.Category != "" && (target != "failure" || !validCategory(op.Category)) {
		return "", errors.New("category requires a supported failure category")
	}
	if target == "failure" && op.Category != "" {
		text = "[" + op.Category + "] " + text
	}
	for _, detail := range []struct{ label, value string }{{"Failed: ", op.FailureReason}, {"Tool state: ", op.ToolState}, {"Corrected to: ", op.CorrectedTo}} {
		if detail.value != "" {
			text += " — " + detail.label + detail.value
		}
	}
	if !utf8.ValidString(text) {
		return "", errors.New("content and details must be UTF-8")
	}
	return text, scanContent(text)
}

func matchesFor(entries []string, target, old, today string) ([]int, error) {
	var matches []int
	texts, scopes := map[string]bool{}, map[string]bool{}
	for i, raw := range entries {
		entry, err := decodeEntry(raw, today)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(entry.text, old) {
			continue
		}
		matches = append(matches, i)
		texts[entry.text], scopes[entry.project] = true, true
	}
	if len(matches) == 0 {
		return nil, errors.New("no entry matched")
	}
	if len(matches) > 1 && !(target == "failure" && len(texts) == 1 && len(scopes) == len(matches)) {
		return nil, errors.New("ambiguous match; use a more specific complete entry")
	}
	return matches, nil
}

func validateReplacement(entry, old, replacement string) error {
	lines := strings.FieldsFunc(entry, func(r rune) bool { return r == '\n' })
	if len(lines) < 2 {
		return nil
	}
	for _, line := range lines {
		line = trimMemory(line)
		if line != "" && !strings.Contains(line, old) && !strings.Contains(replacement, line) {
			return errors.New("replacement omits sibling lines; replace the whole entry or split it first")
		}
	}
	return nil
}

func planEntries(entries []string, request planRequest) ([]string, error) {
	planned := uniqueEntries(entries)
	if len(request.plan.Operations) == 0 {
		return nil, errors.New("mutation plan requires at least one operation")
	}
	for _, op := range request.plan.Operations {
		var err error
		planned, err = applyOperation(planned, op, request)
		if err != nil {
			return nil, err
		}
	}
	total := characterCount(strings.Join(planned, entryDelimiter))
	if (request.capEnforced || request.cap > 0) && total > request.cap {
		return nil, errors.New("memory capacity exceeded; shorten or remove entries first")
	}
	if request.plan.RequireShrink && total >= characterCount(strings.Join(entries, entryDelimiter)) {
		return nil, errors.New("mutation plan must shrink the target")
	}
	return planned, nil
}

type planRequest struct {
	scope       Scope
	plan        MutationPlan
	today       string
	cap         int
	capEnforced bool
}

func applyOperation(entries []string, op Mutation, request planRequest) ([]string, error) {
	if op.Action == "add" {
		return appendOperation(entries, op, request)
	}
	if op.Action != "replace" && op.Action != "remove" {
		return nil, errors.New("unsupported mutation action")
	}
	old := lookupText(op.OldText)
	if old == "" {
		return nil, errors.New("old text must be nonempty")
	}
	matches, err := matchesFor(entries, request.scope.Target, old, request.today)
	if err != nil {
		return nil, err
	}
	text := ""
	if op.Action == "replace" {
		text, err = mutationText(op, request.scope.Target)
		if err != nil {
			return nil, err
		}
	}
	return replaceOrRemove(entries, matches, op.Action, replacementRequest{old, text, request.today})
}

type replacementRequest struct{ old, text, today string }

func replaceOrRemove(entries []string, matches []int, action string, request replacementRequest) ([]string, error) {
	var result []string
	for i, raw := range entries {
		if !slices.Contains(matches, i) {
			result = append(result, raw)
			continue
		}
		if action == "remove" {
			continue
		}
		entry, err := decodeEntry(raw, request.today)
		if err != nil {
			return nil, err
		}
		if err := validateReplacement(entry.text, request.old, request.text); err != nil {
			return nil, err
		}
		entry.text, entry.last = request.text, request.today
		result = append(result, entry.encode())
	}
	return result, nil
}

func appendOperation(entries []string, op Mutation, request planRequest) ([]string, error) {
	text, err := mutationText(op, request.scope.Target)
	if err != nil {
		return nil, err
	}
	project := trimMemory(op.Project)
	if request.scope.Target == "failure" && project == "" {
		project = trimMemory(request.scope.Project)
	}
	if request.scope.Target != "failure" && project != "" {
		return nil, errors.New("entry project attribution is only accepted for failures; select a document scope instead")
	}
	if !utf8.ValidString(project) {
		return nil, errors.New("project attribution must be UTF-8")
	}
	if err := scanContent(project); err != nil {
		return nil, err
	}
	if err := checkDuplicateEntry(entries, request, memoryEntry{text: text, project: project}); err != nil {
		return nil, err
	}
	entry := memoryEntry{text, request.today, request.today, project}
	return append(entries, entry.encode()), nil
}

func checkDuplicateEntry(entries []string, request planRequest, candidate memoryEntry) error {
	for _, raw := range entries {
		entry, err := decodeEntry(raw, request.today)
		if err != nil {
			return err
		}
		if entry.text == candidate.text && (request.scope.Target != "failure" || entry.project == candidate.project) {
			return errDuplicate
		}
	}
	return nil
}

func uniqueEntries(entries []string) []string {
	seen := map[string]bool{}
	var unique []string
	for _, entry := range entries {
		if seen[entry] {
			continue
		}
		seen[entry] = true
		unique = append(unique, entry)
	}
	return unique
}

var errDuplicate = errors.New("duplicate memory entry")
