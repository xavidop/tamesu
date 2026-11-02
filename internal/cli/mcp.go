package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
)

// MCPRequest is what `tamesu mcp` hands the MCP server implementation (the
// llm module, on Genkit's MCP support).
type MCPRequest struct {
	Version string
	// Exec runs a tamesu command in this process, with the deps and global
	// flags of the mcp invocation, and returns its streams and exit code.
	Exec func(ctx context.Context, args []string) (stdout, stderr string, code int)
	// Schema returns the JSON Schema of a suite ("suite") or of tamesu.yaml
	// ("config").
	Schema func(which string) (string, error)
	Logger *slog.Logger
	// HTTPAddr and HTTPPath serve over HTTP instead of stdio when set.
	HTTPAddr string
	HTTPPath string
	// RunsDir is where runs live (.tamesu/runs): the resources and the
	// default output of tamesu_run.
	RunsDir string
}

func (a *app) newMCPCmd() *cobra.Command {
	var dir, httpAddr, httpPath string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve tamesu to coding agents as an MCP server over stdio or HTTP",
		Long: "Mcp serves the tamesu tools over the Model Context Protocol on standard input and\n" +
			"output, or over HTTP with --http, so a coding agent (Claude Code, Cursor, Codex, ...)\n" +
			"can validate, list, run and draft tests and read the suite schema: tamesu_validate,\n" +
			"tamesu_list, tamesu_run, tamesu_generate, tamesu_doctor, tamesu_init and\n" +
			"tamesu_schema. Each tool runs the matching command in this process with the same\n" +
			"--config, --var and --env-file, and returns its output and exit code (a run returns\n" +
			"its summary.json). The run directories under .tamesu/runs are MCP resources:\n" +
			"tamesu://runs lists them, tamesu://runs/<run>/summary.json and\n" +
			"tamesu://runs/<run>/tests/<test>/run-<n>/result.json read them. Secrets are\n" +
			"resolved per command and never returned. Over stdio the server stops when the\n" +
			"client closes the connection; over HTTP it runs until interrupted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.deps.ServeMCP == nil {
				return configError(errors.New("mcp is not available in this build"))
			}
			if dir != "" {
				if err := os.Chdir(dir); err != nil {
					return configError(fmt.Errorf("--cwd: %w", err))
				}
			}
			logger := a.logger()
			slog.SetDefault(logger)
			req := MCPRequest{Version: a.info.Version, Exec: a.exec, Schema: schemaJSON, Logger: logger, HTTPAddr: httpAddr, HTTPPath: httpPath, RunsDir: filepath.Join(".tamesu", "runs")}
			if err := a.deps.ServeMCP(cmd.Context(), req); err != nil {
				return infraError(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "cwd", "", "project directory to run the commands in (default: the current directory)")
	cmd.Flags().StringVar(&httpAddr, "http", "", "serve over streamable HTTP on this address (such as :8765) instead of stdio")
	cmd.Flags().StringVar(&httpPath, "http-path", "/mcp", "the HTTP endpoint path, with --http")
	return cmd
}

// exec runs one tamesu command on a fresh command tree with this app's deps
// and global flags, capturing its streams.
func (a *app) exec(ctx context.Context, args []string) (string, string, int) {
	var out, errb bytes.Buffer
	root := NewRoot(a.info, Streams{Out: &out, Err: &errb}, a.deps)
	global := []string{"--config", a.configPath}
	if a.verbose {
		global = append(global, "--verbose")
	}
	for _, v := range a.varFlags {
		global = append(global, "--var", v)
	}
	if a.envFile != "" {
		global = append(global, "--env-file", a.envFile)
	}
	code := Execute(ctx, root, append(global, args...))
	return out.String(), errb.String(), code
}

// schemaJSON renders the suite or the project schema.
func schemaJSON(which string) (string, error) {
	switch which {
	case "", "suite":
		b, err := config.SchemaJSON(config.SuiteSchema())
		return string(b), err
	case "config":
		b, err := config.SchemaJSON(config.ProjectSchema())
		return string(b), err
	}
	return "", fmt.Errorf("unknown schema %q (suite or config)", which)
}
