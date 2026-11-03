package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/firebase/genkit/go/core/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryWait(t *testing.T) {
	r := Retry{Backoff: 100 * time.Millisecond, MaxBackoff: 350 * time.Millisecond}
	for n, want := range map[int]time.Duration{2: 100 * time.Millisecond, 3: 200 * time.Millisecond, 4: 350 * time.Millisecond, 9: 350 * time.Millisecond} {
		for range 20 {
			got := r.wait(n)
			assert.GreaterOrEqual(t, got, want/2, "try %d", n)
			assert.LessOrEqual(t, got, want, "try %d", n)
		}
	}
	assert.Equal(t, DefaultRetryAttempts, Retry{}.attempts())
	assert.Equal(t, 1, Retry{Attempts: 1}.attempts())
	zero := Retry{}.wait(2)
	assert.GreaterOrEqual(t, zero, DefaultRetryBackoff/2)
	assert.LessOrEqual(t, zero, DefaultRetryBackoff)
}

func TestChain(t *testing.T) {
	assert.Equal(t, []string{"a/1", "b/2", "c/3"}, chain("a/1", []string{"b/2", "a/1"}, []string{"c/3", "", "b/2"}))
	assert.Equal(t, []string{"a/1"}, chain("a/1"))
	assert.Empty(t, chain(""))
}

func TestFallbacksFor(t *testing.T) {
	c := &Client{cfg: Config{Models: Models{Fallback: Fallbacks{Default: []string{"d/1"}, Judge: []string{"j/1"}}}}}
	assert.Equal(t, []string{"j/1"}, c.fallbacksFor("judge"), "the job's own list")
	assert.Equal(t, []string{"d/1"}, c.fallbacksFor("simulator"), "the default list for a job without one")
	assert.Equal(t, []string{"d/1"}, c.fallbacksFor("browser_agent"))
	assert.Equal(t, []string{"d/1"}, c.fallbacksFor("scenario_generator"))
}

func TestRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"genkit unavailable", status.Errorf(status.ErrUnavailable, "overloaded"), true},
		{"genkit resource exhausted", status.Errorf(status.ErrResourceExhausted, "quota"), true},
		{"genkit deadline", status.Errorf(status.ErrDeadlineExceeded, "slow"), true},
		{"genkit internal", status.Errorf(status.ErrInternal, "boom"), true},
		{"genkit wrapped", fmt.Errorf("judge: %w", status.Errorf(status.ErrUnavailable, "x")), true},
		{"genkit unauthenticated", status.Errorf(status.ErrUnauthenticated, "bad key"), false},
		{"genkit invalid argument", status.Errorf(status.ErrInvalidArgument, "bad request"), false},
		{"genkit not found", status.Errorf(status.ErrNotFound, "no such model"), false},
		{"genkit permission", status.Errorf(status.ErrPermissionDenied, "no"), false},
		{"bad output", badOutput("judgeRun", "m/1", errors.New("unexpected end of JSON"), "{"), true},
		{"net timeout", &net.DNSError{IsTimeout: true}, true},
		{"eof", io.ErrUnexpectedEOF, true},
		{"deadline", context.DeadlineExceeded, true},
		{"http 429 text", errors.New("POST \"https://api\": 429 Too Many Requests"), true},
		{"http 503 text", errors.New("Error 503, Message: The model is overloaded"), true},
		{"rate limit text", errors.New("rate limit reached for gpt-4.1"), true},
		{"http 400 text", errors.New("POST \"https://api\": 400 Bad Request"), false},
		{"plain", errors.New("prompt judge is not loaded"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, retryable(tt.err))
		})
	}
}

func TestTerminal(t *testing.T) {
	ctx := context.Background()
	assert.True(t, terminal(ctx, ErrBudgetExceeded))
	assert.True(t, terminal(ctx, fmt.Errorf("x: %w", ErrNoSpeechModel)))
	assert.True(t, terminal(ctx, context.Canceled))
	assert.False(t, terminal(ctx, status.Errorf(status.ErrUnavailable, "x")))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.True(t, terminal(cancelled, status.Errorf(status.ErrUnavailable, "x")), "the caller is gone")
}

func testClient(retry Retry) *Client {
	return &Client{cfg: Config{Retry: retry}, logger: slog.New(slog.DiscardHandler)}
}

func TestAttempt(t *testing.T) {
	fast := Retry{Attempts: 3, Backoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	down := status.Errorf(status.ErrUnavailable, "down")
	denied := status.Errorf(status.ErrUnauthenticated, "denied")

	t.Run("first try", func(t *testing.T) {
		c := testClient(fast)
		calls := 0
		out, err := attempt(context.Background(), c, "judge", []string{"a/1", "b/1"}, func(_ context.Context, m string) (string, error) {
			calls++
			return "from " + m, nil
		})
		require.NoError(t, err)
		assert.Equal(t, "from a/1", out)
		assert.Equal(t, 1, calls)
		assert.Equal(t, 0, c.Usage().Retries+c.Usage().Fallbacks)
	})

	t.Run("retries a transient failure on the same model", func(t *testing.T) {
		c := testClient(fast)
		var seen []string
		out, err := attempt(context.Background(), c, "judge", []string{"a/1", "b/1"}, func(_ context.Context, m string) (string, error) {
			seen = append(seen, m)
			if len(seen) < 3 {
				return "", down
			}
			return "ok", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "ok", out)
		assert.Equal(t, []string{"a/1", "a/1", "a/1"}, seen)
		assert.Equal(t, 2, c.Usage().Retries)
		assert.Equal(t, 0, c.Usage().Fallbacks)
	})

	t.Run("falls back once the attempts are spent", func(t *testing.T) {
		c := testClient(fast)
		var seen []string
		out, err := attempt(context.Background(), c, "judge", []string{"a/1", "b/1"}, func(_ context.Context, m string) (string, error) {
			seen = append(seen, m)
			if m == "a/1" {
				return "", down
			}
			return "from b", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "from b", out)
		assert.Equal(t, []string{"a/1", "a/1", "a/1", "b/1"}, seen)
		assert.Equal(t, 2, c.Usage().Retries)
		assert.Equal(t, 1, c.Usage().Fallbacks)
	})

	t.Run("a failure no retry fixes goes straight to the fallback", func(t *testing.T) {
		c := testClient(fast)
		var seen []string
		_, err := attempt(context.Background(), c, "judge", []string{"a/1", "b/1"}, func(_ context.Context, m string) (string, error) {
			seen = append(seen, m)
			if m == "a/1" {
				return "", denied
			}
			return "from b", nil
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"a/1", "b/1"}, seen)
		assert.Equal(t, 0, c.Usage().Retries)
		assert.Equal(t, 1, c.Usage().Fallbacks)
	})

	t.Run("the chain exhausted names what was tried", func(t *testing.T) {
		c := testClient(fast)
		_, err := attempt(context.Background(), c, "judge", []string{"a/1", "b/1"}, func(_ context.Context, m string) (string, error) {
			if m == "a/1" {
				return "", down
			}
			return "", fmt.Errorf("llm: judgeRun with %s: %w", m, denied)
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, denied, "the last error is the one returned")
		assert.Contains(t, err.Error(), "(tried a/1 3 times, then b/1 once)")
		assert.Equal(t, 2, c.Usage().Retries)
		assert.Equal(t, 1, c.Usage().Fallbacks)
	})

	t.Run("one model and one attempt is the error as is", func(t *testing.T) {
		c := testClient(Retry{Attempts: 1})
		_, err := attempt(context.Background(), c, "judge", []string{"a/1"}, func(context.Context, string) (string, error) { return "", down })
		assert.Equal(t, down, err)
	})

	t.Run("a spent budget comes back at once with the answer", func(t *testing.T) {
		c := testClient(fast)
		calls := 0
		out, err := attempt(context.Background(), c, "judge", []string{"a/1", "b/1"}, func(context.Context, string) (string, error) {
			calls++
			return "answer", ErrBudgetExceeded
		})
		assert.ErrorIs(t, err, ErrBudgetExceeded)
		assert.Equal(t, "answer", out, "the result of the call that broke the budget is kept")
		assert.Equal(t, 1, calls)
	})

	t.Run("a cancelled caller stops the retries", func(t *testing.T) {
		c := testClient(Retry{Attempts: 5, Backoff: time.Hour, MaxBackoff: time.Hour})
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		_, err := attempt(ctx, c, "judge", []string{"a/1", "b/1"}, func(context.Context, string) (string, error) {
			calls++
			return "", down
		})
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, calls, "the wait was interrupted, no second call")
	})

	t.Run("no model", func(t *testing.T) {
		c := testClient(fast)
		_, err := attempt(context.Background(), c, "tts", nil, func(context.Context, string) (string, error) { return "", nil })
		assert.ErrorContains(t, err, "no model configured")
	})
}
