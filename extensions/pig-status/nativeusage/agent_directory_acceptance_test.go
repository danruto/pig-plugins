package nativeusage_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownAgentDirectoryIgnoresTheWorkingDirectorySettingsFile(t *testing.T) {
	const invalidSettings = `{"codexStatusPercentage": "left"}`

	t.Run("working directory settings are ignored", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.WriteFile(hostSettingsFile, []byte(invalidSettings), 0o600); err != nil {
			t.Fatalf("seed the working directory settings file: %v", err)
		}
		host := startWireHostWithHostInfo(t, `{}`, "")

		session := host.exchange(t, "codex-session", sessionStartRequest(), holdNoCall)

		requireNoNotification(t, session.Calls)
	})

	t.Run("agent directory settings are reported", func(t *testing.T) {
		t.Chdir(t.TempDir())
		agentDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(agentDir, hostSettingsFile), []byte(invalidSettings), 0o600); err != nil {
			t.Fatalf("seed the agent directory settings file: %v", err)
		}
		host := startWireHostWithHostInfo(t, `{"agentDir":"`+agentDir+`"}`, agentDir)

		session := host.exchange(t, "codex-session", sessionStartRequest(), holdNoCall)

		requireNotification(t, session.Calls)
	})
}

func TestUnknownAgentDirectoryRefusesSettingsSaves(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	host := startWireHostWithHostInfo(t, `{}`, "")
	host.answers["getModel"] = map[string]any{"id": "deepseek-chat", "provider": "deepseek"}
	host.answers["getModelRegistryState"] = map[string]any{"providers": map[string]any{}}
	host.dialog = wireCompanionToggleChoice

	host.send(t, "usage-command", usageCommandRequest())
	notices := host.awaitSettled(t, "usage-command", func() bool { return settingsFileExists(workingDir) })

	requireNoSettingsFileWritten(t, workingDir)
	requireErrorNotice(t, notices)
}

func wireCompanionToggleChoice(args map[string]any) string {
	options, _ := args["options"].([]any)
	for _, option := range options {
		label, _ := option.(string)
		if strings.Contains(label, "companion") {
			return label
		}
	}
	return ""
}

func requireNoSettingsFileWritten(t *testing.T, root string) {
	t.Helper()
	if path := settingsFilePath(root); path != "" {
		t.Fatalf("a refused save wrote %s", path)
	}
}

func settingsFileExists(root string) bool {
	return settingsFilePath(root) != ""
}

func settingsFilePath(root string) string {
	found := ""
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && found == "" && entry.Name() == hostSettingsFile {
			found = path
		}
		return nil
	})
	return found
}
