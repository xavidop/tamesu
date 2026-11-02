package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
)

// goldenFile resolves a golden file next to the suite.
func goldenFile(suite *config.Suite, file string) string {
	if filepath.IsAbs(file) || suite == nil {
		return file
	}
	return filepath.Join(filepath.Dir(suite.Path), file)
}

// loadGolden reads reference turns: a JSON array of strings, or a
// transcript.json whose agent turns are the reference.
func loadGolden(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var turns []string
	if json.Unmarshal(b, &turns) == nil {
		return turns, nil
	}
	var doc struct {
		Turns []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || len(doc.Turns) == 0 {
		return nil, fmt.Errorf("%s: not a JSON array of turns nor a transcript.json", path)
	}
	for _, t := range doc.Turns {
		if t.Role == "agent" {
			turns = append(turns, t.Text)
		}
	}
	return turns, nil
}

// writeGolden writes the agent turns of a run as a golden file.
func writeGolden(path string, transcript []assert.Turn) error {
	var turns []string
	for _, t := range transcript {
		if t.Role == "agent" {
			turns = append(turns, t.Text)
		}
	}
	b, err := json.MarshalIndent(turns, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// buildGolden turns a golden assertion into an assertion: with
// --update-golden (or a missing file) the file is written from the run and
// the assertion passes with a note; otherwise the reference is loaded.
func (r *run) buildGolden(it *item, g *config.GoldenAssertion, transcript []assert.Turn) (assert.Assertion, error) {
	if len(g.Turns) > 0 {
		return assert.Golden(g.Turns, g.Mode, g.Min, ""), nil
	}
	path := goldenFile(it.suite, g.File)
	_, statErr := os.Stat(path)
	if r.o.UpdateGolden || os.IsNotExist(statErr) {
		if err := writeGolden(path, transcript); err != nil {
			return nil, fmt.Errorf("golden %s: %w", g.File, err)
		}
		note := "written from this run"
		if os.IsNotExist(statErr) && !r.o.UpdateGolden {
			note = "was missing, written from this run; review it and run again"
		}
		return assert.Func("golden "+g.Mode+" "+g.File, func(ctx context.Context, _ *assert.RunResult) assert.Result {
			return assert.Result{Pass: true, Reason: strings.TrimSpace(g.File + " " + note)}
		}), nil
	}
	ref, err := loadGolden(path)
	if err != nil {
		return nil, fmt.Errorf("golden: %w", err)
	}
	return assert.Golden(ref, g.Mode, g.Min, g.File), nil
}
