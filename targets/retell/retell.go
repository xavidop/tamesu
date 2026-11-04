// Package retell is the target for a Retell agent: tamesu creates a web call
// with Retell's API and joins the LiveKit room the call runs in, with the
// token Retell issued, as the browser SDK does. The conversation itself
// (speaking, listening, barge-in, the recording and the metrics) is the
// livekit target's in audio mode; this package adds the call creation, the
// token and Retell's participant layout.
package retell

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/livekit"
)

// Defaults.
const (
	DefaultBaseURL = "https://api.retellai.com"
	// DefaultLiveKitURL is the LiveKit cloud Retell's web calls run on, the
	// one its browser SDK connects to.
	DefaultLiveKitURL = "wss://retell-ai-4ihahnq7.livekit.cloud"
	// AgentIdentity is the identity of Retell's server participant, which
	// publishes the agent's audio and events.
	AgentIdentity  = "server"
	DefaultTimeout = 60 * time.Second
	// WebCallPath is the endpoint that issues LiveKit tokens; Retell's newer
	// v3 endpoint issues tokens for its own WebRTC gateway, which tamesu
	// cannot join.
	WebCallPath = "/v2/create-web-call"
)

// Config is the target's configuration.
type Config struct {
	// BaseURL is the Retell API; empty means DefaultBaseURL.
	BaseURL string
	// APIKey is the Retell API key ({{secrets.RETELL_API_KEY}}).
	APIKey string
	// AgentID is the agent to call; AgentVersion picks a version.
	AgentID      string
	AgentVersion string
	// Variables are the retell_llm_dynamic_variables (templates render);
	// Metadata is stored on the call.
	Variables map[string]string
	Metadata  map[string]any
	// LiveKitURL overrides the room server; empty means DefaultLiveKitURL.
	LiveKitURL string
	// JoinTimeout bounds the wait for the agent; Idle ends a turn after
	// this silence; Timeout bounds one turn.
	JoinTimeout time.Duration
	Idle        time.Duration
	Timeout     time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
	// Connect is how creating the call and joining the room are retried.
	Connect target.ConnectPolicy
	// Join overrides how the room is joined (tests); nil joins with the
	// livekit target.
	Join func(ctx context.Context, run *target.RunContext, cfg livekit.Config) (target.Session, error)
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.LiveKitURL == "" {
		cfg.LiveKitURL = DefaultLiveKitURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	if cfg.Join == nil {
		cfg.Join = func(ctx context.Context, run *target.RunContext, lk livekit.Config) (target.Session, error) {
			return livekit.New(name, lk).Start(ctx, run)
		}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the retell target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// WebCall is what create-web-call answers, as far as the target reads it.
type WebCall struct {
	CallID      string `json:"call_id"`
	AccessToken string `json:"access_token"`
	Transport   string `json:"transport"`
	Status      string `json:"call_status"`
	Message     string `json:"message"`
}

// Start creates the web call and joins its room.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if run.Speech == nil {
		return nil, fmt.Errorf("retell %s: the retell target needs the speech models: set speech.tts.model and speech.stt.model in tamesu.yaml", t.name)
	}
	apiKey, err := run.Render(t.cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("retell %s: api_key: %w", t.name, err)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("retell %s: api_key is empty", t.name)
	}
	agentID, err := run.Render(t.cfg.AgentID)
	if err != nil {
		return nil, fmt.Errorf("retell %s: agent_id: %w", t.name, err)
	}
	if agentID == "" {
		return nil, fmt.Errorf("retell %s: agent_id is required", t.name)
	}
	body := map[string]any{"agent_id": agentID}
	if t.cfg.AgentVersion != "" {
		body["agent_version"] = t.cfg.AgentVersion
	}
	if len(t.cfg.Variables) > 0 {
		vars := map[string]any{}
		for k, v := range t.cfg.Variables {
			rv, err := run.Render(v)
			if err != nil {
				return nil, fmt.Errorf("retell %s: variables.%s: %w", t.name, k, err)
			}
			vars[k] = rv
		}
		body["retell_llm_dynamic_variables"] = vars
	}
	if t.cfg.Metadata != nil {
		body["metadata"] = t.cfg.Metadata
	}
	call, err := target.Connect(ctx, t.cfg.Connect, run.Logger, "retell "+t.name+" create web call", func(ctx context.Context) (WebCall, error) {
		return t.createWebCall(ctx, apiKey, body)
	})
	if err != nil {
		return nil, fmt.Errorf("retell %s: create web call: %w", t.name, err)
	}
	if call.Transport != "" && call.Transport != "livekit" {
		return nil, fmt.Errorf("retell %s: the call %s runs on Retell's %q transport, which tamesu cannot join; the %s endpoint issues LiveKit tokens", t.name, call.CallID, call.Transport, WebCallPath)
	}
	identity, err := tokenIdentity(call.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("retell %s: access token: %w", t.name, err)
	}
	lk := livekit.Config{
		URL: t.cfg.LiveKitURL, Token: call.AccessToken, Identity: identity, Room: call.CallID,
		Mode: livekit.ModeAudio, AgentIdentityPrefix: AgentIdentity,
		JoinTimeout: t.cfg.JoinTimeout, TurnIdle: t.cfg.Idle, TurnTimeout: t.cfg.Timeout, Connect: t.cfg.Connect,
	}
	sess, err := t.cfg.Join(ctx, run, lk)
	if err != nil {
		return nil, fmt.Errorf("retell %s: call %s: %w", t.name, call.CallID, err)
	}
	return &session{Session: sess, callID: call.CallID}, nil
}

// createWebCall performs POST /v2/create-web-call.
func (t *Target) createWebCall(ctx context.Context, apiKey string, body map[string]any) (WebCall, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return WebCall{}, target.NoRetry(err)
	}
	rctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, strings.TrimRight(t.cfg.BaseURL, "/")+WebCallPath, bytes.NewReader(b))
	if err != nil {
		return WebCall{}, target.NoRetry(err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		return WebCall{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout:
		return WebCall{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 300:
		return WebCall{}, target.NoRetry(fmt.Errorf("HTTP %d: %s", resp.StatusCode, excerpt(raw)))
	}
	var out WebCall
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return WebCall{}, target.NoRetry(fmt.Errorf("unexpected response: %s", excerpt(raw)))
	}
	return out, nil
}

func excerpt(b []byte) string {
	str := strings.TrimSpace(string(b))
	if len(str) > 300 {
		return str[:300] + "..."
	}
	return str
}

// tokenIdentity reads the identity (sub) of a LiveKit access token without
// verifying it; the server verifies.
func tokenIdentity(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", fmt.Errorf("claims: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("claims: %w", err)
	}
	if claims.Sub == "" {
		return "", errors.New("the token has no identity (sub)")
	}
	return claims.Sub, nil
}

// session is the room session with the call id on its artifacts.
type session struct {
	target.Session
	callID string
}

// Close ends the room session and adds the call to its events.
func (s *session) Close(ctx context.Context) (target.Artifacts, error) {
	arts, err := s.Session.Close(ctx)
	arts.Events = append([]target.Event{{At: time.Now(), Kind: "retell_call", Detail: s.callID}}, arts.Events...)
	return arts, err
}
