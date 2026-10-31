package cli

import (
	"context"
	"errors"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/secrets"
)

// Process exit codes (design.md §3).
const (
	ExitOK     = 0 // every test passed
	ExitFailed = 1 // one or more tests failed
	ExitConfig = 2 // config, schema or usage error
	ExitInfra  = 3 // infrastructure error: browser, LiveKit, model, secret provider
	ExitBudget = 4 // the cost or token budget was exceeded
)

// exitError carries an explicit exit code.
type exitError struct {
	code int
	err  error
	// silent means the command already printed everything: Execute writes no
	// "error:" line.
	silent bool
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

func usageError(err error) error  { return &exitError{code: ExitConfig, err: err} }
func configError(err error) error { return &exitError{code: ExitConfig, err: err} }
func silentExit(code int) error   { return &exitError{code: code, silent: true} }

// ExitCode maps err to a process exit code: 0 for nil; the code of an
// exitError; 2 for config errors (schema, usage, unresolvable secret refs); 3
// for infrastructure errors (a secret provider that failed, cancellation,
// timeouts and anything unclassified).
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var errs config.Errors
	var one *config.Error
	if errors.As(err, &errs) || errors.As(err, &one) || secrets.IsConfig(err) {
		return ExitConfig
	}
	if secrets.IsInfra(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ExitInfra
	}
	return ExitInfra
}
