package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/xavidop/mamori"

	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/redact"
)

// BuildInfo describes the running binary; cmd/tamesu sets it at link time.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Streams are the standard streams the commands read and write.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Deps are the injected collaborators of the commands.
type Deps struct {
	// LookupEnv reads the process environment; nil means nothing is set.
	LookupEnv func(name string) (string, bool)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Providers are mamori providers registered for this process only (tests;
	// cmd/tamesu registers the cloud providers through blank imports).
	Providers []mamori.Provider
	// Checks are extra `tamesu doctor` checks: models, Chromium and LiveKit
	// come from the modules that know how to reach them.
	Checks []DoctorCheck
	// NewProvider builds the AI layer for a run (the llm module); nil means
	// this build has no model provider and conversational tests fail with an
	// infrastructure error.
	NewProvider func(ctx context.Context, cfg ProviderConfig) (model.Provider, error)
	// Targets are the target factories by type (rest, livekit, sip, browser)
	// this build links.
	Targets map[string]TargetFactory
	// SIPSetup runs `tamesu sip setup` (the sip module); nil means the
	// command reports that this build has no sip support.
	SIPSetup func(ctx context.Context, req SIPSetupRequest) (SIPSetupResult, error)
}

type app struct {
	info     BuildInfo
	streams  Streams
	deps     Deps
	redactor *redact.Redactor

	// Global flags.
	configPath string
	verbose    bool
	varFlags   []string
	envFile    string
}

// apps maps each root command to its app, so Execute can redact the final
// error message with the secrets the command resolved.
var apps sync.Map

// NewRoot builds the tamesu command tree wired to streams and deps.
func NewRoot(info BuildInfo, streams Streams, deps Deps) *cobra.Command {
	if streams.Out == nil {
		streams.Out = io.Discard
	}
	if streams.Err == nil {
		streams.Err = io.Discard
	}
	if deps.LookupEnv == nil {
		deps.LookupEnv = func(string) (string, bool) { return "", false }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	a := &app{info: info, streams: streams, deps: deps, redactor: redact.New()}
	root := &cobra.Command{
		Use:   "tamesu",
		Short: "Put your web apps and AI agents to the test.",
		Long: "tamesu: Put your web apps and AI agents to the test.\n\n" +
			"tamesu (試す, \"to try / to put to the test\") runs agentic tests, written in YAML or Go,\n" +
			"against web apps and AI agents: browser, REST, LiveKit voice and SIP phone targets,\n" +
			"with one runner, one assertion model and one report format.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageError(fmt.Errorf("unknown command %q for \"tamesu\"", args[0]))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	if streams.In != nil {
		root.SetIn(streams.In)
	}
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })
	pf := root.PersistentFlags()
	pf.StringVar(&a.configPath, "config", "tamesu.yaml", "project configuration file")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "debug logging to stderr")
	pf.StringArrayVar(&a.varFlags, "var", nil, "KEY=VAL for ${VAR} in secret refs and {{vars.KEY}} (repeatable)")
	pf.StringVar(&a.envFile, "env-file", "", "dotenv file read through mamori when an env: secret or ${VAR} is not set")
	root.AddCommand(
		newVersionCmd(info),
		a.newInitCmd(),
		a.newValidateCmd(),
		a.newListCmd(),
		a.newRunCmd(),
		a.newDoctorCmd(),
		a.newSIPCmd(),
	)
	apps.Store(root, a)
	return root
}

// logger writes redacted structured logs to stderr: warnings by default,
// everything with --verbose.
func (a *app) logger() *slog.Logger {
	level := slog.LevelWarn
	if a.verbose {
		level = slog.LevelDebug
	}
	return slog.New(a.redactor.Handler(slog.NewTextHandler(a.streams.Err, &slog.HandlerOptions{Level: level})))
}

// Execute runs root with args and returns the process exit code. An error is
// printed to stderr as one redacted "error: <message>" line, unless the
// command already reported it.
func Execute(ctx context.Context, root *cobra.Command, args []string) int {
	if args == nil {
		args = []string{} // nil would make cobra fall back to os.Args
	}
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) && ee.silent {
		return ee.code
	}
	msg := err.Error()
	if v, ok := apps.Load(root); ok {
		msg = v.(*app).redactor.Redact(msg)
	}
	_, _ = fmt.Fprintf(root.ErrOrStderr(), "error: %s\n", msg)
	return ExitCode(err)
}
