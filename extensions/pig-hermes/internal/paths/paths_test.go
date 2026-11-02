package paths_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
)

func TestCanonicalResolvesSymlinksWithMissingTails(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "real"), 0o700))
	must(t, os.Symlink("real/missing", filepath.Join(root, "link")))
	tests := []struct{ name, input, want string }{
		{"existing link and missing tail", filepath.Join(root, "link", "tail"), filepath.Join(root, "real", "missing", "tail")},
		{"missing lexical tail", filepath.Join(root, "real", "new", "tail"), filepath.Join(root, "real", "new", "tail")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := paths.Canonical(tt.input)
			must(t, err)
			equal(t, got, tt.want)
		})
	}
}

func TestCanonicalAgreesWithFrozenTypeScriptOnSyntheticPaths(t *testing.T) {
	source := os.Getenv("HERMES_PACKAGE")
	requireFrozenSource(t, source)
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "real"), 0o700))
	must(t, os.Symlink("real", filepath.Join(root, "alias")))
	must(t, os.Symlink("missing", filepath.Join(root, "dangling")))
	inputs := []string{filepath.Join(root, "alias", "new", "tail"), filepath.Join(root, "dangling", "tail"), filepath.Join(root, "missing", "intermediate", "tail")}
	encoded, err := json.Marshal(inputs)
	must(t, err)
	cmd := exec.CommandContext(t.Context(), "node", "--import", "tsx", "--input-type=module", "-e", `import {pathToFileURL} from 'node:url'; import {join} from 'node:path'; const {canonicalStoragePathSync}=await import(pathToFileURL(join(process.argv[1],'src/store/canonical-storage-path.ts')).href); console.log(JSON.stringify(JSON.parse(process.argv[2]).map(p=>canonicalStoragePathSync(p))));`, source, string(encoded))
	output, err := cmd.CombinedOutput()
	must(t, err)
	var expected []string
	must(t, json.Unmarshal(output, &expected))

	for i, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, err := paths.Canonical(input)
			must(t, err)
			equal(t, got, expected[i])
		})
	}
}

func requireFrozenSource(t testing.TB, source string) {
	t.Helper()
	if source == "" {
		t.Skip("set HERMES_PACKAGE to reconstructed frozen public source")
	}
}

func TestCanonicalAllowsFortySymlinks(t *testing.T) {
	root := t.TempDir()
	symlinkChain(t, root, 40)

	got, err := paths.Canonical(filepath.Join(root, "link-0", "tail"))
	must(t, err)
	equal(t, got, filepath.Join(root, "real", "tail"))
}

func TestCanonicalRejectsFortyOneSymlinks(t *testing.T) {
	root := t.TempDir()
	symlinkChain(t, root, 41)

	_, err := paths.Canonical(filepath.Join(root, "link-0", "tail"))
	rejects(t, err)
}

func symlinkChain(t testing.TB, root string, count int) {
	t.Helper()
	for i := 0; i < count-1; i++ {
		must(t, os.Symlink(fmt.Sprintf("link-%d", i+1), filepath.Join(root, fmt.Sprintf("link-%d", i))))
	}
	must(t, os.Symlink("real", filepath.Join(root, fmt.Sprintf("link-%d", count-1))))
}

func TestCanonicalRejectsCyclesAndNonDirectoryComponents(t *testing.T) {
	root := t.TempDir()
	must(t, os.Symlink("cycle", filepath.Join(root, "cycle")))
	must(t, os.WriteFile(filepath.Join(root, "file"), []byte("synthetic"), 0o600))
	tests := []string{filepath.Join(root, "cycle", "tail"), filepath.Join(root, "file", "tail")}
	for _, input := range tests {
		t.Run(filepath.Base(filepath.Dir(input)), func(t *testing.T) {
			_, err := paths.Canonical(input)
			rejects(t, err)
		})
	}
}

func TestResolveSeparatesKnowledgeSettingsAndSupportedSessions(t *testing.T) {
	root := t.TempDir()
	env := paths.Environment{Home: root, Cwd: filepath.Join(root, "work"), AgentRoot: filepath.Join(root, "shared"), PiGAgentRoot: filepath.Join(root, "native"), PiGSessionRoot: filepath.Join(root, "pig-env"), PiSessionRoot: filepath.Join(root, "pi-env"), ClaudeSessionRoot: filepath.Join(root, "claude-env")}
	settings, err := config.Parse([]byte(`{"memoryDir":"knowledge","projectsMemoryDir":"projects","sessionRoots":{"pig":"pig-explicit","pi":"pi-explicit","claudeCode":"claude-explicit"}}`))
	must(t, err)

	got, err := paths.Resolve(settings, env)
	must(t, err)
	equal(t, got.AgentRoot, env.AgentRoot)
	equal(t, paths.NativeAgentRoot(env), env.PiGAgentRoot)
	equal(t, got.Knowledge, filepath.Join(root, "shared", "knowledge"))
	equal(t, got.Database, filepath.Join(root, "shared", "knowledge", "sessions.db"))
	equal(t, got.Projects, filepath.Join(root, "shared", "projects"))
	equal(t, got.Sessions, config.SessionRoots{PiG: filepath.Join(env.Cwd, "pig-explicit"), Pi: filepath.Join(env.Cwd, "pi-explicit"), ClaudeCode: filepath.Join(env.Cwd, "claude-explicit")})
}

func TestResolveDefaultsAndSessionEnvironmentOverrides(t *testing.T) {
	root := t.TempDir()
	settings, err := config.Parse([]byte(`{}`))
	must(t, err)
	env := paths.Environment{Home: root, Cwd: filepath.Join(root, "work")}

	got, err := paths.Resolve(settings, env)
	must(t, err)
	equal(t, got.Knowledge, filepath.Join(root, ".pi", "agent", "pi-hermes-memory"))
	equal(t, paths.NativeAgentRoot(env), filepath.Join(root, ".pig", "agent"))
	equal(t, got.Sessions, config.SessionRoots{PiG: filepath.Join(root, ".pig", "agent", "sessions"), Pi: filepath.Join(root, ".pi", "agent", "sessions"), ClaudeCode: filepath.Join(root, ".claude", "projects")})
	env.PiGSessionRoot = "pig-alt"
	env.PiSessionRoot = "pi-alt"
	overridden, err := paths.Resolve(settings, env)
	must(t, err)
	equal(t, overridden.Sessions, config.SessionRoots{PiG: filepath.Join(env.Cwd, "pig-alt"), Pi: filepath.Join(env.Cwd, "pi-alt"), ClaudeCode: filepath.Join(root, ".claude", "projects")})
}

func TestResolveRejectsProjectRootEscape(t *testing.T) {
	root := t.TempDir()
	env := paths.Environment{Home: root, Cwd: root, AgentRoot: filepath.Join(root, "agent")}
	tests := []string{`{"projectsMemoryDir":"../escape"}`, `{"projectsMemoryDir":"nested/child"}`, `{"projectsMemoryDir":"."}`}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			settings, err := config.Parse([]byte(input))
			must(t, err)
			_, err = paths.Resolve(settings, env)
			rejects(t, err)
		})
	}
}

func TestResolveBindsSessionCwdWithBasenameMigrationBridge(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	sub := filepath.Join(repo, "nested")
	must(t, os.MkdirAll(sub, 0o700))
	must(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o700))
	settings, err := config.Parse([]byte(`{}`))
	must(t, err)
	env := paths.Environment{Home: root, Cwd: sub, AgentRoot: filepath.Join(root, "shared")}
	old := filepath.Join(env.AgentRoot, "projects-memory", "nested")
	must(t, os.MkdirAll(old, 0o700))
	must(t, os.WriteFile(filepath.Join(old, "MEMORY.md"), []byte("synthetic legacy project"), 0o600))

	got, err := paths.Resolve(settings, env)
	must(t, err)
	equal(t, got.ProjectName, "nested")
	equal(t, got.Project, old)
	data, err := os.ReadFile(filepath.Join(got.Project, "MEMORY.md"))
	must(t, err)
	equal(t, string(data), "synthetic legacy project")
	env.Cwd = filepath.Join(root, "second")
	must(t, os.Mkdir(env.Cwd, 0o700))
	must(t, os.MkdirAll(filepath.Join(env.Cwd, ".git"), 0o700))
	second, err := paths.Resolve(settings, env)
	must(t, err)
	equal(t, second.ProjectName, "second")
}

func TestResolveGitSubdirectoryAndLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	sub := filepath.Join(repo, "nested")
	linked := filepath.Join(root, "linked")
	metadata := filepath.Join(repo, ".git", "worktrees", "linked")
	must(t, os.MkdirAll(sub, 0o700))
	must(t, os.MkdirAll(metadata, 0o700))
	must(t, os.MkdirAll(linked, 0o700))
	must(t, os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+metadata+"\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(metadata, "commondir"), []byte("../..\n"), 0o600))
	settings, err := config.Parse([]byte(`{}`))
	must(t, err)
	tests := []struct{ name, cwd string }{{"git subdirectory", sub}, {"linked worktree", linked}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := paths.Resolve(settings, paths.Environment{Home: root, Cwd: tt.cwd, AgentRoot: filepath.Join(root, "agent")})
			must(t, err)
			equal(t, got.ProjectName, "repository")
			equal(t, got.Project, filepath.Join(root, "agent", "projects-memory", "repository"))
		})
	}
}

func TestResolveHomeAndFilesystemRootHaveNoProject(t *testing.T) {
	root := t.TempDir()
	settings, err := config.Parse([]byte(`{}`))
	must(t, err)
	for _, cwd := range []string{root, "/"} {
		t.Run(cwd, func(t *testing.T) {
			got, err := paths.Resolve(settings, paths.Environment{Home: root, Cwd: cwd})
			must(t, err)
			equal(t, got.ProjectName, "")
			equal(t, got.Project, "")
		})
	}
}

func TestResolveUsesLexicalProjectBeforeCanonicalStorage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	must(t, os.MkdirAll(filepath.Join(root, "real"), 0o700))
	must(t, os.MkdirAll(filepath.Join(root, ".pi", "agent", "projects-memory", "alias"), 0o700))
	must(t, os.Symlink("real", filepath.Join(root, "alias")))
	settings, err := config.Parse([]byte(`{"memoryDir":"~/knowledge"}`))
	must(t, err)

	got, err := paths.Resolve(settings, paths.Environment{Home: root, Cwd: filepath.Join(root, "alias")})
	must(t, err)
	equal(t, got.ProjectName, "alias")
	equal(t, got.Knowledge, filepath.Join(root, "knowledge"))
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func rejects(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected rejection")
	}
}
func equal(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}
