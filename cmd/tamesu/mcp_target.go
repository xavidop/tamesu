package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	mcptarget "github.com/xavidop/tamesu/targets/mcp"
	modeltarget "github.com/xavidop/tamesu/targets/model"
)

func init() {
	targetFactories[config.TargetMCP] = newMCPTarget
}

// newMCPTarget maps a suite's mcp target onto the mcp module's config: the
// server (url or command), and the model that drives its tools.
func newMCPTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := mcptarget.Config{
		URL: t.URL, Headers: t.Headers, Command: t.Command, Args: t.Args, Env: t.Env, Tools: t.AllowTools, CallTimeout: t.CallTimeout.Std(),
		Model: modeltarget.Config{Model: t.Model, Fallback: t.Fallback, System: t.System, DoneTool: t.DoneTool, Temperature: t.Temperature, MaxTokens: t.MaxTokens, MaxToolRounds: t.MaxToolRounds},
	}
	if t.SystemFile != "" {
		path := t.SystemFile
		if !filepath.IsAbs(path) && spec.Suite != nil {
			path = filepath.Join(filepath.Dir(spec.Suite.Path), path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("mcp %s: system_file: %w", spec.Name, err)
		}
		cfg.Model.System = string(b)
	}
	if cfg.Command != "" && !filepath.IsAbs(cfg.Command) && spec.Suite != nil && (len(cfg.Command) > 1 && (cfg.Command[0] == '.' || filepath.Base(cfg.Command) != cfg.Command)) {
		// A relative path is relative to the suite, like system_file.
		cfg.Command = filepath.Join(filepath.Dir(spec.Suite.Path), cfg.Command)
	}
	return mcptarget.New(spec.Name, cfg), nil
}
