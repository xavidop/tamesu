package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/firebase/genkit/go/core/status"
)

// Retry is how a model call is retried before the next model is tried.
type Retry struct {
	// Attempts is the number of tries per model (default 3; 1 retries nothing).
	Attempts int
	// Backoff is the wait before the second try (default 500ms); it doubles
	// every try, with jitter, up to MaxBackoff (default 10s).
	Backoff    time.Duration
	MaxBackoff time.Duration
}

// The defaults of Retry.
const (
	DefaultRetryAttempts   = 3
	DefaultRetryBackoff    = 500 * time.Millisecond
	DefaultRetryMaxBackoff = 10 * time.Second
)

// Fallbacks names the models tried, in order, after a job's model failed:
// the job's own list, or Default for the jobs without one.
type Fallbacks struct {
	Default           []string
	Judge             []string
	Simulator         []string
	BrowserAgent      []string
	ScenarioGenerator []string
	// Embedder is the only list that applies to the embedding model: the
	// default list names chat models, which cannot embed.
	Embedder []string
}

// ErrBadOutput marks a reply that was not the JSON the prompt asked for. It
// is retried like a transport failure: the same model usually gets it right
// the next time.
var ErrBadOutput = errors.New("the model did not return the expected JSON")

// attempts is Retry.Attempts with the default applied.
func (r Retry) attempts() int {
	if r.Attempts <= 0 {
		return DefaultRetryAttempts
	}
	return r.Attempts
}

// wait is how long to sleep before try n (2 is the first retry): Backoff
// doubled per try, capped at MaxBackoff, with half of it as jitter.
func (r Retry) wait(n int) time.Duration {
	base, maxWait := r.Backoff, r.MaxBackoff
	if base <= 0 {
		base = DefaultRetryBackoff
	}
	if maxWait <= 0 {
		maxWait = DefaultRetryMaxBackoff
	}
	d := base
	for i := 2; i < n && d < maxWait; i++ {
		d *= 2
	}
	d = min(d, maxWait)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// fallbacksFor is the fallback list of a job (see Fallbacks).
func (c *Client) fallbacksFor(job string) []string {
	f := c.cfg.Models.Fallback
	var list []string
	switch job {
	case "simulator":
		list = f.Simulator
	case "judge":
		list = f.Judge
	case "browser_agent":
		list = f.BrowserAgent
	case "scenario_generator":
		list = f.ScenarioGenerator
	case "embedder":
		return f.Embedder
	}
	if len(list) == 0 {
		list = f.Default
	}
	return list
}

// chain is the models tried for a call, in order: primary, then the fallback
// lists, each model once and never an empty name.
func chain(primary string, fallbacks ...[]string) []string {
	out := make([]string, 0, 1+len(fallbacks))
	add := func(m string) {
		if m == "" {
			return
		}
		for _, have := range out {
			if have == m {
				return
			}
		}
		out = append(out, m)
	}
	add(primary)
	for _, list := range fallbacks {
		for _, m := range list {
			add(m)
		}
	}
	return out
}

// attempt runs fn with each model of the chain until one answers. A
// transient failure (see retryable) is retried on the same model up to
// Retry.Attempts times with backoff; a failure the model will repeat (a bad
// request, a missing model, a refused key) moves straight to the next model.
// A failure no other model can fix (see terminal) comes back at once, with
// fn's result, because a spent budget still carries a usable answer. When the
// chain is exhausted the last error comes back, with what was tried appended.
func attempt[T any](ctx context.Context, c *Client, job string, models []string, fn func(ctx context.Context, modelName string) (T, error)) (T, error) {
	var zero T
	if len(models) == 0 {
		return zero, fmt.Errorf("llm: %s: no model configured", job)
	}
	attempts := c.cfg.Retry.attempts()
	var trail []string
	var last error
	for i, m := range models {
		for n := 1; ; n++ {
			out, err := fn(ctx, m)
			if err == nil {
				return out, nil
			}
			if terminal(ctx, err) {
				return out, err
			}
			last = err
			if retryable(err) && n < attempts {
				wait := c.cfg.Retry.wait(n + 1)
				c.logger.Warn("model call failed, retrying", "job", job, "model", m, "attempt", n, "of", attempts, "wait", wait.Round(time.Millisecond).String(), "err", err)
				c.countRetry()
				if !sleep(ctx, wait) {
					return zero, fmt.Errorf("%w (while waiting to retry %s with %s: %v)", ctx.Err(), job, m, err)
				}
				continue
			}
			trail = append(trail, fmt.Sprintf("%s %s", m, times(n)))
			if i+1 < len(models) {
				c.logger.Warn("model call failed, trying the fallback", "job", job, "model", m, "attempts", n, "fallback", models[i+1], "err", err)
				c.countFallback()
			}
			break
		}
	}
	if len(models) == 1 && attempts == 1 {
		return zero, last
	}
	return zero, fmt.Errorf("%w (tried %s)", last, strings.Join(trail, ", then "))
}

// times words an attempt count.
func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// sleep waits d or until ctx ends; false when ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// terminal reports a failure no retry and no other model can fix: the caller
// is gone, the budget is spent, or no speech model is configured at all.
func terminal(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return errors.Is(err, ErrBudgetExceeded) || errors.Is(err, ErrNoSpeechModel) || errors.Is(err, context.Canceled)
}

// retryable reports a failure the same model may not repeat: a rate limit,
// an overloaded or unavailable provider, a timeout, a dropped connection, a
// server error, or a reply that was not the JSON the prompt asked for.
// Everything the provider classified as the request's fault (a bad request,
// an unknown model, a refused key, a quota that is simply gone) is not.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrBadOutput) {
		return true
	}
	if s, ok := status.Classified(err); ok {
		switch s {
		case status.Unavailable, status.ResourceExhausted, status.DeadlineExceeded, status.Aborted, status.Internal, status.Unknown:
			return true
		default:
			return false
		}
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{" 429", "429 ", "rate limit", "ratelimit", "overloaded", " 500", "500 ", " 502", "502 ", " 503", "503 ", " 504", "504 ", "unavailable", "timeout", "timed out", "deadline exceeded", "connection reset", "temporarily", "try again", "server error", "resource exhausted", "resource_exhausted"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// badOutput is the error of a reply that did not parse as the prompt's schema.
func badOutput(flow, modelName string, err error, text string) error {
	return fmt.Errorf("llm: %s with %s: %w: %v (text: %s)", flow, modelName, ErrBadOutput, err, short(text))
}
