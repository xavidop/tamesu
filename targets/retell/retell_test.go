package retell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/livekit"
)

func jwt(sub string) string {
	claims, _ := json.Marshal(map[string]any{"sub": sub, "video": map[string]any{"roomJoin": true}})
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

type fakeSpeech struct{}

func (fakeSpeech) Synthesize(context.Context, target.SynthesizeRequest) (target.Audio, error) {
	return target.Audio{Rate: 48000}, nil
}
func (fakeSpeech) Transcribe(context.Context, target.TranscribeRequest) (target.Transcript, error) {
	return target.Transcript{}, nil
}

type fakeSession struct{ closed bool }

func (*fakeSession) Send(context.Context, target.UserTurn) error { return nil }
func (*fakeSession) Receive(context.Context) (target.AgentTurn, error) {
	return target.AgentTurn{Text: "hi"}, nil
}
func (f *fakeSession) Close(context.Context) (target.Artifacts, error) {
	f.closed = true
	return target.Artifacts{Events: []target.Event{{Kind: "joined"}}}, nil
}

func TestStartCreatesTheCallAndJoinsItsRoom(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/create-web-call", r.URL.Path)
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"call_id": "call_1", "access_token": jwt("web-user-9"), "call_status": "registered"})
	}))
	t.Cleanup(srv.Close)
	var joined livekit.Config
	fs := &fakeSession{}
	tgt := New("retell", Config{BaseURL: srv.URL, APIKey: "{{secrets.RETELL}}", AgentID: "agent_1", AgentVersion: "latest_published",
		Variables: map[string]string{"customer_name": "{{vars.name}}"}, Metadata: map[string]any{"suite": "x"}, Idle: time.Second, Timeout: 30 * time.Second, JoinTimeout: 20 * time.Second,
		Join: func(_ context.Context, _ *target.RunContext, cfg livekit.Config) (target.Session, error) {
			joined = cfg
			return fs, nil
		}})
	run := &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Speech: fakeSpeech{}, Render: func(s string) (string, error) {
		s = strings.ReplaceAll(s, "{{secrets.RETELL}}", "key-1")
		return strings.ReplaceAll(s, "{{vars.name}}", "Marta"), nil
	}}
	sess, err := tgt.Start(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, "Bearer key-1", auth)
	assert.Equal(t, map[string]any{"agent_id": "agent_1", "agent_version": "latest_published", "retell_llm_dynamic_variables": map[string]any{"customer_name": "Marta"}, "metadata": map[string]any{"suite": "x"}}, got)
	assert.Equal(t, DefaultLiveKitURL, joined.URL)
	assert.Equal(t, jwt("web-user-9"), joined.Token)
	assert.Equal(t, "web-user-9", joined.Identity, "the identity comes from the token")
	assert.Equal(t, "call_1", joined.Room)
	assert.Equal(t, livekit.ModeAudio, joined.Mode)
	assert.Equal(t, AgentIdentity, joined.AgentIdentityPrefix)
	assert.Equal(t, time.Second, joined.TurnIdle)
	assert.Equal(t, 30*time.Second, joined.TurnTimeout)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, fs.closed)
	assert.Equal(t, "retell_call", arts.Events[0].Kind)
	assert.Equal(t, "call_1", arts.Events[0].Detail)
}

func TestStartErrors(t *testing.T) {
	status, body := http.StatusCreated, map[string]any{"call_id": "c", "access_token": jwt("u")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	run := func() *target.RunContext {
		return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Speech: fakeSpeech{}, Render: func(s string) (string, error) { return s, nil }}
	}
	join := func(context.Context, *target.RunContext, livekit.Config) (target.Session, error) {
		return &fakeSession{}, nil
	}
	base := Config{BaseURL: srv.URL, APIKey: "k", AgentID: "a", Join: join}

	noSpeech := run()
	noSpeech.Speech = nil
	_, err := New("r", base).Start(context.Background(), noSpeech)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "speech models")

	c := base
	c.AgentID = ""
	_, err = New("r", c).Start(context.Background(), run())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent_id is required")

	status, body = http.StatusUnauthorized, map[string]any{"status": "error", "message": "API key is missing or invalid."}
	_, err = New("r", base).Start(context.Background(), run())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")

	status, body = http.StatusCreated, map[string]any{"call_id": "c", "access_token": jwt("u"), "transport": "gateway"}
	_, err = New("r", base).Start(context.Background(), run())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"gateway" transport`)

	status, body = http.StatusCreated, map[string]any{"call_id": "c", "access_token": "not-a-jwt"}
	_, err = New("r", base).Start(context.Background(), run())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a JWT")
}

func TestTokenIdentity(t *testing.T) {
	id, err := tokenIdentity(jwt("abc"))
	require.NoError(t, err)
	assert.Equal(t, "abc", id)
	_, err = tokenIdentity("x.y")
	require.Error(t, err)
}
