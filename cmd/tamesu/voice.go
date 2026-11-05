package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/voice"
)

func init() {
	targetFactories[config.TargetVoice] = newVoiceTarget
}

// newVoiceTarget maps a suite's voice target onto the voice module's config.
func newVoiceTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := voice.Config{Connect: connectPolicy(t), URL: t.URL, Headers: t.Headers, Idle: t.Idle.Std(), Timeout: t.TurnTimeout.Std()}
	if t.Audio != nil {
		cfg.Audio = voice.Audio{Encoding: t.Audio.Encoding, SampleRate: t.Audio.SampleRate, Frames: t.Audio.Frames, Send: t.Audio.Send, Receive: t.Audio.Receive}
	}
	if m := t.Messages; m != nil {
		cfg.Messages = voice.Messages{Open: m.Open, Transcript: m.Transcript, Done: m.Done, Interrupted: m.Interrupted, ToolCalls: m.ToolCalls, DTMF: m.DTMF, Close: m.Close}
	}
	// session.open is performed by the runner for every target type; its
	// captures reach the target as {{session.NAME}} through Render.
	return voice.New(spec.Name, cfg), nil
}

var _ cli.TargetFactory = newVoiceTarget
