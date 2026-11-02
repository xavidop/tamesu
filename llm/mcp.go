package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/mcp"
)

// MCPOptions configure ServeMCP: the command backend the tools call and the
// schema they hand out.
type MCPOptions struct {
	Version string
	// Exec runs a tamesu command (its arguments, without the binary) and
	// returns its standard output, standard error and exit code.
	Exec func(ctx context.Context, args []string) (stdout, stderr string, code int)
	// Schema returns the JSON Schema of a suite ("suite") or of tamesu.yaml
	// ("config").
	Schema func(which string) (string, error)
	Logger *slog.Logger
}

// ServeMCP serves the tamesu tools to a coding agent over stdio with the
// Model Context Protocol, on a Genkit instance of its own with no model
// plugins (the tools run tamesu commands; the commands bring their own
// models). It returns when the client closes the connection.
func ServeMCP(ctx context.Context, o MCPOptions) error {
	if o.Exec == nil {
		return errors.New("llm: ServeMCP needs Exec")
	}
	g, err := initBareGenkit(ctx)
	if err != nil {
		return err
	}
	DefineMCPTools(g, o)
	srv := mcp.NewMCPServer(g, mcp.MCPServerOptions{Name: "tamesu", Version: o.Version})
	return srv.ServeStdio()
}

// Tool inputs. Descriptions avoid commas: the jsonschema tag splits on them.

type execResult struct {
	ExitCode int    `json:"exit_code" jsonschema:"description=0 passed or ok; 1 a test failed; 2 config or usage error; 3 infrastructure error; 4 budget exceeded"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr,omitempty"`
}

type validateInput struct {
	Paths  []string `json:"paths,omitempty" jsonschema:"description=Suite files or globs; empty means the suites of the project file"`
	Config string   `json:"config,omitempty" jsonschema:"description=The project file (tamesu.yaml); default the one tamesu mcp was started with"`
	Strict bool     `json:"strict,omitempty" jsonschema:"description=Treat warnings as errors"`
}

type listInput struct {
	Paths  []string `json:"paths,omitempty" jsonschema:"description=Suite files or globs; empty means the suites of the project file"`
	Config string   `json:"config,omitempty" jsonschema:"description=The project file (tamesu.yaml)"`
	Filter string   `json:"filter,omitempty" jsonschema:"description=A regular expression on test names"`
	Tags   []string `json:"tags,omitempty" jsonschema:"description=Only tests having any of these tags"`
	Target string   `json:"target,omitempty" jsonschema:"description=Only tests of this target name"`
}

type runInput struct {
	Paths      []string `json:"paths,omitempty" jsonschema:"description=Suite files or globs; empty means the suites of the project file"`
	Config     string   `json:"config,omitempty" jsonschema:"description=The project file (tamesu.yaml)"`
	Filter     string   `json:"filter,omitempty" jsonschema:"description=A regular expression on test names"`
	Tags       []string `json:"tags,omitempty" jsonschema:"description=Only tests having any of these tags"`
	Target     string   `json:"target,omitempty" jsonschema:"description=Only tests of this target name"`
	Repeat     int      `json:"repeat,omitempty" jsonschema:"description=Override every test's repeat count"`
	Parallel   int      `json:"parallel,omitempty" jsonschema:"description=Concurrent runs; default the number of CPUs"`
	Mode       string   `json:"mode,omitempty" jsonschema:"enum=text,enum=audio,description=Voice tests: text or audio"`
	MaxCostUSD float64  `json:"max_cost_usd,omitempty" jsonschema:"description=Stop once tamesu's own model spend exceeds this"`
	Out        string   `json:"out,omitempty" jsonschema:"description=The run directory; default a temporary one"`
	Replay     bool     `json:"replay,omitempty" jsonschema:"description=Browser goals from the action cache only (no model)"`
}

type runResult struct {
	ExitCode int             `json:"exit_code" jsonschema:"description=0 passed; 1 a test failed; 2 config error; 3 infrastructure error; 4 budget exceeded"`
	OutDir   string          `json:"out_dir" jsonschema:"description=The run directory with summary.json and one directory per test and run (transcript.json and result.json)"`
	Summary  json.RawMessage `json:"summary,omitempty" jsonschema:"description=The run's summary.json"`
	Console  string          `json:"console" jsonschema:"description=The console reporter's output"`
	Stderr   string          `json:"stderr,omitempty"`
}

type mcpGenerateInput struct {
	Source string `json:"source,omitempty" jsonschema:"description=The PRD or system prompt or knowledge base text to draft tests from"`
	File   string `json:"file,omitempty" jsonschema:"description=A file with that text instead of source"`
	Target string `json:"target" jsonschema:"required,description=The target name the tests will use"`
	Type   string `json:"type,omitempty" jsonschema:"enum=rest,enum=browser,enum=livekit,enum=sip,enum=realtime,description=The target type; default rest"`
	Count  int    `json:"count,omitempty" jsonschema:"description=How many tests to draft"`
	Out    string `json:"out,omitempty" jsonschema:"description=Write the suite here; otherwise it is only returned"`
	Config string `json:"config,omitempty" jsonschema:"description=The project file (tamesu.yaml)"`
}

type mcpGenerateResult struct {
	ExitCode  int    `json:"exit_code" jsonschema:"description=0 the draft validates; 1 it does not (it is still returned); 2 usage or config error; 3 the model failed"`
	SuiteYAML string `json:"suite_yaml" jsonschema:"description=The drafted suite to review"`
	Out       string `json:"out,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
}

type schemaInput struct {
	Which string `json:"which,omitempty" jsonschema:"enum=suite,enum=config,description=suite (default) for a suite file or config for tamesu.yaml"`
}

type initInput struct {
	Dir   string `json:"dir,omitempty" jsonschema:"description=The directory to scaffold; default the current one"`
	Force bool   `json:"force,omitempty" jsonschema:"description=Overwrite existing files"`
}

type doctorInput struct {
	Paths  []string `json:"paths,omitempty" jsonschema:"description=Suite files or globs; empty means the suites of the project file"`
	Config string   `json:"config,omitempty" jsonschema:"description=The project file (tamesu.yaml)"`
}

// DefineMCPTools defines the tamesu tools on g and returns them. ServeMCP
// calls it; tests call it to run the tools without a transport.
func DefineMCPTools(g *genkit.Genkit, o MCPOptions) []ai.Tool {
	exec := func(ctx context.Context, args ...string) execResult {
		out, errOut, code := o.Exec(ctx, args)
		return execResult{ExitCode: code, Stdout: out, Stderr: errOut}
	}
	withConfig := func(args []string, cfg string) []string {
		if cfg != "" {
			args = append(args, "--config", cfg)
		}
		return args
	}
	return []ai.Tool{
		genkit.DefineTool(g, "tamesu_validate",
			"Validate tamesu suite files against the schema and the project configuration: unknown keys, wrong shapes, undeclared secrets, each with its file and line. Exit code 0 when valid, 2 when not.",
			func(ctx *ai.ToolContext, in validateInput) (execResult, error) {
				args := withConfig([]string{"validate"}, in.Config)
				if in.Strict {
					args = append(args, "--strict")
				}
				return exec(ctx, append(args, in.Paths...)...), nil
			}),
		genkit.DefineTool(g, "tamesu_list",
			"List the tests of tamesu suites as JSON (suite, name, target, type, tags, repeat) after validating them. Use filter, tags and target the way tamesu run selects tests.",
			func(ctx *ai.ToolContext, in listInput) (execResult, error) {
				args := withConfig([]string{"list", "--json"}, in.Config)
				args = selection(args, in.Filter, in.Tags, in.Target)
				return exec(ctx, append(args, in.Paths...)...), nil
			}),
		genkit.DefineTool(g, "tamesu_run",
			"Run tamesu tests against their targets and return the run's summary.json (every test, its runs, assertions with reasons, metrics and the cost) with the console output. Secrets are resolved through mamori and never appear in the output. Exit code 0 passed, 1 a test failed, 2 config error, 3 infrastructure error, 4 budget exceeded.",
			func(ctx *ai.ToolContext, in runInput) (runResult, error) {
				out := in.Out
				if out == "" {
					dir, err := os.MkdirTemp("", "tamesu-mcp-run-")
					if err != nil {
						return runResult{}, err
					}
					out = dir
				}
				args := withConfig([]string{"run", "--reporter", "console", "--reporter", "json", "--out", out}, in.Config)
				args = selection(args, in.Filter, in.Tags, in.Target)
				if in.Repeat > 0 {
					args = append(args, "--repeat", strconv.Itoa(in.Repeat))
				}
				if in.Parallel > 0 {
					args = append(args, "--parallel", strconv.Itoa(in.Parallel))
				}
				if in.Mode != "" {
					args = append(args, "--mode", in.Mode)
				}
				if in.MaxCostUSD > 0 {
					args = append(args, "--max-cost-usd", strconv.FormatFloat(in.MaxCostUSD, 'f', -1, 64))
				}
				if in.Replay {
					args = append(args, "--replay")
				}
				res := exec(ctx, append(args, in.Paths...)...)
				r := runResult{ExitCode: res.ExitCode, OutDir: out, Console: res.Stdout, Stderr: res.Stderr}
				if b, err := os.ReadFile(filepath.Join(out, "summary.json")); err == nil && json.Valid(b) {
					r.Summary = b
				}
				return r, nil
			}),
		genkit.DefineTool(g, "tamesu_generate",
			"Draft tamesu tests from a PRD, a system prompt or a knowledge base with the scenario generator model: a suite with a target stub to review. The draft is validated; exit code 1 means it needs fixing before it runs.",
			func(ctx *ai.ToolContext, in mcpGenerateInput) (mcpGenerateResult, error) {
				if in.Target == "" {
					return mcpGenerateResult{}, errors.New("target is required")
				}
				from := in.File
				if from == "" {
					if strings.TrimSpace(in.Source) == "" {
						return mcpGenerateResult{}, errors.New("source (the text) or file is required")
					}
					dir, err := os.MkdirTemp("", "tamesu-mcp-generate-")
					if err != nil {
						return mcpGenerateResult{}, err
					}
					defer os.RemoveAll(dir) //nolint:errcheck // best effort
					from = filepath.Join(dir, "source.md")
					if err := os.WriteFile(from, []byte(in.Source), 0o600); err != nil {
						return mcpGenerateResult{}, err
					}
				}
				out := in.Out
				if out == "" {
					dir, err := os.MkdirTemp("", "tamesu-mcp-generate-out-")
					if err != nil {
						return mcpGenerateResult{}, err
					}
					defer os.RemoveAll(dir) //nolint:errcheck // best effort
					out = filepath.Join(dir, "suite.yaml")
				}
				args := withConfig([]string{"generate", "--from", from, "--target", in.Target, "--out", out}, in.Config)
				if in.Type != "" {
					args = append(args, "--type", in.Type)
				}
				if in.Count > 0 {
					args = append(args, "--count", strconv.Itoa(in.Count))
				}
				res := exec(ctx, args...)
				r := mcpGenerateResult{ExitCode: res.ExitCode, Stderr: res.Stderr}
				if in.Out != "" {
					r.Out = out
				}
				if b, err := os.ReadFile(out); err == nil {
					r.SuiteYAML = string(b)
				} else if res.Stdout != "" {
					r.SuiteYAML = res.Stdout
				}
				return r, nil
			}),
		genkit.DefineTool(g, "tamesu_doctor",
			"Check the environment of a tamesu project: the project file, the suites, that every secret resolves, the model credentials, Chromium, LiveKit and the speech models. Exit code 0 ok, 2 config problems, 3 infrastructure problems.",
			func(ctx *ai.ToolContext, in doctorInput) (execResult, error) {
				return exec(ctx, append(withConfig([]string{"doctor"}, in.Config), in.Paths...)...), nil
			}),
		genkit.DefineTool(g, "tamesu_init",
			"Scaffold a tamesu project: tamesu.yaml, an example suite and the editor schema, in a directory.",
			func(ctx *ai.ToolContext, in initInput) (execResult, error) {
				args := []string{"init"}
				if in.Force {
					args = append(args, "--force")
				}
				if in.Dir != "" {
					args = append(args, in.Dir)
				}
				return exec(ctx, args...), nil
			}),
		genkit.DefineTool(g, "tamesu_schema",
			"Return the JSON Schema of a tamesu suite file (targets, tests, personas, scenarios, steps, assertions) or of tamesu.yaml, the reference for writing them.",
			func(_ *ai.ToolContext, in schemaInput) (string, error) {
				if o.Schema == nil {
					return "", errors.New("no schema in this build")
				}
				which := in.Which
				if which == "" {
					which = "suite"
				}
				return o.Schema(which)
			}),
	}
}

// selection appends tamesu run's selection flags.
func selection(args []string, filter string, tags []string, target string) []string {
	if filter != "" {
		args = append(args, "--filter", filter)
	}
	if len(tags) > 0 {
		args = append(args, "--tags", strings.Join(tags, ","))
	}
	if target != "" {
		args = append(args, "--target", target)
	}
	return args
}

// initBareGenkit is genkit.Init with no plugins and no prompts, for the
// tools-only MCP server; it wraps the panic Init raises on failure.
func initBareGenkit(ctx context.Context) (g *genkit.Genkit, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("llm: genkit init: %v", r)
		}
	}()
	g = genkit.Init(ctx)
	return g, nil
}
