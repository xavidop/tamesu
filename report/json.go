package report

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// JSON writes summary.json into the run directory when the run finishes.
type JSON struct{ dir string }

// NewJSON returns the JSON reporter for the run directory.
func NewJSON(dir string) *JSON { return &JSON{dir: dir} }

func (*JSON) RunStarted(RunInfo)                    {}
func (*JSON) TestStarted(*TestOutcome)              {}
func (*JSON) RunFinished(*TestOutcome, *RunOutcome) {}
func (*JSON) TestFinished(*TestOutcome)             {}

// Finish writes summary.json.
func (j *JSON) Finish(_ context.Context, s *Summary) error {
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(j.dir, "summary.json"), append(b, '\n'), 0o644)
}
