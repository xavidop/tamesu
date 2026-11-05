package a2a

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/a2aproject/a2a-go/a2apb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/xavidop/tamesu/targets/a2a/internal/pbjson"
)

// grpcTransport is the gRPC binding, through the protocol's generated
// service. The address is grpc://host:port (plaintext), grpcs://host:port
// (TLS) or host:port (TLS, plaintext on a loopback address).
type grpcTransport struct {
	s      *session
	conn   *grpc.ClientConn
	client a2apb.A2AServiceClient
	md     metadata.MD
}

// grpcAddress splits a gRPC URL into the dial address and whether to use TLS.
func grpcAddress(u string) (addr string, useTLS bool) {
	switch {
	case strings.HasPrefix(u, "grpcs://"):
		return strings.TrimPrefix(u, "grpcs://"), true
	case strings.HasPrefix(u, "grpc://"):
		return strings.TrimPrefix(u, "grpc://"), false
	case strings.HasPrefix(u, "https://"):
		return strings.TrimPrefix(u, "https://"), true
	case strings.HasPrefix(u, "http://"):
		return strings.TrimPrefix(u, "http://"), false
	}
	host, _, err := net.SplitHostPort(u)
	if err != nil {
		host = u
	}
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
	return u, !loopback
}

// isGRPCURL says whether a URL names a gRPC endpoint by its scheme.
func isGRPCURL(u string) bool {
	return strings.HasPrefix(u, "grpc://") || strings.HasPrefix(u, "grpcs://")
}

func newGRPCTransport(s *session, u string) (*grpcTransport, error) {
	addr, useTLS := grpcAddress(u)
	addr = strings.TrimRight(addr, "/")
	creds := insecure.NewCredentials()
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc %s: %w", addr, err)
	}
	md := metadata.MD{}
	for k, v := range s.hdr {
		lk := strings.ToLower(k)
		if lk == "content-type" || lk == "accept" || lk == "content-length" {
			continue
		}
		md[lk] = append([]string(nil), v...)
	}
	return &grpcTransport{s: s, conn: conn, client: a2apb.NewA2AServiceClient(conn), md: md}, nil
}

// ctx counts the request and attaches the headers as metadata.
func (g *grpcTransport) ctx(ctx context.Context) context.Context {
	g.s.calls++
	return g.outgoing(ctx)
}

func (g *grpcTransport) outgoing(ctx context.Context) context.Context {
	if len(g.md) == 0 {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, g.md)
}

// grpcErr turns a status error into a readable one.
func grpcErr(what string, err error) error {
	if st, ok := status.FromError(err); ok {
		return fmt.Errorf("%s: %s: %s", what, st.Code(), st.Message())
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (g *grpcTransport) send(ctx context.Context, p sendParams) (json.RawMessage, error) {
	params, _ := json.Marshal(p)
	req, err := pbjson.SendRequest(params)
	if err != nil {
		return nil, fmt.Errorf("SendMessage: %w", err)
	}
	resp, err := g.client.SendMessage(g.ctx(ctx), req)
	if err != nil {
		return nil, grpcErr("SendMessage", err)
	}
	return pbjson.SendResponseJSON(resp)
}

func (g *grpcTransport) stream(ctx context.Context, p sendParams) (events, error) {
	params, _ := json.Marshal(p)
	req, err := pbjson.SendRequest(params)
	if err != nil {
		return nil, fmt.Errorf("SendStreamingMessage: %w", err)
	}
	st, err := g.client.SendStreamingMessage(g.ctx(ctx), req)
	if err != nil {
		return nil, grpcErr("SendStreamingMessage", err)
	}
	return &grpcEvents{what: "SendStreamingMessage", st: st}, nil
}

func (g *grpcTransport) get(ctx context.Context, id string) (json.RawMessage, error) {
	req, err := pbjson.GetTaskRequest(id, nil)
	if err != nil {
		return nil, err
	}
	t, err := g.client.GetTask(g.ctx(ctx), req)
	if err != nil {
		return nil, grpcErr("GetTask", err)
	}
	return pbjson.TaskJSON(t)
}

func (g *grpcTransport) cancel(ctx context.Context, id string) (json.RawMessage, error) {
	req, err := pbjson.CancelTaskRequest(id)
	if err != nil {
		return nil, err
	}
	t, err := g.client.CancelTask(g.ctx(ctx), req)
	if err != nil {
		return nil, grpcErr("CancelTask", err)
	}
	return pbjson.TaskJSON(t)
}

func (g *grpcTransport) resubscribe(ctx context.Context, id string) (events, error) {
	req, err := pbjson.SubscribeRequest(id)
	if err != nil {
		return nil, err
	}
	st, err := g.client.TaskSubscription(g.ctx(ctx), req)
	if err != nil {
		return nil, grpcErr("TaskSubscription", err)
	}
	return &grpcEvents{what: "TaskSubscription", st: st}, nil
}

func (g *grpcTransport) setPush(ctx context.Context, id string, cfg pushConfig) error {
	b, _ := json.Marshal(map[string]any{"taskId": id, "pushNotificationConfig": cfg})
	req, err := pbjson.CreatePushRequest(b)
	if err != nil {
		return err
	}
	if _, err := g.client.CreateTaskPushNotificationConfig(g.ctx(ctx), req); err != nil {
		return grpcErr("CreateTaskPushNotificationConfig", err)
	}
	return nil
}

// card is GetAgentCard, the way a gRPC agent publishes its card.
func (g *grpcTransport) card(ctx context.Context) (*Card, error) {
	c, err := g.client.GetAgentCard(g.outgoing(ctx), &a2apb.GetAgentCardRequest{})
	if err != nil {
		return nil, grpcErr("GetAgentCard", err)
	}
	raw, err := pbjson.AgentCardJSON(c)
	if err != nil {
		return nil, err
	}
	return parseCard(raw)
}

func (g *grpcTransport) extendedCard(ctx context.Context) (*Card, error) { return g.card(ctx) }

func (g *grpcTransport) close() { _ = g.conn.Close() }

// grpcEvents adapts a server stream to events.
type grpcEvents struct {
	what string
	st   grpc.ServerStreamingClient[a2apb.StreamResponse]
}

func (e *grpcEvents) next() (json.RawMessage, error) {
	resp, err := e.st.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, grpcErr(e.what, err)
	}
	return pbjson.StreamEventJSON(resp)
}

func (e *grpcEvents) close() {}
