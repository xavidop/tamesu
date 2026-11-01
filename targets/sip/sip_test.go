package sip

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/livekit"
)

// fakeAPI records the SIP API calls.
type fakeAPI struct {
	mu      sync.Mutex
	created []ParticipantRequest
	removed []string
	err     error
}

func (f *fakeAPI) CreateParticipant(_ context.Context, req ParticipantRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req)
	return f.err
}

func (f *fakeAPI) RemoveParticipant(_ context.Context, room, identity string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, room+"/"+identity)
	return nil
}

func render(s string) (string, error) {
	s = strings.ReplaceAll(s, "{{secrets.LIVEKIT_API_KEY}}", "devkey")
	s = strings.ReplaceAll(s, "{{vars.agent_number}}", "+18005550100")
	s = strings.ReplaceAll(s, "{{sip.inbound_number}}", "+34910000000")
	s = strings.ReplaceAll(s, "{{secrets.API_TOKEN}}", "tok-123")
	return s, nil
}

func TestOutboundPlaceAndHangup(t *testing.T) {
	api := &fakeAPI{}
	o := &outbound{cfg: Config{TrunkID: "ST_1", To: "+18005550100", From: "+34910000000", RingTimeout: 20 * time.Second, MaxCallDuration: 2 * time.Minute, Headers: map[string]string{"X-Test": "1"}}, api: api}
	info, err := o.Place(context.Background(), livekit.RoomInfo{Room: "tamesu-sip-1"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(info.ParticipantIdentity, "sip-agent-"))
	require.Len(t, api.created, 1)
	req := api.created[0]
	assert.Equal(t, "ST_1", req.TrunkID)
	assert.Equal(t, "+18005550100", req.To)
	assert.Equal(t, "+34910000000", req.From)
	assert.Equal(t, "tamesu-sip-1", req.Room)
	assert.Equal(t, info.ParticipantIdentity, req.Identity)
	assert.Equal(t, 20*time.Second, req.RingTimeout)
	assert.Equal(t, map[string]string{"X-Test": "1"}, req.Headers)
	assert.Equal(t, 2*time.Minute, o.MaxCallDuration())

	require.NoError(t, o.Hangup(context.Background(), livekit.RoomInfo{Room: "tamesu-sip-1", AgentIdentity: info.ParticipantIdentity}))
	assert.Equal(t, []string{"tamesu-sip-1/" + info.ParticipantIdentity}, api.removed)
}

func TestOutboundFailuresMapToCallReasons(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
		status int
	}{
		{"busy", errors.New("sip: 486 Busy Here"), livekit.EndedByBusy, 486},
		{"declined", errors.New("call declined (603)"), livekit.EndedByBusy, 603},
		{"unavailable", errors.New("480 Temporarily Unavailable"), livekit.EndedByNoAnswer, 480},
		{"ring timeout", errors.New("ringing timeout: no answer"), livekit.EndedByNoAnswer, 408},
		{"other", errors.New("trunk not found"), livekit.EndedByError, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &outbound{cfg: Config{TrunkID: "ST_1", To: "+1"}, api: &fakeAPI{err: tt.err}}
			_, err := o.Place(context.Background(), livekit.RoomInfo{Room: "r"})
			var ce *livekit.CallError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, tt.reason, ce.Reason)
			assert.Equal(t, tt.status, ce.Status)
			assert.ErrorIs(t, err, tt.err)
		})
	}
}

func TestInboundHTTPTrigger(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b := make([]byte, 1024)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	in := &inbound{render: render, unlock: func() {}, api: &fakeAPI{}, cfg: Config{
		InboundNumber: "+34910000000", HTTPClient: srv.Client(), MaxCallDuration: time.Minute,
		Trigger: &Trigger{HTTP: &HTTPTrigger{URL: srv.URL + "/campaigns/test-call", Body: map[string]any{"to": "{{sip.inbound_number}}", "tags": []any{"tamesu"}}, Headers: map[string]string{"Authorization": "Bearer {{secrets.API_TOKEN}}"}}},
	}}
	info, err := in.Place(context.Background(), livekit.RoomInfo{Room: DefaultInboundRoom})
	require.NoError(t, err)
	assert.Empty(t, info.ParticipantIdentity, "any SIP participant that arrives is the caller")
	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/campaigns/test-call", got.URL.Path)
	assert.Equal(t, "Bearer tok-123", got.Header.Get("Authorization"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"to":"+34910000000","tags":["tamesu"]}`, body)

	// A failing trigger is a call error.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusForbidden) }))
	defer srv2.Close()
	in.cfg.Trigger = &Trigger{HTTP: &HTTPTrigger{URL: srv2.URL, Method: http.MethodPut}}
	in.cfg.HTTPClient = srv2.Client()
	_, err = in.Place(context.Background(), livekit.RoomInfo{Room: DefaultInboundRoom})
	var ce *livekit.CallError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, livekit.EndedByError, ce.Reason)
	assert.ErrorContains(t, err, "403")
}

func TestInboundCommandTrigger(t *testing.T) {
	dir := t.TempDir()
	in := &inbound{render: render, unlock: func() {}, api: &fakeAPI{}, cfg: Config{
		InboundNumber: "+34910000000",
		Trigger:       &Trigger{Command: "echo \"$TAMESU_INBOUND_NUMBER $TAMESU_ROOM\" > " + dir + "/out.txt"},
	}}
	_, err := in.Place(context.Background(), livekit.RoomInfo{Room: "tamesu-sip-inbound"})
	require.NoError(t, err)
	b, err := readFile(dir + "/out.txt")
	require.NoError(t, err)
	assert.Equal(t, "+34910000000 tamesu-sip-inbound\n", b)
	in.cfg.Trigger = &Trigger{Command: "exit 3"}
	_, err = in.Place(context.Background(), livekit.RoomInfo{})
	assert.ErrorContains(t, err, "exit status 3")
	require.NoError(t, in.Hangup(context.Background(), livekit.RoomInfo{Room: "r", AgentIdentity: "sip_+1"}))
	assert.Equal(t, []string{"r/sip_+1"}, in.api.(*fakeAPI).removed)
}

func TestStartGuards(t *testing.T) {
	run := &target.RunContext{TestName: "t", Render: render}
	api := &fakeAPI{}
	_, err := New("phone", Config{LiveKitURL: "ws://lk", API: api, To: "{{vars.agent_number}}", TrunkID: "ST_1", AllowedNumbers: []string{"+34*"}}).Start(context.Background(), run)
	assert.ErrorIs(t, err, ErrNumberNotAllowed, "the rendered number is checked against allowed_numbers")
	assert.Empty(t, api.created, "nothing was dialed")
	_, err = New("phone", Config{LiveKitURL: "", API: api, To: "+1"}).Start(context.Background(), run)
	assert.ErrorContains(t, err, "sip.livekit_url is required")
	_, err = New("phone", Config{LiveKitURL: "ws://lk", API: api, To: "+18005550100", AllowedNumbers: []string{"+1800*"}}).Start(context.Background(), run)
	assert.ErrorContains(t, err, "trunk_id")
	_, err = New("phone", Config{LiveKitURL: "ws://lk", API: api, Direction: Inbound}).Start(context.Background(), run)
	assert.ErrorContains(t, err, "trigger.http or trigger.command")
	_, err = New("phone", Config{LiveKitURL: "ws://lk", API: api, Direction: "sideways"}).Start(context.Background(), run)
	assert.ErrorContains(t, err, "direction must be outbound or inbound")
	_, err = New("phone", Config{LiveKitURL: "ws://lk", To: "+18005550100", TrunkID: "ST_1", AllowedNumbers: []string{"*"}}).Start(context.Background(), run)
	assert.ErrorContains(t, err, "LIVEKIT_API_KEY and LIVEKIT_API_SECRET are required", "no API fake: the SDK needs keys")
}

// fakeSetup is an in-memory LiveKit SIP API.
type fakeSetup struct {
	outbound []Trunk
	inbound  []Trunk
	rules    []Rule
	calls    []string
	n        int
}

func (f *fakeSetup) id(prefix string) string {
	f.n++
	return prefix + strings.Repeat("x", 3) + string(rune('0'+f.n))
}
func (f *fakeSetup) ListOutboundTrunks(context.Context) ([]Trunk, error) {
	f.calls = append(f.calls, "list-out")
	return f.outbound, nil
}
func (f *fakeSetup) CreateOutboundTrunk(_ context.Context, name string, t TrunkConfig) (string, error) {
	f.calls = append(f.calls, "create-out "+t.Address)
	id := f.id("ST_")
	f.outbound = append(f.outbound, Trunk{ID: id, Name: name, Numbers: t.Numbers})
	return id, nil
}
func (f *fakeSetup) ListInboundTrunks(context.Context) ([]Trunk, error) {
	f.calls = append(f.calls, "list-in")
	return f.inbound, nil
}
func (f *fakeSetup) CreateInboundTrunk(_ context.Context, name string, numbers, allowed []string) (string, error) {
	f.calls = append(f.calls, "create-in "+strings.Join(numbers, ","))
	id := f.id("ST_")
	f.inbound = append(f.inbound, Trunk{ID: id, Name: name, Numbers: numbers})
	return id, nil
}
func (f *fakeSetup) ListDispatchRules(context.Context) ([]Rule, error) {
	f.calls = append(f.calls, "list-rules")
	return f.rules, nil
}
func (f *fakeSetup) CreateDispatchRule(_ context.Context, name, room string, trunkIDs []string) (string, error) {
	f.calls = append(f.calls, "create-rule "+room)
	id := f.id("SDR_")
	f.rules = append(f.rules, Rule{ID: id, Name: name, Room: room, TrunkIDs: trunkIDs})
	return id, nil
}

func TestSetupIsIdempotent(t *testing.T) {
	api := &fakeSetup{}
	cfg := SetupConfig{Trunk: TrunkConfig{Address: "sip.twilio.com", Numbers: []string{"+34910000000"}, AuthUsername: "u", AuthPassword: "p"}}
	res, err := Setup(context.Background(), api, cfg)
	require.NoError(t, err)
	assert.Len(t, res.Created, 3)
	assert.Empty(t, res.Reused)
	assert.Equal(t, []string{"list-out", "create-out sip.twilio.com", "list-in", "create-in +34910000000", "list-rules", "create-rule tamesu-sip-inbound"}, api.calls)
	assert.Equal(t, "sip:\n  outbound_trunk_id: "+res.OutboundTrunkID+"\n  inbound_trunk_id: "+res.InboundTrunkID+"\n  dispatch_rule_id: "+res.DispatchRuleID+"\n", res.YAML())

	api.calls = nil
	again, err := Setup(context.Background(), api, cfg)
	require.NoError(t, err)
	assert.Equal(t, res.OutboundTrunkID, again.OutboundTrunkID)
	assert.Equal(t, res.InboundTrunkID, again.InboundTrunkID)
	assert.Equal(t, res.DispatchRuleID, again.DispatchRuleID)
	assert.Empty(t, again.Created, "the second run creates nothing")
	assert.Len(t, again.Reused, 3)
	assert.Equal(t, []string{"list-out", "list-in", "list-rules"}, api.calls)

	_, err = Setup(context.Background(), &fakeSetup{}, SetupConfig{Outbound: true})
	assert.ErrorContains(t, err, "sip.trunk.address is required")
	_, err = Setup(context.Background(), &fakeSetup{}, SetupConfig{Inbound: true})
	assert.ErrorContains(t, err, "sip.trunk.numbers")
	assert.Equal(t, "https://lk.example", HTTPURL("wss://lk.example"))
	assert.Equal(t, "http://localhost:7880", HTTPURL("ws://localhost:7880"))
}
