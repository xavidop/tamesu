package main

import (
	"context"
	"fmt"
	"os"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/pinned"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/browser"
	"github.com/xavidop/tamesu/targets/desktop"
)

func init() {
	targetFactories[config.TargetDesktop] = newDesktopTarget
}

// newDesktopTarget maps a suite's desktop target onto the desktop module's config.
func newDesktopTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := desktop.Config{App: t.App}
	if t.Desktop != nil {
		cfg.Engine, cfg.Args = t.Desktop.Engine, t.Desktop.Args
	}
	return desktop.New(spec.Name, cfg), nil
}

// checkAgentEngines is the `tamesu doctor` check for the engines that are
// CLIs of their own: agent-browser (browser.engine) and agent-desktop (the
// desktop target). Each is reported only when a suite uses it: which binary
// a run would use and its version against the one tamesu pins, and for
// agent-desktop the system permissions it has. Nothing is downloaded here.
func checkAgentEngines(ctx context.Context, env cli.DoctorEnv) []cli.DoctorResult {
	var usesBrowser, usesDesktop bool
	for _, s := range env.Suites {
		for _, t := range s.Targets {
			switch {
			case t.Type == config.TargetBrowser && t.Browser != nil && t.Browser.Engine == browser.EngineAgentBrowser:
				usesBrowser = true
			case t.Type == config.TargetDesktop:
				usesDesktop = true
			}
		}
	}
	var out []cli.DoctorResult
	if usesBrowser {
		r, _ := checkPinned(ctx, pinned.AgentBrowser)
		out = append(out, r)
	}
	if usesDesktop {
		r, path := checkPinned(ctx, pinned.AgentDesktop)
		out = append(out, r)
		if path != "" {
			out = append(out, checkDesktopPermissions(ctx, path)...)
		}
	}
	return out
}

// checkPinned reports the binary a run would use for a pinned tool, and
// returns its path when there is one.
func checkPinned(ctx context.Context, name string) (cli.DoctorResult, string) {
	tool, err := pinned.Lookup(name)
	if err != nil {
		return cli.DoctorResult{Name: name, Status: cli.StatusFail, Detail: err.Error()}, ""
	}
	found, err := tool.Find(ctx, pinned.Options{NoDownload: true})
	if err != nil {
		if os.Getenv(tool.EnvVar()) != "" {
			return cli.DoctorResult{Name: name, Status: cli.StatusFail, Detail: err.Error()}, ""
		}
		if _, ok := tool.Assets[pinned.Platform()]; !ok {
			return cli.DoctorResult{Name: name, Status: cli.StatusFail, Infra: true, Detail: err.Error()}, ""
		}
		return cli.DoctorResult{Name: name, Status: cli.StatusOK, Detail: fmt.Sprintf("%s %s is not in the cache yet; tamesu run downloads it and checks it against its pinned SHA-256 (or set %s to a binary of your own)", name, tool.Version, tool.EnvVar())}, ""
	}
	if found.Pinned {
		return cli.DoctorResult{Name: name, Status: cli.StatusOK, Detail: fmt.Sprintf("%s (%s)", tool.Version, found.Path)}, found.Path
	}
	version, err := pinned.VersionOf(ctx, found.Path)
	switch {
	case err != nil:
		return cli.DoctorResult{Name: name, Status: cli.StatusFail, Detail: fmt.Sprintf("%s=%s does not run: %v", tool.EnvVar(), found.Path, err)}, ""
	case version != tool.Version:
		return cli.DoctorResult{Name: name, Status: cli.StatusWarn, Detail: fmt.Sprintf("%s (%s, from %s); tamesu is tested with %s", version, found.Path, tool.EnvVar(), tool.Version)}, found.Path
	}
	return cli.DoctorResult{Name: name, Status: cli.StatusOK, Detail: fmt.Sprintf("%s (%s, from %s)", version, found.Path, tool.EnvVar())}, found.Path
}

// checkDesktopPermissions reports what the system lets agent-desktop do.
func checkDesktopPermissions(ctx context.Context, path string) []cli.DoctorResult {
	const name = "desktop permissions"
	perms, err := desktop.Permissions(ctx, path)
	if err != nil {
		return []cli.DoctorResult{{Name: name, Status: cli.StatusFail, Infra: true, Detail: err.Error()}}
	}
	if p := perms["accessibility"]; p.State == "denied" {
		return []cli.DoctorResult{{Name: name, Status: cli.StatusFail, Infra: true, Detail: "Accessibility is denied, so the desktop cannot be read. " + p.Suggestion}}
	}
	if p := perms["screen_recording"]; p.State == "denied" {
		return []cli.DoctorResult{{Name: name, Status: cli.StatusWarn, Detail: "Screen Recording is denied: tests run, without screenshots. " + p.Suggestion}}
	}
	return []cli.DoctorResult{{Name: name, Status: cli.StatusOK, Detail: "the desktop can be read and captured"}}
}
