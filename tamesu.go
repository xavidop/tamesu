package tamesu

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

// Persona is the simulated user of an agent test (design.md §2).
type Persona struct {
	Name     string
	Style    string
	Language string
	Voice    string
	// InterruptProbability, SilenceAfterTurnMs and BackgroundNoise are the
	// audio-mode behaviours.
	InterruptProbability float64
	SilenceAfterTurnMs   [2]int
	BackgroundNoise      string
	LineQuality          string
}

// Scenario is what the persona tries to achieve and when to stop.
type Scenario struct {
	Goal     string
	MaxTurns int
	StopWhen string
}

// Test is one scenario run against one target from Go.
type Test struct {
	Name          string
	Target        target.Target
	Persona       Persona
	Scenario      Scenario
	Repeat        int
	PassThreshold float64
	Retries       int
	Timeout       time.Duration
	Seed          int64
	Assert        []assert.Assertion
}

// Suite is a loaded YAML suite as the Go API sees it.
type Suite struct {
	Name string
	Path string
	Tags []string
	// Tests lists the suite's tests by name, target and target type.
	Tests []TestInfo
}

// TestInfo describes one YAML test.
type TestInfo struct {
	Name   string
	Target string
	Type   string
	Tags   []string
}

// LoadSuite loads and validates a YAML suite. Validation errors come back as
// one error that lists every problem with its file and line.
func LoadSuite(path string) (*Suite, error) {
	s, errs, err := config.LoadSuite(path, config.LoadOptions{Env: os.LookupEnv})
	if err != nil {
		return nil, fmt.Errorf("tamesu: load %s: %w", path, err)
	}
	if errs.HasErrors() {
		return nil, errs.Only()
	}
	out := &Suite{Name: s.Suite, Path: s.Path, Tags: s.Tags}
	for _, t := range s.Tests {
		tgt, _ := s.TargetOf(t)
		out.Tests = append(out.Tests, TestInfo{Name: t.Name, Target: t.Target, Type: tgt.Type, Tags: s.TagsOf(t)})
	}
	return out, nil
}

// Env returns an environment variable, or "" when it is not set. It is a
// convenience for target configs built in Go tests.
func Env(name string) string { return os.Getenv(name) }

// UUID returns a random UUID v4, for room and session names.
func UUID() string { return uuid.NewString() }
