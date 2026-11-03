package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/generate"
)

func (a *app) newImportCmd() *cobra.Command {
	var format string
	var targetName, typ, out, goldenDir, goldenMode string
	var tags []string
	cmd := &cobra.Command{
		Use:   "import <conversations.json|.jsonl|.csv> --target <name>",
		Short: "Turn recorded conversations into a suite that replays them against the agent",
		Long: "Import reads real conversations (a JSON array of conversations with turns, flat\n" +
			"messages with a conversation id, JSON Lines of either, or a CSV with conversation,\n" +
			"role and text columns) and writes one test per conversation: a scenario script that\n" +
			"says the user's lines in order, and a golden assertion holding the agent's recorded\n" +
			"answers (similar by default). The target is a stub to point at your agent. Exit 1\n" +
			"when the suite does not validate (the files are kept), 2 on a usage or config error.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetName == "" {
				return usageError(errors.New("--target <name> is required"))
			}
			if typ == "" {
				typ = config.TargetREST
			}
			if !containsString(config.TargetTypes(), typ) || typ == config.TargetBrowser {
				return usageError(fmt.Errorf("--type must be a conversational target type: %s", strings.Join(conversationalTypes(), ", ")))
			}
			if goldenDir != "" && out == "" {
				return usageError(errors.New("--golden-dir needs --out: the reference files are written next to the suite"))
			}
			data, err := os.ReadFile(args[0])
			if err != nil {
				return configError(err)
			}
			convs, err := generate.ParseConversationsAs(args[0], data, format)
			if err != nil {
				return configError(fmt.Errorf("%s: %w", args[0], err))
			}
			if len(convs) == 0 {
				return configError(fmt.Errorf("%s: no conversations found", args[0]))
			}
			res, err := generate.Import(convs, generate.ImportOptions{SourceName: args[0], Target: targetName, Type: typ, GoldenDir: goldenDir, GoldenMode: goldenMode, Tags: tags})
			if err != nil {
				return configError(err)
			}
			p, perrs, err := a.loadProject(cmd, false)
			if err != nil {
				return err
			}
			if perrs.HasErrors() {
				return configError(perrs)
			}
			for _, id := range res.Skipped {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skipped %q: no user line followed by an agent answer\n", id)
			}
			if out == "" {
				_, _ = cmd.OutOrStdout().Write(res.Suite)
				return a.validateDraft(cmd, p, res.Suite, "")
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return infraError(err)
			}
			for rel, turns := range res.Golden {
				path := filepath.Join(filepath.Dir(out), filepath.FromSlash(rel))
				b, err := json.MarshalIndent(turns, "", "  ")
				if err != nil {
					return infraError(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return infraError(err)
				}
				if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
					return infraError(err)
				}
			}
			if err := os.WriteFile(out, res.Suite, 0o644); err != nil {
				return infraError(err)
			}
			msg := fmt.Sprintf("%s: %d test(s) from %d conversation(s) for target %s (%s)", out, res.Tests, len(convs), targetName, typ)
			if len(res.Golden) > 0 {
				msg += fmt.Sprintf(", %d reference file(s) under %s", len(res.Golden), filepath.Join(filepath.Dir(out), goldenDir))
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s; point the target at your agent before running\n", msg)
			return a.validateDraft(cmd, p, res.Suite, out)
		},
	}
	cmd.Flags().StringVar(&format, "format", generate.FormatAuto, "the file's format: "+strings.Join(generate.ImportFormats, ", ")+" (auto recognises the LangSmith, Langfuse and Voiceflow exports by their keys)")
	cmd.Flags().StringVar(&targetName, "target", "", "the target name the tests use")
	cmd.Flags().StringVar(&typ, "type", "rest", "the target's type: "+strings.Join(conversationalTypes(), ", "))
	cmd.Flags().StringVar(&out, "out", "", "write the suite here (default: print it)")
	cmd.Flags().StringVar(&goldenDir, "golden-dir", "", "write each conversation's agent answers to <dir>/<test>.json next to the suite instead of inline turns")
	cmd.Flags().StringVar(&goldenMode, "golden-mode", "similar", "how answers are compared: exact, similar or judge")
	cmd.Flags().StringSliceVar(&tags, "tags", []string{"imported"}, "tags on every test")
	return cmd
}

// conversationalTypes are the target types a conversation can replay on.
func conversationalTypes() []string {
	var out []string
	for _, t := range config.TargetTypes() {
		if t != config.TargetBrowser {
			out = append(out, t)
		}
	}
	return out
}
