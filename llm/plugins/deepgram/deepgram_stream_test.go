package deepgram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xavidop/tamesu/llm/plugins/speechstream"
)

// liveServer is a fake Deepgram live endpoint: it accepts the socket and hands
// it to serve, which runs on the handler's goroutine.
func liveServer(t *testing.T, serve func(ctx context.Context, c *websocket.Conn, r *http.Request)) *Deepgram {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		serve(r.Context(), c, r)
	}))
	t.Cleanup(srv.Close)
	return &Deepgram{APIKey: "dg-test", WSBaseURL: "ws://" + strings.TrimPrefix(srv.URL, "http://"), HTTPClient: srv.Client()}
}

// readControl reads frames until a text one, summing the binary bytes seen.
func readControl(ctx context.Context, c *websocket.Conn, audioBytes *atomic.Int64) (map[string]string, error) {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ == websocket.MessageBinary {
			audioBytes.Add(int64(len(data)))
			continue
		}
		var msg map[string]string
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return msg, nil
	}
}

func results(transcript string, confidence float64, final bool) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "Results", "is_final": final, "speech_final": final,
		"channel": map[string]any{"alternatives": []map[string]any{{"transcript": transcript, "confidence": confidence}}},
	})
	return b
}

func TestOpenTranscription(t *testing.T) {
	var audioBytes atomic.Int64
	serverDone := make(chan error, 1)
	d := liveServer(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		assert.Equal(t, "/v1/listen", r.URL.Path)
		assert.Equal(t, "Token dg-test", r.Header.Get("Authorization"))
		q := r.URL.Query()
		assert.Equal(t, "nova-3", q.Get("model"))
		assert.Equal(t, "linear16", q.Get("encoding"))
		assert.Equal(t, "16000", q.Get("sample_rate"))
		assert.Equal(t, "1", q.Get("channels"))
		assert.Equal(t, "true", q.Get("punctuate"))
		assert.Equal(t, "true", q.Get("smart_format"))
		assert.Equal(t, "false", q.Get("interim_results"))
		assert.Equal(t, "en-GB", q.Get("language"))

		msg, err := readControl(ctx, c, &audioBytes)
		if err != nil {
			serverDone <- err
			return
		}
		assert.Equal(t, "CloseStream", msg["type"])
		for _, m := range [][]byte{
			[]byte(`{"type":"SpeechStarted","timestamp":0.1}`),
			results("hello", 0.5, false),
			results("Hello there.", 0.9, true),
			results("", 0.1, true),
			[]byte(`{"type":"UtteranceEnd","last_word_end":1.2}`),
			results(" How are you? ", 0.7, true),
			[]byte(`{"type":"Metadata","request_id":"r1","duration":0.3}`),
		} {
			if err := c.Write(ctx, websocket.MessageText, m); err != nil {
				serverDone <- err
				return
			}
		}
		// The close handshake completes once the client closes its side.
		serverDone <- c.Close(websocket.StatusNormalClosure, "")
	})

	ctx := context.Background()
	tr, err := d.OpenTranscription(ctx, speechstream.Options{Model: "nova-3", Rate: 16000, Language: "en-GB"})
	require.NoError(t, err)
	frame := make([]int16, 1600)
	for i := range frame {
		frame[i] = int16(i - 800)
	}
	for range 3 {
		require.NoError(t, tr.Write(ctx, frame))
	}
	res, err := tr.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Hello there. How are you?", res.Text)
	assert.InDelta(t, 0.8, res.Confidence, 1e-9)
	assert.InDelta(t, 0.3, res.AudioSeconds, 1e-9)

	select {
	case err := <-serverDone:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the server never finished")
	}
	assert.Equal(t, int64(3*1600*2), audioBytes.Load())
	// The socket is released: nothing more can be written.
	assert.Error(t, tr.Write(ctx, frame))
}

func TestOpenTranscriptionUnsupported(t *testing.T) {
	d := liveServer(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		t.Error("an Aura model must not dial")
	})
	for _, m := range []string{"aura-2", "aura-2-thalia-en", "aura"} {
		_, err := d.OpenTranscription(context.Background(), speechstream.Options{Model: m, Rate: 16000})
		assert.ErrorIs(t, err, speechstream.ErrUnsupported, m)
	}
	_, err := d.OpenTranscription(context.Background(), speechstream.Options{Model: "nova-3"})
	assert.ErrorContains(t, err, "sample rate")
	_, err = (&Deepgram{WSBaseURL: d.WSBaseURL}).OpenTranscription(context.Background(), speechstream.Options{Model: "nova-3", Rate: 16000})
	assert.ErrorContains(t, err, "no API key")
}

func TestOpenTranscriptionDialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"err_code":"INVALID_AUTH","err_msg":"Invalid credentials."}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	d := &Deepgram{APIKey: "bad", WSBaseURL: "ws://" + strings.TrimPrefix(srv.URL, "http://"), HTTPClient: srv.Client()}
	_, err := d.OpenTranscription(context.Background(), speechstream.Options{Model: "nova-2", Rate: 8000})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "deepgram: "), err.Error())
	assert.ErrorContains(t, err, "401")
	assert.ErrorContains(t, err, "INVALID_AUTH")
}

func TestOpenTranscriptionServerError(t *testing.T) {
	d := liveServer(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"Error","description":"Audio not recognized."}`))
		_ = c.Close(websocket.StatusPolicyViolation, "bad audio")
	})
	ctx := context.Background()
	tr, err := d.OpenTranscription(ctx, speechstream.Options{Model: "nova-2", Rate: 8000})
	require.NoError(t, err)
	// The first frame goes through; the ones after it fail once the error
	// lands, or Close reports it.
	require.NoError(t, tr.Write(ctx, make([]int16, 80)))
	var writeErr error
	for range 200 {
		if writeErr = tr.Write(ctx, make([]int16, 80)); writeErr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, closeErr := tr.Close(closeCtx)
	require.Error(t, closeErr)
	assert.ErrorContains(t, closeErr, "deepgram: Audio not recognized.")
	if writeErr != nil {
		assert.ErrorContains(t, writeErr, "deepgram: Audio not recognized.")
	}
	assert.Empty(t, res.Text)
}

func TestOpenTranscriptionAbruptClose(t *testing.T) {
	d := liveServer(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
		_ = c.CloseNow()
	})
	ctx := context.Background()
	tr, err := d.OpenTranscription(ctx, speechstream.Options{Model: "base", Rate: 8000})
	require.NoError(t, err)
	require.NoError(t, tr.Write(ctx, make([]int16, 80)))
	var writeErr error
	for range 200 {
		if writeErr = tr.Write(ctx, make([]int16, 80)); writeErr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, closeErr := tr.Close(closeCtx)
	require.Error(t, closeErr)
	assert.True(t, strings.HasPrefix(closeErr.Error(), "deepgram: "), closeErr.Error())
	if writeErr != nil {
		assert.True(t, strings.HasPrefix(writeErr.Error(), "deepgram: "), writeErr.Error())
	}
	// A second Close after the failure is still safe and still reports it.
	_, again := tr.Close(closeCtx)
	assert.Error(t, again)
}

func TestOpenTranscriptionCloseCancelled(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	d := liveServer(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		// Swallow everything, never answer CloseStream.
		for {
			if _, _, err := c.Read(ctx); err != nil {
				break
			}
		}
		<-release
	})
	ctx := context.Background()
	tr, err := d.OpenTranscription(ctx, speechstream.Options{Model: "enhanced", Rate: 16000})
	require.NoError(t, err)
	require.NoError(t, tr.Write(ctx, make([]int16, 16000)))
	closeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := tr.Close(closeCtx)
	assert.Less(t, time.Since(start), 3*time.Second)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), err.Error())
	assert.True(t, strings.HasPrefix(err.Error(), "deepgram: "), err.Error())
	assert.InDelta(t, 1.0, res.AudioSeconds, 1e-9)
}

func TestIsStreaming(t *testing.T) {
	for _, m := range DefaultModels {
		assert.True(t, IsStreaming(m), m)
	}
	for _, m := range DefaultTTSModels {
		assert.False(t, IsStreaming(m), m)
	}
	assert.True(t, IsStreaming("nova-2-phonecall"))
	assert.False(t, IsStreaming("aura-2-apollo-en"))
}
