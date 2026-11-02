package grpc

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/grpc/turnv1"
	"github.com/xavidop/tamesu/targettest"
)

// fakeAgent answers scripted replies word by word; a message about returns
// calls lookup_policy; the last reply ends the session. unaryOnly leaves
// Converse unimplemented.
type fakeAgent struct {
	turnv1.UnimplementedTurnServiceServer
	mu        sync.Mutex
	replies   []string
	i         int
	unaryOnly bool
	slow      time.Duration
	sessions  []string
	headers   []metadata.MD
	ended     int
}

func (f *fakeAgent) next() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := "no more replies"
	if f.i < len(f.replies) {
		reply = f.replies[f.i]
	}
	f.i++
	return reply, f.i >= len(f.replies)
}

func (f *fakeAgent) Converse(stream grpc.BidiStreamingServer[turnv1.ClientEvent, turnv1.ServerEvent]) error {
	if f.unaryOnly {
		return status.Error(codes.Unimplemented, "no Converse here")
	}
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		f.mu.Lock()
		f.headers = append(f.headers, md)
		f.mu.Unlock()
	}
	for {
		in, err := stream.Recv()
		if err != nil {
			return nil
		}
		switch e := in.Event.(type) {
		case *turnv1.ClientEvent_SessionStart:
			f.mu.Lock()
			f.sessions = append(f.sessions, e.SessionStart.SessionId)
			f.mu.Unlock()
		case *turnv1.ClientEvent_SessionEnd:
			f.mu.Lock()
			f.ended++
			f.mu.Unlock()
			return nil
		case *turnv1.ClientEvent_UserTurn:
			reply, last := f.next()
			time.Sleep(f.slow)
			for _, w := range strings.SplitAfter(reply, " ") {
				_ = stream.Send(&turnv1.ServerEvent{Event: &turnv1.ServerEvent_TextDelta{TextDelta: &turnv1.TextDelta{Text: w}}})
			}
			if strings.Contains(strings.ToLower(e.UserTurn.Text), "return") {
				_ = stream.Send(&turnv1.ServerEvent{Event: &turnv1.ServerEvent_ToolCall{ToolCall: &turnv1.ToolCall{Name: "lookup_policy", ArgumentsJson: `{"topic":"returns"}`}}})
			}
			_ = stream.Send(&turnv1.ServerEvent{Event: &turnv1.ServerEvent_TurnEnd{TurnEnd: &turnv1.TurnEnd{EndSession: last}}})
		}
	}
}

func (f *fakeAgent) Turn(_ context.Context, req *turnv1.TurnRequest) (*turnv1.TurnResponse, error) {
	reply, last := f.next()
	f.mu.Lock()
	f.sessions = append(f.sessions, req.SessionId)
	f.mu.Unlock()
	resp := &turnv1.TurnResponse{Text: reply, EndSession: last}
	if strings.Contains(strings.ToLower(req.Text), "return") {
		resp.ToolCalls = []*turnv1.ToolCall{{Name: "lookup_policy", ArgumentsJson: `{"topic":"returns"}`}}
	}
	return resp, nil
}

func serve(t *testing.T, f *fakeAgent) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	turnv1.RegisterTurnServiceServer(srv, f)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
	return "grpc://" + l.Addr().String()
}

func runCtx(t *testing.T) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: func(s string) (string, error) {
		s = strings.ReplaceAll(s, "{{secrets.TOKEN}}", "tok-123")
		return strings.ReplaceAll(s, "{{uuid}}", "sess-1"), nil
	}}
}

func TestConformance(t *testing.T) {
	for _, unary := range []bool{false, true} {
		name := "stream"
		if unary {
			name = "unary"
		}
		t.Run(name, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
				return New("agent", Config{URL: serve(t, &fakeAgent{replies: replies, unaryOnly: unary}), Timeout: 5 * time.Second})
			}})
		})
	}
}

func TestStreamTurn(t *testing.T) {
	f := &fakeAgent{replies: []string{"Our window is 30 days.", "Bye!"}}
	tgt := New("agent", Config{URL: serve(t, f), Headers: map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"}, Timeout: 5 * time.Second})
	ctx := context.Background()
	sess, err := tgt.Start(ctx, runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Can I return shoes?"}))
	agent, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", agent.Text)
	require.Len(t, agent.ToolCalls, 1)
	assert.Equal(t, target.ToolCall{Name: "lookup_policy", Args: map[string]any{"topic": "returns"}}, agent.ToolCalls[0])
	assert.False(t, agent.Done)
	assert.False(t, agent.FirstTokenAt.IsZero())
	assert.True(t, !agent.FirstTokenAt.Before(agent.StartedAt) && !agent.EndedAt.Before(agent.FirstTokenAt))
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "thanks"}))
	agent, err = sess.Receive(ctx)
	require.NoError(t, err)
	assert.True(t, agent.Done, "the last reply ends the session")
	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2.0, arts.Metrics["turns"])
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []string{"sess-1"}, f.sessions)
	assert.Equal(t, 1, f.ended, "Close sends SessionEnd")
	require.Len(t, f.headers, 1)
	assert.Equal(t, []string{"Bearer tok-123"}, f.headers[0].Get("authorization"), "headers travel as metadata")
}

func TestUnaryFallback(t *testing.T) {
	f := &fakeAgent{replies: []string{"hello", "bye"}, unaryOnly: true}
	sess, err := New("agent", Config{URL: serve(t, f), Timeout: 5 * time.Second}).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "returns?"}))
	agent, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "hello", agent.Text)
	assert.Equal(t, "lookup_policy", agent.ToolCalls[0].Name)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "unary")
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("bad url", func(t *testing.T) {
		_, err := New("agent", Config{URL: "http://localhost:1"}).Start(ctx, runCtx(t))
		assert.ErrorContains(t, err, "scheme must be grpc or grpcs")
	})
	t.Run("nothing listening", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := l.Addr().String()
		require.NoError(t, l.Close())
		sess, err := New("agent", Config{URL: "grpc://" + addr, Timeout: 2 * time.Second}).Start(ctx, runCtx(t))
		if err == nil {
			err = sess.Send(ctx, target.UserTurn{Text: "hi"})
		}
		assert.Error(t, err)
	})
	t.Run("timeout", func(t *testing.T) {
		f := &fakeAgent{replies: []string{"slow"}, slow: 2 * time.Second}
		sess, err := New("agent", Config{URL: serve(t, f), Timeout: 300 * time.Millisecond}).Start(ctx, runCtx(t))
		require.NoError(t, err)
		err = sess.Send(ctx, target.UserTurn{Text: "hi"})
		assert.ErrorContains(t, err, "no TurnEnd within")
	})
}
