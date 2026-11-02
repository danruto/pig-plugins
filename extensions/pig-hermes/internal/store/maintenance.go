package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func recoveryNow() int64 { return time.Now().UnixMilli() }

func recentFailures(failures []int64, now int64, window time.Duration) []int64 {
	result := []int64{}
	cutoff := now - window.Milliseconds()
	for _, failure := range failures {
		if failure >= cutoff {
			result = append(result, failure)
		}
	}
	return result
}

func (d *Database) recoveryFailures() ([]int64, error) {
	path := d.path + ".recovery-state.json"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, errors.New("invalid recovery circuit state")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state struct {
		Failures []int64 `json:"failures"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, errors.New("invalid recovery circuit state")
	}
	if state.Failures == nil {
		return nil, errors.New("missing recovery circuit failures")
	}
	return recentFailures(state.Failures, recoveryNow(), d.options.CircuitWindow), nil
}

func (d *Database) recordFailure(failures []int64) error {
	failures = append(failures, recoveryNow())
	if len(failures) > d.options.CircuitLimit {
		failures = failures[len(failures)-d.options.CircuitLimit:]
	}
	raw, err := json.Marshal(struct {
		Failures []int64 `json:"failures"`
	}{failures})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(d.path), ".recovery-state-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	_, writeErr := file.Write(raw)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(name, d.path+".recovery-state.json")
}

func (d *Database) clearFailures() error {
	err := os.Remove(d.path + ".recovery-state.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type databaseBackup struct {
	name     string
	modified time.Time
	paths    []string
	main     bool
}

func (d *Database) databaseBackups() ([]databaseBackup, error) {
	directory := filepath.Dir(d.path)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	groups := map[string]*databaseBackup{}
	prefix := filepath.Base(d.path) + ".corrupt-"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(entry.Name(), "-wal"), "-shm")
		group := groups[name]
		if group == nil {
			group = &databaseBackup{name: name}
			groups[name] = group
		}
		group.paths = append(group.paths, filepath.Join(directory, entry.Name()))
		group.main = group.main || entry.Name() == name
		if info.ModTime().After(group.modified) {
			group.modified = info.ModTime()
		}
	}
	result := make([]databaseBackup, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	slices.SortFunc(result, func(a, b databaseBackup) int {
		if order := b.modified.Compare(a.modified); order != 0 {
			return order
		}
		return strings.Compare(b.name, a.name)
	})
	return result, nil
}

func (d *Database) pruneDatabaseBackups(ctx context.Context, lease *Lease, current []string, retained int) error {
	backups, err := d.databaseBackups()
	if err != nil {
		return err
	}
	slices.SortStableFunc(backups, func(a, b databaseBackup) int {
		return backupPriority(a, current) - backupPriority(b, current)
	})
	if len(backups) <= retained {
		return nil
	}
	for _, backup := range backups[retained:] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := requireRecoveryOwner(ctx, lease); err != nil {
			return err
		}
		for _, path := range backup.paths {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func backupPriority(backup databaseBackup, current []string) int {
	for index, path := range current {
		if slices.Contains(backup.paths, path) {
			return index
		}
	}
	return len(current)
}

type MaintenanceResult struct {
	StoresMaintained int
	FailedStores     []string
}

func (m *Memory) Maintain(ctx context.Context) (MaintenanceResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := MaintenanceResult{}
	dirs, err := m.maintenanceDirs()
	if err != nil {
		return result, err
	}
	var failures []error
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(err, errors.Join(failures...))
		}
		if err := maintainStore(ctx, dir); err != nil {
			result.FailedStores = append(result.FailedStores, dir)
			failures = append(failures, fmt.Errorf("maintain memory store: %w", err))
			continue
		}
		result.StoresMaintained++
	}
	return result, errors.Join(failures...)
}

func (m *Memory) maintenanceDirs() ([]string, error) {
	dirs := []string{m.paths.Knowledge}
	entries, err := os.ReadDir(m.paths.Projects)
	if errors.Is(err, os.ErrNotExist) {
		return dirs, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "skills" {
			continue
		}
		dirs = append(dirs, filepath.Join(m.paths.Projects, entry.Name()))
	}
	return dirs, nil
}

func maintainStore(ctx context.Context, directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("maintenance store must be a nonsymlink directory")
	}
	for _, name := range []string{"MEMORY.md", "USER.md", "failures.md"} {
		if err := maintainDocument(ctx, filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return nil
}

func maintainDocument(ctx context.Context, document string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	coordination, err := MutationCoordination(document)
	if err != nil {
		return err
	}
	lease, err := coordination.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Release()) }()
	return pruneMemoryArtifactsContext(ctx, document)
}

func restoreFile(source, destination string) (err error) {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("restore source must be a regular file")
	}
	directory, err := os.MkdirTemp(filepath.Dir(destination), ".restore-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	staged := filepath.Join(directory, "database")
	if err := copyFileExclusive(source, staged); err != nil {
		return err
	}
	if err := os.Chmod(staged, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Link(staged, destination)
}

func copyFileExclusive(source, destination string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return copyFileContext(ctx, source, destination)
}

func copyFileContext(ctx context.Context, source, destination string) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	copyErr := copyChunks(ctx, output, input)
	return errors.Join(copyErr, output.Sync(), output.Close())
}

func copyChunks(ctx context.Context, output io.Writer, input io.Reader) error {
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			written, err := output.Write(buffer[:count])
			if err != nil {
				return err
			}
			if written != count {
				return io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
