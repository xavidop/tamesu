package runner

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"time"

	"github.com/xavidop/tamesu/internal/a2aserver"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

// remoteAgents are the suite's remote_agents started for one run: A2A
// servers the agent under test can delegate to, whose messages the run
// records as delegations.
type remoteAgents struct {
	servers map[string]*a2aserver.Server
	names   []string
}

// startRemoteAgents starts one server per remote agent, with the test's
// fuzz for it when fuzz_tools names it; templates in forward and headers
// render with the run's scope.
func startRemoteAgents(specs map[string]config.RemoteAgent, render func(string) (string, error), fuzz map[string]target.ToolFuzz, logger *slog.Logger) (*remoteAgents, error) {
	ra := &remoteAgents{servers: map[string]*a2aserver.Server{}}
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := specs[name]
		cfg, err := remoteConfig(name, spec, render, logger)
		if err != nil {
			ra.close()
			return nil, err
		}
		if f, ok := fuzz[name]; ok {
			ff := f
			cfg.Fuzz = &ff
		}
		srv, err := a2aserver.Start(cfg)
		if err != nil {
			ra.close()
			return nil, err
		}
		ra.servers[name] = srv
		ra.names = append(ra.names, name)
	}
	return ra, nil
}

// remoteConfig maps a suite's remote agent onto the server's config.
func remoteConfig(name string, spec config.RemoteAgent, render func(string) (string, error), logger *slog.Logger) (a2aserver.Config, error) {
	cfg := a2aserver.Config{Addr: spec.Addr, Name: spec.Name, Description: spec.Description, Logger: logger}
	if cfg.Name == "" {
		cfg.Name = name
	}
	for _, sk := range spec.Skills {
		cfg.Skills = append(cfg.Skills, a2aserver.Skill{ID: sk.ID, Name: sk.Name, Description: sk.Description, Tags: sk.Tags})
	}
	var err error
	if cfg.Default.Text, err = render(spec.Reply); err != nil {
		return cfg, fmt.Errorf("remote agent %s: reply: %w", name, err)
	}
	for i, r := range spec.Replies {
		reply := a2aserver.Reply{Data: r.Data, State: r.State}
		if reply.Text, err = render(r.Say); err != nil {
			return cfg, fmt.Errorf("remote agent %s: replies[%d].say: %w", name, i, err)
		}
		if r.When != "" {
			if reply.When, err = regexp.Compile(r.When); err != nil {
				return cfg, fmt.Errorf("remote agent %s: replies[%d].when: %w", name, i, err)
			}
		}
		cfg.Replies = append(cfg.Replies, reply)
	}
	if spec.Forward != "" {
		if cfg.Forward, err = render(spec.Forward); err != nil {
			return cfg, fmt.Errorf("remote agent %s: forward: %w", name, err)
		}
		cfg.ForwardHeaders = map[string]string{}
		for k, v := range spec.Headers {
			rv, err := render(v)
			if err != nil {
				return cfg, fmt.Errorf("remote agent %s: headers.%s: %w", name, k, err)
			}
			cfg.ForwardHeaders[k] = rv
		}
	}
	return cfg, nil
}

// urls maps each agent's name to its URL, for {{remote.<name>.url}}.
func (ra *remoteAgents) urls() map[string]string {
	out := map[string]string{}
	if ra == nil {
		return out
	}
	for name, srv := range ra.servers {
		out[name] = srv.URL()
	}
	return out
}

// delegations lists what the agent under test sent to the remote agents,
// as tool calls of kind delegation, plus one event per message.
func (ra *remoteAgents) delegations() ([]target.ToolCall, []target.Event) {
	if ra == nil {
		return nil, nil
	}
	type timed struct {
		at   time.Time
		call target.ToolCall
		ev   target.Event
	}
	var all []timed
	for _, name := range ra.names {
		for _, d := range ra.servers[name].Delegations() {
			detail := fmt.Sprintf("%s: %s", name, d.Message)
			if d.Forwarded {
				detail += " (forwarded)"
			}
			if d.Fuzzed != "" {
				detail += " (fuzz " + d.Fuzzed + ")"
			}
			call := target.ToolCall{Name: name, Kind: target.ToolCallDelegation, Args: map[string]any{"message": d.Message, "state": d.State}, Result: d.Reply}
			all = append(all, timed{at: d.At, call: call, ev: target.Event{At: d.At, Kind: "delegated", Detail: detail}})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	calls := make([]target.ToolCall, 0, len(all))
	events := make([]target.Event, 0, len(all))
	for _, t := range all {
		calls = append(calls, t.call)
		events = append(events, t.ev)
	}
	return calls, events
}

func (ra *remoteAgents) close() {
	if ra == nil {
		return
	}
	for _, srv := range ra.servers {
		_ = srv.Close()
	}
}

// counterpartSession starts the counterpart target and returns the loop
// hook that makes it play the user, and a close function.
func (r *run) counterpartSession(ctx context.Context, it *item, cp *config.Counterpart, rc *target.RunContext, dir string) (func(ctx context.Context, agentText string, turn int) (string, bool, error), func(context.Context) target.Artifacts, error) {
	cfg, ok := it.suite.Targets[cp.Target]
	if !ok {
		return nil, nil, fmt.Errorf("counterpart target %q is not declared in targets", cp.Target)
	}
	tgt, err := r.o.Targets(ctx, TargetSpec{Name: cp.Target, Config: cfg, Suite: it.suite, Project: r.o.Project, Mode: "text"})
	if err != nil {
		return nil, nil, fmt.Errorf("counterpart %s: %w", cp.Target, err)
	}
	crc := *rc
	crc.TestName = it.test.Name + " (counterpart)"
	crc.Dir = dir
	sess, err := tgt.Start(ctx, &crc)
	if err != nil {
		return nil, nil, fmt.Errorf("start counterpart %s: %w", cp.Target, err)
	}
	hook := func(ctx context.Context, agentText string, turn int) (string, bool, error) {
		if err := sess.Send(ctx, target.UserTurn{Text: agentText, At: time.Now()}); err != nil {
			return "", false, fmt.Errorf("%s turn %d: %w", cp.Target, turn, err)
		}
		a, err := sess.Receive(ctx)
		if err != nil {
			return "", false, fmt.Errorf("%s turn %d: %w", cp.Target, turn, err)
		}
		return a.Text, a.Done, nil
	}
	closeFn := func(ctx context.Context) target.Artifacts {
		arts, _ := sess.Close(ctx)
		return arts
	}
	return hook, closeFn, nil
}
