package target

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"
)

// ConnectPolicy is how a target retries reaching the agent: the websocket or
// room it joins at the start of a session, the requests it sends. The zero
// value is three attempts with a 500ms backoff.
type ConnectPolicy struct {
	// Attempts is the number of tries (default 3; 1 retries nothing).
	Attempts int
	// Backoff is the wait before the second try (default 500ms); it doubles
	// every try, with jitter, up to MaxBackoff (default 5s).
	Backoff    time.Duration
	MaxBackoff time.Duration
}

// The defaults of ConnectPolicy.
const (
	DefaultConnectAttempts   = 3
	DefaultConnectBackoff    = 500 * time.Millisecond
	DefaultConnectMaxBackoff = 5 * time.Second
)

// Tries is Attempts with the default applied.
func (p ConnectPolicy) Tries() int {
	if p.Attempts <= 0 {
		return DefaultConnectAttempts
	}
	return p.Attempts
}

// Wait is how long to sleep before try n (2 is the first retry).
func (p ConnectPolicy) Wait(n int) time.Duration {
	base, maxWait := p.Backoff, p.MaxBackoff
	if base <= 0 {
		base = DefaultConnectBackoff
	}
	if maxWait <= 0 {
		maxWait = DefaultConnectMaxBackoff
	}
	d := base
	for i := 2; i < n && d < maxWait; i++ {
		d *= 2
	}
	d = min(d, maxWait)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// Sleep waits d or until ctx ends; false when ctx ended.
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// permanent marks a failure more tries will not fix.
type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// NoRetry marks err as a failure more tries will not fix (a refused
// credential, a bad request, an unknown room): Connect returns it at once.
func NoRetry(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// IsNoRetry reports whether err was marked with NoRetry.
func IsNoRetry(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

// HandshakeError classifies a failed HTTP or websocket handshake: a 4xx
// answer other than 408 and 429 is the request's fault and marked NoRetry;
// everything else (no answer, a 5xx, a timeout) is worth another try.
func HandshakeError(err error, resp *http.Response) error {
	if err == nil {
		return nil
	}
	if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		return NoRetry(fmt.Errorf("HTTP %d: %w", resp.StatusCode, err))
	}
	return err
}

// Connect runs dial until it answers, retrying with the policy's backoff
// while the attempts last. A NoRetry error and a cancelled ctx come back at
// once. Every retry is a warning on logger (nil is fine) with what failed.
func Connect[T any](ctx context.Context, p ConnectPolicy, logger *slog.Logger, what string, dial func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	tries := p.Tries()
	for n := 1; ; n++ {
		out, err := dial(ctx)
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil || IsNoRetry(err) {
			return zero, err
		}
		if n >= tries {
			if tries > 1 {
				return zero, fmt.Errorf("%w (after %d attempts)", err, n)
			}
			return zero, err
		}
		wait := p.Wait(n + 1)
		logger.Warn("connect failed, retrying", "what", what, "attempt", n, "of", tries, "wait", wait.Round(time.Millisecond).String(), "err", err)
		if !Sleep(ctx, wait) {
			return zero, fmt.Errorf("%w (while waiting to retry %s: %v)", ctx.Err(), what, err)
		}
	}
}
