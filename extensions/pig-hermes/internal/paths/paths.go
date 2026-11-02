// Package paths resolves Hermes knowledge identities independently of host settings and session roots.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"
)

type Environment struct {
	Home              string
	Cwd               string
	AgentRoot         string
	PiGAgentRoot      string
	PiGSessionRoot    string
	PiSessionRoot     string
	ClaudeSessionRoot string
}

type Resolved struct {
	AgentRoot   string
	Knowledge   string
	Database    string
	Projects    string
	ProjectName string
	Project     string
	Sessions    config.SessionRoots
}

func Canonical(path string) (string, error) {
	input, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := string(filepath.Separator)
	parts := split(input)
	depth := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		candidate := filepath.Join(current, part)
		state, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			parent, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{parent, part}, parts...)...), nil
		}
		if err != nil {
			return "", err
		}
		if state.Mode()&os.ModeSymlink == 0 {
			current = candidate
			continue
		}
		if depth >= 40 {
			return "", fmt.Errorf("canonical storage path: %w", syscall.ELOOP)
		}
		depth++
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(candidate), target)
		}
		parts = append(split(filepath.Clean(target)), parts...)
		current = string(filepath.Separator)
	}
	return filepath.EvalSymlinks(current)
}

func split(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == filepath.Separator })
}

func expand(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func absolute(path, base, home string) string {
	path = expand(strings.TrimSpace(path), home)
	if !filepath.IsAbs(path) {
		return filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

func AgentRoot(env Environment) (string, error) {
	if !filepath.IsAbs(env.Home) || !filepath.IsAbs(env.Cwd) {
		return "", errors.New("home and session cwd must be absolute")
	}
	root := env.AgentRoot
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(env.Home, ".pi", "agent")
	}
	return absolute(root, env.Cwd, env.Home), nil
}

func Resolve(s config.Settings, env Environment) (Resolved, error) {
	agent, err := AgentRoot(env)
	if err != nil {
		return Resolved{}, err
	}
	knowledge := filepath.Join(agent, "pi-hermes-memory")
	if strings.TrimSpace(s.MemoryDir) != "" {
		knowledge = absolute(s.MemoryDir, agent, env.Home)
	}
	projects, err := projectsRoot(s.ProjectsMemoryDir, agent, env.Home)
	if err != nil {
		return Resolved{}, err
	}
	name, err := projectName(env, projects)
	if err != nil {
		return Resolved{}, err
	}
	result := Resolved{AgentRoot: agent, ProjectName: name}
	locations := []struct {
		target *string
		path   string
	}{
		{&result.Knowledge, knowledge}, {&result.Database, filepath.Join(knowledge, "sessions.db")},
		{&result.Projects, projects},
	}
	for _, location := range locations {
		*location.target, err = Canonical(location.path)
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve knowledge path: %w", err)
		}
	}
	if name != "" {
		result.Project, err = Canonical(filepath.Join(projects, name))
		if err != nil {
			return Resolved{}, err
		}
	}
	result.Sessions, err = sessionRoots(s.SessionRoots, env, agent)
	return result, err
}

func projectsRoot(path, agent, home string) (string, error) {
	normalized := absolute(path, agent, home)
	relative, err := filepath.Rel(agent, normalized)
	if err != nil {
		return "", err
	}
	relative = strings.Trim(relative, "/\\")
	segments := strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' })
	if len(segments) != 1 || segments[0] == "." || segments[0] == ".." {
		return "", errors.New("projectsMemoryDir must be one directory beneath the shared agent root")
	}
	return filepath.Join(agent, relative), nil
}

func NativeAgentRoot(env Environment) string {
	root := env.PiGAgentRoot
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(env.Home, ".pig", "agent")
	}
	return absolute(root, env.Cwd, env.Home)
}

func sessionRoots(roots config.SessionRoots, env Environment, agent string) (config.SessionRoots, error) {
	pigAgent := NativeAgentRoot(env)
	defaults := []struct {
		target             *string
		override, fallback string
	}{
		{&roots.PiG, env.PiGSessionRoot, filepath.Join(pigAgent, "sessions")},
		{&roots.Pi, env.PiSessionRoot, filepath.Join(agent, "sessions")},
		{&roots.ClaudeCode, env.ClaudeSessionRoot, filepath.Join(env.Home, ".claude", "projects")},
	}
	for _, root := range defaults {
		path := *root.target
		if strings.TrimSpace(path) == "" {
			path = root.override
		}
		if strings.TrimSpace(path) == "" {
			path = root.fallback
		}
		canonical, err := Canonical(absolute(path, env.Cwd, env.Home))
		if err != nil {
			return config.SessionRoots{}, fmt.Errorf("resolve session root: %w", err)
		}
		*root.target = canonical
	}
	return roots, nil
}

func projectName(env Environment, projects string) (string, error) {
	cwd := filepath.Clean(env.Cwd)
	if cwd == filepath.Clean(env.Home) || cwd == string(filepath.Separator) {
		return "", nil
	}
	name := filepath.Base(cwd)
	repo, err := repositoryRoot(cwd)
	if err != nil {
		return "", err
	}
	if repo == "" || repo == cwd || repo == filepath.Clean(env.Home) {
		return name, nil
	}
	repoName := filepath.Base(repo)
	repoExists, err := exists(filepath.Join(projects, repoName))
	if err != nil {
		return "", err
	}
	oldExists, err := exists(filepath.Join(projects, name))
	if err != nil {
		return "", err
	}
	if !repoExists && oldExists {
		return name, nil
	}
	return repoName, nil
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func repositoryRoot(dir string) (string, error) {
	for {
		dotGit := filepath.Join(dir, ".git")
		state, err := os.Stat(dotGit)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil && state.IsDir() {
			return dir, nil
		}
		if err == nil && state.Mode().IsRegular() {
			return worktreeRoot(dir, dotGit)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

var gitPointer = regexp.MustCompile(`(?m)^gitdir:\s*(.+)$`)

func worktreeRoot(dir, dotGit string) (string, error) {
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", err
	}
	match := gitPointer.FindStringSubmatch(string(data))
	if match == nil {
		return dir, nil
	}
	gitDir := absolute(match[1], dir, "")
	common, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	commonDir := ""
	if err == nil && strings.TrimSpace(string(common)) != "" {
		commonDir = absolute(string(common), gitDir, "")
	}
	if commonDir == "" && filepath.Base(filepath.Dir(gitDir)) == "worktrees" {
		commonDir = filepath.Dir(filepath.Dir(gitDir))
	}
	if commonDir == "" {
		return dir, nil
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir), nil
	}
	return commonDir, nil
}
