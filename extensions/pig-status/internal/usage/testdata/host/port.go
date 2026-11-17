package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	"github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

type hostPort struct{ usage.HostPort }

func pauseBoundary(ctx context.Context, kind string, provider any) error {
	root := os.Getenv("PIG_FIXTURE_ROOT")
	path := filepath.Join(root, "pause.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var pause struct{ Kind, Provider string }
	if err := json.Unmarshal(data, &pause); err != nil {
		return err
	}
	if pause.Kind != kind || (pause.Provider != "" && pause.Provider != provider) {
		return nil
	}
	if err := os.Rename(path, filepath.Join(root, "paused.json")); err != nil {
		return err
	}
	record(map[string]any{"event": "boundary-paused", "kind": kind})
	for {
		if _, err := os.Stat(filepath.Join(root, "resume")); err == nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func (p hostPort) Metadata(ctx context.Context) (usage.HostMetadata, error) {
	metadata, err := p.HostPort.Metadata(ctx)
	if err != nil {
		return usage.HostMetadata{}, err
	}
	if err := pauseBoundary(ctx, "metadata", ""); err != nil {
		return usage.HostMetadata{}, err
	}
	return metadata, nil
}

func (p hostPort) SessionAuth(ctx context.Context, options map[string]any) (map[string]any, error) {
	result, err := p.HostPort.SessionAuth(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := pauseBoundary(ctx, "auth", result["provider"]); err != nil {
		return nil, err
	}
	return result, nil
}

func port(call sdk.Context) usage.HostPort { return hostPort{HostPort: nativeusage.Port(call)} }
