package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/voiceflow"
)

func init() {
	targetFactories[config.TargetVoiceflow] = newVoiceflowTarget
}

// newVoiceflowTarget maps a suite's voiceflow target onto the voiceflow module's config.
func newVoiceflowTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	return voiceflow.New(spec.Name, voiceflow.Config{
		Connect: connectPolicy(t),
		BaseURL: t.BaseURL, APIKey: t.APIKey, ProjectID: t.ProjectID, Environment: t.Environment, Version: t.Version, VersionID: t.VersionID,
		UserID: t.UserID, Variables: t.Variables, Reset: t.Reset, Timeout: t.TurnTimeout.Std(),
	}), nil
}

var _ cli.TargetFactory = newVoiceflowTarget
