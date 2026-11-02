package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var errExternalConflict = errors.New("external memory document conflict")
var artifactSuffix = regexp.MustCompile(`^\d+-[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type documentState struct {
	entries     []string
	fingerprint string
	mode        os.FileMode
	size        int64
}

func readDocument(path string) (documentState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return documentState{fingerprint: "missing", mode: 0600}, nil
	}
	if err != nil {
		return documentState{}, err
	}
	if !info.Mode().IsRegular() {
		return documentState{}, errors.New("memory document must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return documentState{}, err
	}
	if !utf8.Valid(raw) {
		return documentState{}, errors.New("memory document contains invalid UTF-8")
	}
	state := documentState{fingerprint: fingerprint(raw), mode: info.Mode().Perm(), size: int64(len(raw))}
	for _, entry := range strings.Split(string(raw), entryDelimiter) {
		if text := trimMemory(entry); text != "" {
			state.entries = append(state.entries, text)
		}
	}
	return state, nil
}

func fingerprint(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func stageDocument(path, content string, mode os.FileMode) (string, error) {
	temp, err := os.MkdirTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(temp, "write.tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", errors.Join(err, os.RemoveAll(temp))
	}
	_, writeErr := io.WriteString(file, content)
	err = errors.Join(writeErr, file.Chmod(mode), file.Sync(), file.Close())
	if err != nil {
		return "", errors.Join(err, os.RemoveAll(temp))
	}
	return temp, nil
}

func publishDocument(path string, before documentState, content string) (published bool, err error) {
	temp, err := stageDocument(path, content, before.mode)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	staged := filepath.Join(temp, "write.tmp")
	current, err := readDocument(path)
	if err != nil {
		return false, err
	}
	if current.fingerprint != before.fingerprint {
		return false, errExternalConflict
	}
	if before.fingerprint == "missing" {
		err = os.Link(staged, path)
		published = err == nil
	} else {
		published, err = displaceAndPublish(path, staged, before)
	}
	if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
		err = errors.Join(errExternalConflict, err)
	}
	if err != nil {
		return published, err
	}
	current, err = readDocument(path)
	if err != nil {
		return published, err
	}
	if current.fingerprint != fingerprint([]byte(content)) {
		return published, errExternalConflict
	}
	return published, pruneMemoryArtifacts(path)
}

func artifactPath(path, kind string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%s-%d-%s", filepath.Base(path), kind, time.Now().UnixMilli(), token)), nil
}

func displaceAndPublish(path, staged string, before documentState) (published bool, err error) {
	recovery, err := artifactPath(path, "recovery")
	if err != nil {
		return false, err
	}
	identity, err := os.Lstat(staged)
	if err != nil {
		return false, err
	}
	if err := os.Rename(path, recovery); err != nil {
		return false, err
	}
	err = verifyDocumentFingerprint(recovery, before.fingerprint)
	if err == nil {
		err = os.Link(staged, path)
		published = err == nil
	}
	if err == nil {
		err = verifyDocumentFingerprint(recovery, before.fingerprint)
	}
	if err == nil {
		return published, nil
	}
	if published {
		return true, errors.Join(err, rollbackPublication(path, recovery, identity))
	}
	return false, errors.Join(err, restoreDisplaced(recovery, path))
}

func verifyDocumentFingerprint(path, expected string) error {
	state, err := readDocument(path)
	if err != nil {
		return err
	}
	if state.fingerprint != expected {
		return errExternalConflict
	}
	return nil
}

func restoreDisplaced(recovery, path string) error {
	err := os.Link(recovery, path)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func rollbackPublication(path, recovery string, identity os.FileInfo) error {
	conflict, err := artifactPath(path, "conflict-local")
	if err != nil {
		return err
	}
	if err := os.Rename(path, conflict); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return restoreDisplaced(recovery, path)
		}
		return err
	}
	moved, err := os.Lstat(conflict)
	if err != nil {
		return err
	}
	if os.SameFile(identity, moved) {
		return restoreDisplaced(recovery, path)
	}
	return restoreDisplaced(conflict, path)
}

type memoryArtifact struct {
	path, kind string
	info       os.FileInfo
}

func memoryArtifacts(path string) ([]memoryArtifact, error) {
	names, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	prefix := "." + filepath.Base(path) + "."
	var artifacts []memoryArtifact
	for _, name := range names {
		kind := artifactKind(strings.TrimPrefix(name.Name(), prefix))
		if !strings.HasPrefix(name.Name(), prefix) || kind == "" {
			continue
		}
		info, err := name.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			artifacts = append(artifacts, memoryArtifact{filepath.Join(filepath.Dir(path), name.Name()), kind, info})
		}
	}
	slices.SortFunc(artifacts, func(a, b memoryArtifact) int { return b.info.ModTime().Compare(a.info.ModTime()) })
	return artifacts, nil
}

func artifactKind(name string) string {
	for _, kind := range []string{"recovery", "retired", "conflict-local"} {
		suffix, ok := strings.CutPrefix(name, kind+"-")
		if !ok {
			continue
		}
		if artifactSuffix.MatchString(suffix) {
			return kind
		}
	}
	return ""
}

func pruneMemoryArtifacts(path string) error {
	artifacts, err := memoryArtifacts(path)
	if err != nil {
		return err
	}
	counts, sizes := map[string]int{}, map[string]int64{}
	now := time.Now()
	for _, artifact := range artifacts {
		keep := artifactWithinBudget(artifact, counts, sizes, now)
		if keep {
			counts[artifact.kind]++
			sizes[artifact.kind] += artifact.info.Size()
			continue
		}
		if artifact.kind == "recovery" {
			if err := retireMemoryArtifact(path, artifact.path); err != nil {
				return err
			}
		} else if err := os.Remove(artifact.path); err != nil {
			return err
		}
	}
	return pruneRetiredArtifacts(path)
}

func artifactWithinBudget(artifact memoryArtifact, counts map[string]int, sizes map[string]int64, now time.Time) bool {
	if counts[artifact.kind] >= 32 || sizes[artifact.kind]+artifact.info.Size() > 64*1024*1024 {
		return false
	}
	age := now.Sub(artifact.info.ModTime())
	if artifact.kind == "recovery" {
		return age <= 7*24*time.Hour || counts[artifact.kind] == 0
	}
	return age <= 30*24*time.Hour
}

func retireMemoryArtifact(path, recovery string) error {
	retired, err := artifactPath(path, "retired")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(recovery)
	if err != nil {
		return err
	}
	temp, err := stageDocument(path, string(raw), 0600)
	if err != nil {
		return err
	}
	err = os.Rename(filepath.Join(temp, "write.tmp"), retired)
	cleanup := os.RemoveAll(temp)
	if err != nil {
		return errors.Join(err, cleanup)
	}
	return errors.Join(cleanup, os.Remove(recovery))
}

func pruneRetiredArtifacts(path string) error {
	artifacts, err := memoryArtifacts(path)
	if err != nil {
		return err
	}
	var count int
	var size int64
	for _, artifact := range artifacts {
		if artifact.kind != "retired" {
			continue
		}
		if count < 32 && size+artifact.info.Size() <= 64*1024*1024 && time.Since(artifact.info.ModTime()) <= 30*24*time.Hour {
			count++
			size += artifact.info.Size()
			continue
		}
		if err := os.Remove(artifact.path); err != nil {
			return err
		}
	}
	return nil
}
