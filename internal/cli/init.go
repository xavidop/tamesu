package cli

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// scaffoldFS holds the files `tamesu init` writes: tamesu.yaml and examples/.
//
//go:embed all:scaffold
var scaffoldFS embed.FS

func (a *app) newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Scaffold tamesu.yaml and an examples/ suite",
		Long: "Init writes a tamesu.yaml with sensible defaults and examples/support-agent.yaml,\n" +
			"a suite with a REST and a LiveKit target, into dir (default: the working\n" +
			"directory). It refuses to overwrite an existing tamesu.yaml unless --force is given.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			out := cmd.OutOrStdout()
			var files []string
			err := fs.WalkDir(scaffoldFS, "scaffold", func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					files = append(files, path)
				}
				return err
			})
			if err != nil {
				return err
			}
			// Check every destination first, so a refusal writes nothing.
			dst := func(path string) string { return filepath.Join(dir, strings.TrimPrefix(path, "scaffold/")) }
			if !force {
				for _, path := range files {
					if _, err := os.Stat(dst(path)); err == nil {
						return configError(fmt.Errorf("%s already exists; use --force to overwrite", dst(path)))
					}
				}
			}
			for _, path := range files {
				data, err := scaffoldFS.ReadFile(path)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(dst(path)), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(dst(path), data, 0o644); err != nil {
					return err
				}
				_, _ = fmt.Fprintln(out, a.style(out).Pass("created"), dst(path))
			}
			_, _ = fmt.Fprintln(out, "\nNext: edit tamesu.yaml and examples/, then run `tamesu validate` and `tamesu doctor`.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return cmd
}
