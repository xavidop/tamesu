package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/retell"
	"github.com/xavidop/tamesu/targets/vapi"
)

func init() {
	targetFactories[config.TargetVapi] = newVapiTarget
	targetFactories[config.TargetRetell] = newRetellTarget
}

// newVapiTarget maps a suite's vapi target onto the vapi module's config.
func newVapiTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	return vapi.New(spec.Name, vapi.Config{
		Connect: connectPolicy(t),
		BaseURL: t.BaseURL, APIKey: t.APIKey, AssistantID: t.AssistantID, Assistant: t.Assistant, SquadID: t.SquadID,
		AssistantOverrides: t.AssistantOverrides, Variables: t.Variables, Metadata: t.Metadata,
		SampleRate: t.SampleRate, Idle: t.Idle.Std(), Timeout: t.TurnTimeout.Std(),
	}), nil
}

// newRetellTarget maps a suite's retell target onto the retell module's config.
func newRetellTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	return retell.New(spec.Name, retell.Config{
		Connect: connectPolicy(t),
		BaseURL: t.BaseURL, APIKey: t.APIKey, AgentID: t.AgentID, AgentVersion: t.AgentVersion, Variables: t.Variables, Metadata: t.Metadata,
		LiveKitURL: t.LiveKitURL, JoinTimeout: t.JoinTimeout.Std(), Idle: t.Idle.Std(), Timeout: t.TurnTimeout.Std(),
	}), nil
}

var (
	_ cli.TargetFactory = newVapiTarget
	_ cli.TargetFactory = newRetellTarget
)
