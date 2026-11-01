package main

import (
	"context"
	"os/exec"
	"strings"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/browser"
)

func init() {
	targetFactories[config.TargetBrowser] = newBrowserTarget
}

// newBrowserTarget maps a suite's browser target onto the browser module's config.
func newBrowserTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := browser.Config{URL: t.URL, AllowedDomains: t.AllowedDomains, Headed: spec.Headed}
	if t.Browser != nil {
		cfg.Viewport, cfg.Locale = t.Browser.Viewport, t.Browser.Locale
	}
	if t.Auth != nil {
		cfg.StorageState = t.Auth.StorageState
	}
	return browser.New(spec.Name, cfg), nil
}

// recordBrowser is `tamesu record browser`.
func recordBrowser(ctx context.Context, req cli.RecordRequest) (cli.RecordResult, error) {
	steps, err := browser.Record(ctx, req.URL, browser.RecordOptions{Log: req.Log})
	res := cli.RecordResult{Steps: steps}
	if len(steps) > 0 {
		res.YAML = browser.StepsYAML(steps)
	}
	return res, err
}

// checkChromium is the `tamesu doctor` check for the browser target: a
// Chromium binary is found (no download during doctor) and reports a version.
func checkChromium(ctx context.Context, env cli.DoctorEnv) []cli.DoctorResult {
	used := 0
	for _, s := range env.Suites {
		for _, t := range s.Targets {
			if t.Type == config.TargetBrowser {
				used++
			}
		}
	}
	path, err := browser.FindChromium(ctx, "", false)
	if err != nil {
		status := cli.StatusWarn
		if used > 0 {
			status = cli.StatusFail
		}
		return []cli.DoctorResult{{Name: "chromium", Status: status, Infra: used > 0, Detail: err.Error() + "; tamesu run downloads Chrome for Testing into ~/.cache/tamesu when needed"}}
	}
	detail := path
	out, verr := exec.CommandContext(ctx, path, "--version").Output()
	if verr == nil && len(out) > 0 {
		detail = strings.TrimSpace(string(out)) + " (" + path + ")"
	}
	if used == 0 {
		detail += "; no browser target configured"
	}
	return []cli.DoctorResult{{Name: "chromium", Status: cli.StatusOK, Detail: detail}}
}
