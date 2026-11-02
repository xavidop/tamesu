package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	tgrpc "github.com/xavidop/tamesu/targets/grpc"
)

func init() {
	targetFactories[config.TargetGRPC] = newGRPCTarget
}

// newGRPCTarget maps a suite's grpc target onto the grpc module's config.
func newGRPCTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := tgrpc.Config{URL: t.URL, Headers: t.Headers, Timeout: t.TurnTimeout.Std()}
	if t.Session != nil {
		cfg.SessionID = t.Session.ID
	}
	return tgrpc.New(spec.Name, cfg), nil
}
