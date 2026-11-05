package cli

import (
	"errors"
	"fmt"
	"github.com/xavidop/tamesu/report"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/secrets"
)

// suiteFile is one loaded suite with everything found wrong with it.
type suiteFile struct {
	Path  string
	Suite *config.Suite
	Errs  config.Errors
}

// env is the ${VAR} lookup: the process environment, then --env-file.
func (a *app) env() config.EnvLookup { return secrets.EnvLookup(a.envFile, a.deps.LookupEnv) }

// refVars are the mamori ref vars: the suite's vars, then --var KEY=VAL on
// top. The ambient environment never feeds them.
func (a *app) refVars(s *config.Suite) (map[string]string, error) {
	vars := map[string]string{}
	if s != nil {
		for k, v := range s.Vars {
			vars[k] = v
		}
	}
	for _, kv := range a.varFlags {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, usageError(fmt.Errorf("--var %q: expected KEY=VAL", kv))
		}
		vars[k] = v
	}
	return vars, nil
}

// loadProject reads --config. The default tamesu.yaml may be absent; a path
// the user named must exist.
func (a *app) loadProject(cmd *cobra.Command, strict bool) (config.Project, config.Errors, error) {
	if cmd.Flags().Changed("config") {
		if _, err := os.Stat(a.configPath); errors.Is(err, fs.ErrNotExist) {
			return config.Project{}, nil, configError(fmt.Errorf("config file %s does not exist", a.configPath))
		}
	}
	p, errs, err := config.LoadProject(a.configPath, config.LoadOptions{Env: a.env(), StrictEnv: strict})
	if err != nil {
		return p, nil, configError(err)
	}
	return p, errs, nil
}

// suitePaths resolves the suite files to work on: the paths and globs given
// on the command line (relative to the working directory), or the project's
// suites: globs (relative to the config file).
func (a *app) suitePaths(args []string, p config.Project) ([]string, error) {
	if len(args) > 0 {
		paths, err := config.ExpandPaths(args, "")
		if err != nil {
			return nil, configError(err)
		}
		return paths, nil
	}
	dir := filepath.Dir(a.configPath)
	paths, err := config.ExpandPaths(p.Suites, dir)
	if err != nil {
		return nil, configError(fmt.Errorf("%w (suites: in %s; pass suite paths to use others)", err, a.configPath))
	}
	if dir != "." && dir != "" {
		for i := range paths {
			paths[i] = filepath.ToSlash(filepath.Join(dir, paths[i]))
		}
	}
	return paths, nil
}

// loadSuites loads every path. I/O failures are config errors; validation
// problems come back in each suiteFile.
func (a *app) loadSuites(paths []string, p config.Project, strict bool) ([]suiteFile, error) {
	out := make([]suiteFile, 0, len(paths))
	for _, path := range paths {
		s, errs, err := config.LoadSuiteWithProject(path, p, config.LoadOptions{Env: a.env(), StrictEnv: strict})
		if err != nil {
			return nil, configError(err)
		}
		out = append(out, suiteFile{Path: path, Suite: s, Errs: errs})
	}
	return out, nil
}

// selectFlags are the test selection flags shared by list and run.
type selectFlags struct {
	filter string
	tags   []string
	target string
	shard  string
}

func (f *selectFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.filter, "filter", "", "run tests whose name matches this regular expression")
	cmd.Flags().StringSliceVar(&f.tags, "tags", nil, "run tests having any of these tags")
	cmd.Flags().StringVar(&f.target, "target", "", "restrict to tests of this target")
	cmd.Flags().StringVar(&f.shard, "shard", "", "INDEX/TOTAL: run only this slice of the selected tests, for CI jobs that split a suite (2/4 is the second of four)")
}

// shardOf parses --shard.
func (f *selectFlags) shardOf() (config.Shard, error) {
	return config.ParseShard(f.shard)
}

func (f *selectFlags) compile() (*regexp.Regexp, error) {
	if f.filter == "" {
		return nil, nil
	}
	re, err := regexp.Compile(f.filter)
	if err != nil {
		return nil, usageError(fmt.Errorf("--filter: %w", err))
	}
	return re, nil
}

// printErrors writes every entry, sorted by line, and returns how many are
// errors and how many warnings.
func printErrors(w interface{ Write([]byte) (int, error) }, errs config.Errors, st report.Style) (nErr, nWarn int) {
	for _, e := range errs.Sorted() {
		line := e.Error()
		if e.Warning {
			nWarn++
			line = st.Warn(line)
		} else {
			nErr++
			line = st.Fail(line)
		}
		_, _ = fmt.Fprintln(w, line)
	}
	return nErr, nWarn
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
