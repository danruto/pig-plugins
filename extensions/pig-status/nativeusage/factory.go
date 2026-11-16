package nativeusage

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	"github.com/danruto/pig-plugins/extensions/pig-status/status"
)

func Extension() *sdk.Extension {
	ext, err := status.New(status.Options{Collect: collect})
	if err != nil {
		panic(err)
	}
	return ext
}

func collect(ext *sdk.Extension, state *status.State) error {
	path := settingsPath()
	settings := usage.NewSettingsStore(path)
	repaint := func(ctx sdk.Context) { _ = state.Install(ctx) }
	runtime, err := Register(ext, Options{Client: usage.Client{}, Settings: loadCollection(path), Publish: state.Quota, Repaint: repaint})
	if err != nil {
		return err
	}
	for _, event := range []string{"session_start", "turn_end", "agent_end", "model_select", "session_tree", "session_compact"} {
		ext.OnEvent(event, func(ctx sdk.Context, _ map[string]any) (any, error) {
			entries, err := ctx.GetEntries()
			if err != nil {
				return nil, err
			}
			state.Tokens(SessionTokens(entries, ctx.ModelQualified()))
			return nil, state.Install(ctx)
		})
	}
	registerCommands(ext, runtime, settings)
	return nil
}

func settingsPath() string {
	dir, err := sdk.AgentDir()
	if err != nil {
		return usage.SettingsFileName
	}
	return usage.SettingsPath(dir)
}

func loadCollection(path string) usage.CollectionSettings {
	state, err := usage.LoadSettings(context.Background(), path)
	if err != nil {
		return usage.CollectionSettings{}
	}
	return state.Settings.Collection()
}

func SessionTokens(entries []json.RawMessage, model string) status.Tokens {
	tokens := status.Tokens{Subscription: strings.HasPrefix(model, "claude-bridge/")}
	for _, entry := range entries {
		var message sessionMessage
		if err := json.Unmarshal(entry, &message); err != nil {
			continue
		}
		if message.Type != "message" || message.Message.Role != "assistant" {
			continue
		}
		used := message.Message.Usage
		tokens.Input += used.Input
		tokens.Output += used.Output
		tokens.CacheRead += used.CacheRead
		tokens.CacheWrite += used.CacheWrite
		tokens.Cost += used.Cost.Total
	}
	return tokens
}

type sessionMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role  string `json:"role"`
		Usage struct {
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cacheRead"`
			CacheWrite int `json:"cacheWrite"`
			Cost       struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}
