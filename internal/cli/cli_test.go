package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/xavidop/mamori"

	"github.com/xavidop/tamesu/internal/cli"
)

const specSuite = "../config/testdata/valid/support-agent.yaml"

// run executes one command on a fresh command tree and returns the exit code
// and the two streams.
func run(t *testing.T, env map[string]string, deps cli.Deps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	if deps.LookupEnv == nil {
		deps.LookupEnv = func(name string) (string, bool) { v, ok := env[name]; return v, ok }
	}
	root := cli.NewRoot(cli.BuildInfo{Version: "1.2.3", Commit: "abc1234", Date: "2026-10-31"}, cli.Streams{Out: &out, Err: &errb}, deps)
	code = cli.Execute(context.Background(), root, args)
	return code, out.String(), errb.String()
}

// fakeProvider answers fake://<path> from a map; a path in errs fails with that error.
type fakeProvider struct {
	values map[string]string
	errs   map[string]error
}

func (p *fakeProvider) Scheme() string { return "fake" }

func (p *fakeProvider) Resolve(_ context.Context, ref mamori.Ref) (mamori.Value, error) {
	if err, ok := p.errs[ref.Path]; ok {
		return mamori.Value{}, err
	}
	v, ok := p.values[ref.Path]
	if !ok {
		return mamori.Value{}, fmt.Errorf("fake: %s: %w", ref.Path, mamori.ErrNotFound)
	}
	return mamori.Value{Bytes: []byte(v), Sensitive: true}, nil
}
