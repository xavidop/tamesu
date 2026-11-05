package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/generate"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/secrets"
)

func (a *app) newGenerateCmd() *cobra.Command {
	var from, targetName, typ, out string
	var count int
	cmd := &cobra.Command{
		Use:   "generate --from <file> --target <name>",
		Short: "Draft tests from a PRD, a system prompt or a knowledge base (the generateScenarios flow)",
		Long: "Generate reads a document and asks models.scenario_generator (or models.default)\n" +
			"for draft tests for one target: personas, goals, judge rubrics and phrases for a\n" +
			"conversational target, goals and expectations for a browser target. It writes a\n" +
			"suite with a target stub to review, then validates it. Exit 1 when the draft does\n" +
			"not validate (the file is kept), 2 on a usage or config error, 3 when the model\n" +
			"failed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if from == "" || targetName == "" {
				return usageError(errors.New("--from <file> and --target <name> are required"))
			}
			if typ == "" {
				typ = config.TargetREST
			}
			if !containsString(config.TargetTypes(), typ) {
				return usageError(fmt.Errorf("--type must be one of %s", strings.Join(config.TargetTypes(), ", ")))
			}
			source, err := os.ReadFile(from)
			if err != nil {
				return configError(fmt.Errorf("--from: %w", err))
			}
			if strings.TrimSpace(string(source)) == "" {
				return configError(fmt.Errorf("--from: %s is empty", from))
			}
			p, perrs, err := a.loadProject(cmd, true)
			if err != nil {
				return err
			}
			if perrs.HasErrors() {
				return configError(perrs)
			}
			provider, err := a.modelProvider(ctx, p)
			if err != nil {
				return err
			}
			defer func() { _ = provider.Close(context.WithoutCancel(ctx)) }()
			gen, ok := provider.(model.Generator)
			if !ok {
				return configError(errors.New("this build's model provider cannot generate tests"))
			}
			draft, err := gen.GenerateTests(ctx, model.GenerateRequest{Source: string(source), SourceName: from, Target: targetName, Type: typ, Count: count})
			if err != nil {
				return infraError(err)
			}
			data, err := generate.Suite(draft, generate.Options{SourceName: from, Target: targetName, Type: typ})
			if err != nil {
				return infraError(err)
			}
			if out == "" {
				_, _ = cmd.OutOrStdout().Write(data)
				return a.validateDraft(cmd, p, data, "")
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return infraError(err)
			}
			if err := os.WriteFile(out, data, 0o644); err != nil {
				return infraError(err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %d draft test(s) for target %s (%s); review before running\n", out, len(draft.Tests), targetName, typ)
			return a.validateDraft(cmd, p, data, out)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the PRD, system prompt or knowledge base file to draft from")
	cmd.Flags().StringVar(&targetName, "target", "", "the target name the tests use")
	cmd.Flags().StringVar(&typ, "type", "rest", "the target's type: rest, livekit, sip or browser")
	cmd.Flags().IntVar(&count, "count", 5, "how many tests to draft")
	cmd.Flags().StringVar(&out, "out", "", "write the suite here (default: print it)")
	return cmd
}

// modelProvider builds the AI layer from the project alone (no suites): the
// model credentials among the project's secrets are resolved first. The
// error is already an exit error.
func (a *app) modelProvider(ctx context.Context, p config.Project) (model.Provider, error) {
	if a.deps.NewProvider == nil {
		return nil, configError(errors.New("no model provider in this build"))
	}
	want := map[string]bool{}
	for _, n := range secrets.ModelCredentialNames {
		want[n] = true
	}
	for _, tmpl := range p.Models.Credentials {
		for _, n := range config.SecretRefs(tmpl) {
			want[n] = true
		}
	}
	refs := map[string]string{}
	for name, ref := range p.Secrets {
		if want[name] {
			refs[name] = ref
		}
	}
	flagVars, err := a.refVars(nil)
	if err != nil {
		return nil, err
	}
	store, err := secrets.Resolve(ctx, refs, secrets.Options{RefVars: flagVars, EnvFile: a.envFile, Providers: a.deps.Providers})
	if err != nil {
		return nil, err
	}
	for _, name := range store.Names() {
		if v, ok := store.Lookup(name); ok {
			a.redactor.Add(name, v)
		}
	}
	provider, err := a.deps.NewProvider(ctx, ProviderConfig{Project: p, Models: p.Models, Secrets: store, Logger: a.logger(), MaxCostUSD: p.Budgets.MaxCostUSD, MaxTokens: p.Budgets.MaxTokens})
	if err != nil {
		return nil, infraError(fmt.Errorf("model provider: %w", err))
	}
	return provider, nil
}

// validateDraft loads the generated suite the way tamesu validate would and
// reports its problems; exit 1 keeps the draft for review.
func (a *app) validateDraft(cmd *cobra.Command, p config.Project, data []byte, path string) error {
	if path == "" {
		tmp, err := os.CreateTemp("", "tamesu-generate-*.yaml")
		if err != nil {
			return nil
		}
		path = tmp.Name()
		_, _ = tmp.Write(data)
		_ = tmp.Close()
		defer func() { _ = os.Remove(path) }()
	}
	_, errs, err := config.LoadSuiteWithProject(path, p, config.LoadOptions{})
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "the draft does not parse: %v\n", err)
		return silentExit(ExitFailed)
	}
	if errs.HasErrors() {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "the draft does not validate; fix it before running:\n")
		_, _ = printErrors(cmd.ErrOrStderr(), errs, a.style(cmd.ErrOrStderr()))
		return silentExit(ExitFailed)
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
