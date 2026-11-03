package target

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectPolicyDefaults(t *testing.T) {
	var p ConnectPolicy
	assert.Equal(t, DefaultConnectAttempts, p.Tries())
	assert.Equal(t, 1, ConnectPolicy{Attempts: 1}.Tries())
	for range 20 {
		w := p.Wait(2)
		assert.GreaterOrEqual(t, w, DefaultConnectBackoff/2)
		assert.LessOrEqual(t, w, DefaultConnectBackoff)
	}
	capped := ConnectPolicy{Backoff: time.Second, MaxBackoff: 3 * time.Second}
	assert.LessOrEqual(t, capped.Wait(9), 3*time.Second)
}

func TestHandshakeError(t *testing.T) {
	err := errors.New("bad handshake")
	assert.True(t, IsNoRetry(HandshakeError(err, &http.Response{StatusCode: 401})), "a refused key is final")
	assert.True(t, IsNoRetry(HandshakeError(err, &http.Response{StatusCode: 404})))
	assert.False(t, IsNoRetry(HandshakeError(err, &http.Response{StatusCode: 429})), "a rate limit is worth another try")
	assert.False(t, IsNoRetry(HandshakeError(err, &http.Response{StatusCode: 503})))
	assert.False(t, IsNoRetry(HandshakeError(err, nil)), "no answer at all is worth another try")
	assert.NoError(t, HandshakeError(nil, &http.Response{StatusCode: 401}))
	assert.ErrorContains(t, HandshakeError(err, &http.Response{StatusCode: 403}), "HTTP 403")
}

func TestConnect(t *testing.T) {
	fast := ConnectPolicy{Attempts: 3, Backoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	refused := errors.New("connection refused")

	t.Run("retries until it answers", func(t *testing.T) {
		calls := 0
		out, err := Connect(context.Background(), fast, nil, "ws", func(context.Context) (string, error) {
			calls++
			if calls < 3 {
				return "", refused
			}
			return "conn", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "conn", out)
		assert.Equal(t, 3, calls)
	})

	t.Run("gives up after the attempts", func(t *testing.T) {
		calls := 0
		_, err := Connect(context.Background(), fast, nil, "ws", func(context.Context) (string, error) {
			calls++
			return "", refused
		})
		assert.ErrorIs(t, err, refused)
		assert.ErrorContains(t, err, "(after 3 attempts)")
		assert.Equal(t, 3, calls)
	})

	t.Run("one attempt is the error as is", func(t *testing.T) {
		_, err := Connect(context.Background(), ConnectPolicy{Attempts: 1}, nil, "ws", func(context.Context) (string, error) { return "", refused })
		assert.Equal(t, refused, err)
	})

	t.Run("a final failure is not retried", func(t *testing.T) {
		calls := 0
		_, err := Connect(context.Background(), fast, nil, "ws", func(context.Context) (string, error) {
			calls++
			return "", NoRetry(errors.New("401"))
		})
		assert.ErrorContains(t, err, "401")
		assert.Equal(t, 1, calls)
	})

	t.Run("a cancelled caller stops the wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		_, err := Connect(ctx, ConnectPolicy{Attempts: 5, Backoff: time.Hour, MaxBackoff: time.Hour}, nil, "ws", func(context.Context) (string, error) {
			calls++
			return "", refused
		})
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, calls)
	})
}
