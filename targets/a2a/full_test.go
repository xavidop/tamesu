package a2a

import (
	"context"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/a2aserver"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/a2a/a2abind"
)

// remoteAgent starts tamesu's own A2A server with every binding, as the
// agent under test.
func remoteAgent(t *testing.T, cfg a2aserver.Config) *a2aserver.Server {
	t.Helper()
	cfg.Bindings = []a2aserver.Binding{a2abind.New()}
	if cfg.Name == "" {
		cfg.Name = "Billing"
	}
	if cfg.Skills == nil {
		cfg.Skills = []a2aserver.Skill{{ID: "refund"}}
	}
	if cfg.Default.Text == "" && len(cfg.Replies) == 0 {
		cfg.Default = Replyf("Refund issued, reference RF-889", map[string]any{"reference": "RF-889"})
		cfg.Replies = []a2aserver.Reply{{When: regexp.MustCompile(`(?i)^refund$`), Text: "Which order?", State: a2aserver.StateInputRequired}}
	}
	srv, err := a2aserver.Start(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// Replyf is a reply with words and data.
func Replyf(text string, data map[string]any) a2aserver.Reply {
	return a2aserver.Reply{Text: text, Data: data}
}

func interfaceURL(t *testing.T, srv *a2aserver.Server, transport string) string {
	t.Helper()
	for _, in := range srv.Interfaces() {
		if in.Transport == transport {
			return in.URL
		}
	}
	t.Fatalf("no %s interface on the card", transport)
	return ""
}

func converse(t *testing.T, tgt target.Target, texts ...string) ([]target.AgentTurn, target.Artifacts) {
	t.Helper()
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	var turns []target.AgentTurn
	for _, text := range texts {
		require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: text}))
		turn, err := sess.Receive(context.Background())
		require.NoError(t, err)
		turns = append(turns, turn)
	}
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	return turns, arts
}

func eventKinds(arts target.Artifacts) []string {
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// TestEveryTransport runs the same conversation over JSON-RPC, HTTP+JSON and
// gRPC, blocking and streaming, against tamesu's own server.
func TestEveryTransport(t *testing.T) {
	srv := remoteAgent(t, a2aserver.Config{})
	cases := []struct {
		name      string
		cfg       Config
		transport string // as the server records it
	}{
		{"jsonrpc", Config{URL: srv.URL() + "/a2a"}, a2aserver.TransportJSONRPC},
		{"http", Config{URL: srv.URL(), Transport: TransportHTTP}, a2aserver.TransportHTTPJSON},
		{"grpc", Config{URL: interfaceURL(t, srv, a2aserver.TransportGRPC)}, a2aserver.TransportGRPC},
		{"grpc via the card", Config{URL: srv.URL() + "/a2a", Transport: TransportGRPC}, a2aserver.TransportGRPC},
		{"http via the card", Config{URL: srv.URL() + "/a2a", Transport: TransportHTTP}, a2aserver.TransportHTTPJSON},
	}
	for _, stream := range []bool{false, true} {
		for _, tc := range cases {
			name := tc.name
			if stream {
				name += " stream"
			}
			t.Run(name, func(t *testing.T) {
				before := len(srv.Delegations())
				cfg := tc.cfg
				cfg.Stream = stream
				cfg.Timeout = 5 * time.Second
				cfg.PollInterval = 10 * time.Millisecond
				cfg.Headers = map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"}
				tgt := New("billing", cfg)
				turns, arts := converse(t, tgt, "refund", "ORD-1")
				assert.Equal(t, "Which order?", turns[0].Text)
				assert.Equal(t, `Refund issued, reference RF-889{"reference":"RF-889"}`, turns[1].Text)
				assert.Equal(t, 1.0, arts.Metrics["a2a_tasks"], "one task carried both turns")
				assert.Equal(t, 1.0, arts.Metrics["a2a_skills"])
				kinds := eventKinds(arts)
				assert.Equal(t, "card", kinds[0])
				assert.Contains(t, kinds, "transport")
				assert.Contains(t, kinds, "task_input_required")
				assert.Contains(t, kinds, "task_completed")
				calls := srv.Delegations()[before:]
				require.Len(t, calls, 2)
				assert.Equal(t, tc.transport, calls[0].Transport)
				assert.Equal(t, "refund", calls[0].Message)
				assert.Equal(t, stream, calls[0].Streamed)
				for _, e := range arts.Events {
					if e.Kind == "transport" {
						assert.True(t, strings.HasPrefix(e.Detail, strings.Split(tc.name, " ")[0]+" at "), e.Detail)
					}
				}
			})
		}
	}
}

func TestChooseTransport(t *testing.T) {
	card := &Card{URL: "https://agent/a2a", PreferredTransport: "JSONRPC", AdditionalInterfaces: []Interface{
		{Transport: "GRPC", URL: "grpcs://agent:443"}, {Transport: "HTTP+JSON", URL: "https://agent/rest"}}}
	for _, tc := range []struct {
		configured, url string
		card            *Card
		kind, endpoint  string
	}{
		{"", "https://agent/a2a", card, TransportJSONRPC, "https://agent/a2a"},
		{"", "grpcs://agent:443", card, TransportGRPC, "grpcs://agent:443"},
		{"", "https://agent/rest", card, TransportHTTP, "https://agent/rest"},
		{"grpc", "https://agent/a2a", card, TransportGRPC, "grpcs://agent:443"},
		{"http", "https://agent/a2a", card, TransportHTTP, "https://agent/rest"},
		{"http", "https://other/x", nil, TransportHTTP, "https://other/x"},
		{"", "https://other/x", nil, TransportJSONRPC, "https://other/x"},
		{"", "https://other/x", &Card{URL: "https://agent/x", PreferredTransport: "HTTP+JSON"}, TransportJSONRPC, "https://other/x"},
		{"", "https://agent/x", &Card{URL: "https://agent/x/", PreferredTransport: "HTTP+JSON"}, TransportHTTP, "https://agent/x"},
	} {
		kind, endpoint := chooseTransport(tc.configured, tc.url, tc.card)
		assert.Equal(t, tc.kind, kind, "%+v", tc)
		assert.Equal(t, tc.endpoint, endpoint, "%+v", tc)
	}
	addr, tls := grpcAddress("grpcs://agent:443")
	assert.Equal(t, "agent:443", addr)
	assert.True(t, tls)
	addr, tls = grpcAddress("127.0.0.1:9000")
	assert.Equal(t, "127.0.0.1:9000", addr)
	assert.False(t, tls)
	_, tls = grpcAddress("agent.example.com:9000")
	assert.True(t, tls)
}

// TestPushNotifications has the agent finish its tasks by push notification
// instead of being polled, over JSON-RPC and gRPC.
func TestPushNotifications(t *testing.T) {
	srv := remoteAgent(t, a2aserver.Config{Default: Replyf("Done by push", nil)})
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"jsonrpc", Config{URL: srv.URL() + "/a2a"}},
		{"grpc", Config{URL: interfaceURL(t, srv, a2aserver.TransportGRPC)}},
		{"http", Config{URL: srv.URL(), Transport: TransportHTTP}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.Push = &Push{}
			cfg.Timeout = 5 * time.Second
			cfg.PollInterval = 200 * time.Millisecond // the safety poll stays out of the way on a slow runner
			tgt := New("billing", cfg)
			t.Cleanup(tgt.(*Target).Shutdown)
			turns, arts := converse(t, tgt, "hello", "again")
			assert.Equal(t, "Done by push", turns[0].Text)
			assert.Equal(t, "Done by push", turns[1].Text)
			assert.Equal(t, 2.0, arts.Metrics["a2a_pushes"], "one notification per turn")
			assert.Equal(t, 2.0, arts.Metrics["a2a_requests"], "no polling was needed")
			assert.Contains(t, eventKinds(arts), "push_received")
			assert.NotEmpty(t, tgt.(*Target).PushURL())
			calls := srv.Delegations()
			assert.True(t, calls[len(calls)-1].Pushed)
		})
	}
}

func TestPushFallsBackWhenTheCardSaysNo(t *testing.T) {
	a := newAgent(reply{text: "plain"})
	tgt, _ := newTarget(t, a, Config{Push: &Push{}})
	turns, arts := converse(t, tgt, "hi")
	assert.Equal(t, "plain", turns[0].Text)
	assert.Contains(t, eventKinds(arts), "no_push")
	assert.NotContains(t, arts.Metrics, "a2a_pushes")
	assert.Empty(t, tgt.(*Target).PushURL())
}

func TestDroppedStreamIsResubscribed(t *testing.T) {
	a := newAgent(reply{text: "streamed reply here", drop: true})
	tgt, _ := newTarget(t, a, Config{Stream: true})
	turns, arts := converse(t, tgt, "hi")
	assert.Equal(t, "streamed after the drop", turns[0].Text)
	assert.Equal(t, 1, a.resubscribes)
	assert.Equal(t, 1.0, arts.Metrics["a2a_resubscribes"])
	assert.Contains(t, eventKinds(arts), "resubscribed")

	// Without a task to resubscribe to, a dropped stream is an error.
	a = newAgent(reply{text: "streamed reply here", drop: true})
	tgt, _ = newTarget(t, a, Config{Stream: true, Connect: target.ConnectPolicy{Attempts: 1}})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a final event")
}

func TestAuthenticatedExtendedCard(t *testing.T) {
	a := newAgent(reply{text: "ok"}, reply{text: "ok"})
	a.extended = true
	tgt, _ := newTarget(t, a, Config{Skill: "vip-returns"})
	_, arts := converse(t, tgt, "hi")
	assert.Equal(t, 3.0, arts.Metrics["a2a_skills"], "the extended card adds a skill")
	kinds := eventKinds(arts)
	assert.Equal(t, []string{"card_extended", "card", "transport", "task_completed"}, kinds)
	assert.Contains(t, arts.Events[1].Detail, "vip-returns")
	converse(t, tgt, "hi")
	assert.Equal(t, 1, a.extendedCalls, "read once per target")

	// Without credentials the public card is all there is, and a secured
	// card is noted.
	a = newAgent(reply{text: "ok"})
	a.extended, a.secured = true, true
	tgt, _ = newTarget(t, a, Config{Headers: map[string]string{}})
	_, arts = converse(t, tgt, "hi")
	assert.Equal(t, 2.0, arts.Metrics["a2a_skills"])
	assert.Contains(t, eventKinds(arts), "auth_missing")
	assert.Equal(t, 0, a.extendedCalls)
}

func TestUserDataAndFiles(t *testing.T) {
	a := newAgent(reply{text: "got them"})
	tgt, _ := newTarget(t, a, Config{})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "here you go", Data: map[string]any{"order": "ORD-1"},
		Files: []target.Attachment{{Name: "invoice.pdf", MIME: "application/pdf", Bytes: []byte("%PDF")}, {Name: "photo.jpg", MIME: "image/jpeg", URI: "https://cdn.example.com/photo.jpg"}}}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "got them", turn.Text)
	require.Len(t, a.received, 1)
	parts := a.received[0].Parts
	require.Len(t, parts, 4)
	assert.Equal(t, "text", parts[0].Kind)
	assert.Equal(t, "data", parts[1].Kind)
	assert.Equal(t, "ORD-1", parts[1].Data["order"])
	assert.Equal(t, "file", parts[2].Kind)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("%PDF")), parts[2].File["bytes"])
	assert.Equal(t, "application/pdf", parts[2].File["mimeType"])
	assert.Equal(t, "https://cdn.example.com/photo.jpg", parts[3].File["uri"])
	assert.Nil(t, parts[3].File["bytes"])

	// A turn with no words still carries a text part, so every agent accepts it.
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Data: map[string]any{"a": 1}}))
	_, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "data", a.received[1].Parts[0].Kind)
	assert.Len(t, a.received[1].Parts, 1)
	_, _ = sess.Close(context.Background())
}
