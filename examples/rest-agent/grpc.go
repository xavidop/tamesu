package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"strings"

	"google.golang.org/grpc"

	"github.com/xavidop/tamesu/targets/grpc/turnv1"
)

// turnServer exposes the same agent behind tamesu's turn API
// (proto/tamesu/turn/v1), streaming and unary.
type turnServer struct {
	turnv1.UnimplementedTurnServiceServer
	a *agent
}

func (s *turnServer) Converse(stream grpc.BidiStreamingServer[turnv1.ClientEvent, turnv1.ServerEvent]) error {
	sessionID := ""
	for {
		in, err := stream.Recv()
		if err != nil {
			return nil
		}
		switch e := in.Event.(type) {
		case *turnv1.ClientEvent_SessionStart:
			sessionID = e.SessionStart.SessionId
		case *turnv1.ClientEvent_SessionEnd:
			return nil
		case *turnv1.ClientEvent_UserTurn:
			resp, err := s.a.flow.Run(stream.Context(), request{SessionID: sessionID, Message: e.UserTurn.Text})
			if err != nil {
				return err
			}
			// Stream the reply word by word, then the tool calls, then the end.
			for _, w := range strings.SplitAfter(resp.Reply, " ") {
				if err := stream.Send(&turnv1.ServerEvent{Event: &turnv1.ServerEvent_TextDelta{TextDelta: &turnv1.TextDelta{Text: w}}}); err != nil {
					return err
				}
			}
			for _, c := range toolCallsProto(resp.ToolCalls) {
				if err := stream.Send(&turnv1.ServerEvent{Event: &turnv1.ServerEvent_ToolCall{ToolCall: c}}); err != nil {
					return err
				}
			}
			if err := stream.Send(&turnv1.ServerEvent{Event: &turnv1.ServerEvent_TurnEnd{TurnEnd: &turnv1.TurnEnd{EndSession: resp.EndSession}}}); err != nil {
				return err
			}
		}
	}
}

func (s *turnServer) Turn(ctx context.Context, req *turnv1.TurnRequest) (*turnv1.TurnResponse, error) {
	resp, err := s.a.flow.Run(ctx, request{SessionID: req.SessionId, Message: req.Text})
	if err != nil {
		return nil, err
	}
	return &turnv1.TurnResponse{Text: resp.Reply, ToolCalls: toolCallsProto(resp.ToolCalls), EndSession: resp.EndSession}, nil
}

func toolCallsProto(calls []toolCall) []*turnv1.ToolCall {
	out := make([]*turnv1.ToolCall, 0, len(calls))
	for _, c := range calls {
		args, _ := json.Marshal(c.Args)
		if c.Args == nil {
			args = nil
		}
		out = append(out, &turnv1.ToolCall{Name: c.Name, ArgumentsJson: string(args)})
	}
	return out
}

// serveGRPC serves the turn API on addr until ctx ends.
func (a *agent) serveGRPC(ctx context.Context, addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	turnv1.RegisterTurnServiceServer(srv, &turnServer{a: a})
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	log.Printf("rest-agent serving the gRPC turn API on %s", addr)
	return srv.Serve(l)
}
