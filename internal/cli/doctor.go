package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/redact"
	"github.com/xavidop/tamesu/internal/secrets"
)

// DoctorStatus is the outcome of one doctor check.
type DoctorStatus string

// Doctor statuses.
const (
	StatusOK   DoctorStatus = "ok"
	StatusWarn DoctorStatus = "warn"
	StatusFail DoctorStatus = "fail"
	StatusSkip DoctorStatus = "skip"
)

// DoctorResult is one line of `tamesu doctor`.
type DoctorResult struct {
	Name   string
	Detail string
	Status DoctorStatus
	// Infra marks a failure as an infrastructure problem (exit 3) rather than
	// a configuration one (exit 2).
	Infra bool
}

// DoctorEnv is what a DoctorCheck can look at.
type DoctorEnv struct {
	Project config.Project
	Suites  []*config.Suite
	Secrets *secrets.Store
	Logger  *slog.Logger
}

// DoctorCheck is a check another module contributes (models, Chromium,
// LiveKit). Its results are printed after the built-in ones.
type DoctorCheck func(ctx context.Context, env DoctorEnv) []DoctorResult

// externalChecks are the checks other modules provide; doctor prints a skip
// line for each one no DoctorCheck reported on.
var externalChecks = []string{"models", "speech", "chromium", "livekit", "sip"}

func (a *app) newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor [paths...]",
		Short: "Check the config, suites, secrets, models, speech, Chromium, LiveKit and SIP",
		Long: "Doctor validates tamesu.yaml and the suites, resolves every declared secret\n" +
			"through mamori and reports which link of each chain answered (never the value),\n" +
			"then runs the checks the linked modules provide. Exit 2 when a check fails on\n" +
			"configuration, 3 when a provider or service is unreachable.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			results := a.doctor(cmd.Context(), cmd, args)
			code := ExitOK
			for _, r := range results {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-4s %s: %s\n", r.Status, r.Name, r.Detail)
				if r.Status == StatusFail {
					switch {
					case r.Infra:
						code = ExitInfra
					case code != ExitInfra:
						code = ExitConfig
					}
				}
			}
			if code != ExitOK {
				return silentExit(code)
			}
			return nil
		},
	}
	return cmd
}

func (a *app) doctor(ctx context.Context, cmd *cobra.Command, args []string) []DoctorResult {
	var results []DoctorResult
	add := func(status DoctorStatus, name, detail string) {
		results = append(results, DoctorResult{Name: name, Detail: detail, Status: status})
	}

	// Config.
	p, perrs, err := a.loadProject(cmd, true)
	if err != nil {
		add(StatusFail, "config", err.Error())
		return results
	}
	if _, statErr := os.Stat(a.configPath); errors.Is(statErr, fs.ErrNotExist) {
		add(StatusOK, "config", a.configPath+" not found; using the defaults")
	} else {
		add(StatusOK, "config", fmt.Sprintf("%s (version %d)", a.configPath, p.Version))
	}
	for _, e := range perrs.Sorted() {
		if e.Warning {
			add(StatusWarn, "config", e.Error())
		} else {
			add(StatusFail, "config", e.Error())
		}
	}

	// Suites.
	var suites []*config.Suite
	paths, err := a.suitePaths(args, p)
	if err != nil {
		add(StatusFail, "suites", err.Error())
	} else {
		files, err := a.loadSuites(paths, p, true)
		if err != nil {
			add(StatusFail, "suites", err.Error())
		} else {
			tests := 0
			for _, f := range files {
				for _, e := range f.Errs.Sorted() {
					if e.Warning {
						add(StatusWarn, "suite "+f.Path, e.Error())
					} else {
						add(StatusFail, "suite "+f.Path, e.Error())
					}
				}
				if f.Suite != nil && !f.Errs.HasErrors() {
					suites = append(suites, f.Suite)
					tests += len(f.Suite.Tests)
				}
			}
			add(StatusOK, "suites", fmt.Sprintf("%s, %s", plural(len(files), "file"), plural(tests, "test")))
		}
	}

	// Secrets: every declared ref, project and suites, resolved one by one.
	refs := map[string]string{}
	for k, v := range p.Secrets {
		refs[k] = v
	}
	vars := map[string]string{}
	for _, s := range suites {
		for k, v := range s.Secrets {
			refs[k] = v
		}
		for k, v := range s.Vars {
			vars[k] = v
		}
	}
	flagVars, err := a.refVars(nil)
	if err != nil {
		add(StatusFail, "secrets", err.Error())
	}
	for k, v := range flagVars {
		vars[k] = v
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	store := secrets.NewStore(nil)
	resolved := map[string]struct{}{}
	for _, name := range names {
		one, err := secrets.Resolve(ctx, map[string]string{name: refs[name]}, secrets.Options{RefVars: vars, EnvFile: a.envFile, Providers: a.deps.Providers})
		if err != nil {
			results = append(results, DoctorResult{Name: "secret " + name, Detail: err.Error(), Status: StatusFail, Infra: secrets.IsInfra(err)})
			continue
		}
		src, _ := one.Source(name)
		v, _ := one.Get(name)
		if a.redactor.Add(name, v.Reveal()) {
			add(StatusOK, "secret "+name, fmt.Sprintf("%s (link %d of %d)", src.Scheme, src.Link, src.Links))
		} else {
			add(StatusWarn, "secret "+name, fmt.Sprintf("%s (link %d of %d): resolved but too short to redact (under %d bytes)", src.Scheme, src.Link, src.Links, redact.MinLength))
		}
		resolved[name] = struct{}{}
		store = mergeStores(store, one)
	}
	if len(names) == 0 {
		add(StatusOK, "secrets", "none declared")
	}

	// Checks from the linked modules.
	env := DoctorEnv{Project: p, Suites: suites, Secrets: store, Logger: a.logger()}
	reported := map[string]bool{}
	for _, check := range a.deps.Checks {
		for _, r := range check(ctx, env) {
			reported[r.Name] = true
			results = append(results, r)
		}
	}
	for _, name := range externalChecks {
		if !reported[name] {
			add(StatusSkip, name, "not checked: the module that knows how to is not in this build")
		}
	}
	return results
}

func mergeStores(dst, src *secrets.Store) *secrets.Store {
	values := map[string]string{}
	for _, name := range dst.Names() {
		v, _ := dst.Reveal(name)
		values[name] = v
	}
	for _, name := range src.Names() {
		v, _ := src.Reveal(name)
		values[name] = v
	}
	return secrets.NewStoreFromStrings(values)
}
