// Package a2abind gives tamesu's remote agents the protocol's other two
// bindings: HTTP+JSON (REST paths under /v1 on the agent's HTTP port) and
// gRPC (the generated A2AService on its own port). Both convert at the edge
// and answer through the server's Core, so every binding gives the same
// replies and records the same delegations.
package a2abind

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/a2apb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/xavidop/tamesu/internal/a2aserver"
	"github.com/xavidop/tamesu/targets/a2a/internal/pbjson"
)

// Binding serves HTTP+JSON and gRPC for one remote agent.
type Binding struct {
	// GRPCAddr is the gRPC listen address; empty means a free port on
	// 127.0.0.1. No gRPC is served when it is "off".
	GRPCAddr string
	core     a2aserver.Core
	grpc     *grpc.Server
	mu       sync.Mutex
}

// New returns a binding with both transports.
func New() a2aserver.Binding { return &Binding{} }

// Serve registers the REST routes on the agent's mux and starts the gRPC
// server, returning both as card interfaces.
func (b *Binding) Serve(core a2aserver.Core) ([]a2aserver.Interface, error) {
	b.core = core
	mux := core.Mux()
	mux.HandleFunc("POST /v1/message:send", b.auth(b.send))
	mux.HandleFunc("POST /v1/message:stream", b.auth(b.stream))
	mux.HandleFunc("GET /v1/card", b.auth(b.card))
	mux.HandleFunc("GET /v1/tasks", b.auth(b.listTasks))
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		mux.HandleFunc(m+" /v1/tasks/{rest...}", b.auth(b.tasks))
	}
	out := []a2aserver.Interface{{Transport: a2aserver.TransportHTTPJSON, URL: core.URL()}}
	if b.GRPCAddr != "off" {
		addr := b.GRPCAddr
		if addr == "" {
			addr = "127.0.0.1:0"
		}
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("grpc listen %s: %w", addr, err)
		}
		host := l.Addr().String()
		if h, port, err := net.SplitHostPort(host); err == nil && (h == "" || h == "::" || h == "0.0.0.0") {
			host = net.JoinHostPort("127.0.0.1", port)
		}
		b.mu.Lock()
		b.grpc = grpc.NewServer(grpc.UnaryInterceptor(b.unaryAuth), grpc.StreamInterceptor(b.streamAuth))
		a2apb.RegisterA2AServiceServer(b.grpc, &service{b: b})
		srv := b.grpc
		b.mu.Unlock()
		go func() { _ = srv.Serve(l) }()
		out = append(out, a2aserver.Interface{Transport: a2aserver.TransportGRPC, URL: "grpc://" + host})
	}
	return out, nil
}

// Close stops the gRPC server; the REST routes go with the agent's server.
func (b *Binding) Close() error {
	b.mu.Lock()
	srv := b.grpc
	b.grpc = nil
	b.mu.Unlock()
	if srv != nil {
		srv.Stop()
	}
	return nil
}

// HTTP+JSON

func (b *Binding) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.core.Authorized(r.Header.Get("Authorization")) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeStatus(w, http.StatusUnauthorized, codes.Unauthenticated, "a bearer token is required")
			return
		}
		h(w, r)
	}
}

// writeStatus writes a google.rpc.Status body, the REST binding's error form.
func writeStatus(w http.ResponseWriter, httpCode int, code codes.Code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": int(code), "message": msg, "status": code.String()})
}

// httpError maps a protocol error onto an HTTP status and a gRPC code.
func httpError(w http.ResponseWriter, e *a2aserver.Error) {
	code, httpCode := grpcCode(e.Code)
	writeStatus(w, httpCode, code, e.Message)
}

func grpcCode(code int) (codes.Code, int) {
	switch code {
	case a2aserver.CodeTaskNotFound:
		return codes.NotFound, http.StatusNotFound
	case a2aserver.CodeNotCancelable:
		return codes.FailedPrecondition, http.StatusConflict
	case a2aserver.CodeInvalidParams, a2aserver.CodeInvalidRequest, a2aserver.CodeParse:
		return codes.InvalidArgument, http.StatusBadRequest
	case a2aserver.CodeMethodNotFound, a2aserver.CodePushUnsupported:
		return codes.Unimplemented, http.StatusNotImplemented
	case a2aserver.CodeAuthRequired:
		return codes.Unauthenticated, http.StatusUnauthorized
	}
	return codes.Internal, http.StatusInternalServerError
}

func readProto(w http.ResponseWriter, r *http.Request, m proto.Message) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, codes.InvalidArgument, err.Error())
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := pbjson.Unmarshal(body, m); err != nil {
		writeStatus(w, http.StatusBadRequest, codes.InvalidArgument, "not the expected JSON: "+err.Error())
		return false
	}
	return true
}

func writeProto(w http.ResponseWriter, m proto.Message) {
	b, err := pbjson.Marshal(m)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, codes.Internal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (b *Binding) send(w http.ResponseWriter, r *http.Request) {
	var req a2apb.SendMessageRequest
	if !readProto(w, r, &req) {
		return
	}
	resp, e := b.sendMessage(r.Context(), &req, a2aserver.TransportHTTPJSON)
	if e != nil {
		httpError(w, e)
		return
	}
	writeProto(w, resp)
}

func (b *Binding) stream(w http.ResponseWriter, r *http.Request) {
	var req a2apb.SendMessageRequest
	if !readProto(w, r, &req) {
		return
	}
	params, err := pbjson.SendParamsJSON(&req)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, codes.InvalidArgument, err.Error())
		return
	}
	b.sse(w, r, "message/stream", params)
}

// sse relays a streaming method as server-sent events of StreamResponse.
func (b *Binding) sse(w http.ResponseWriter, r *http.Request, method string, params json.RawMessage) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	emit := func(ev json.RawMessage) error {
		sr, err := pbjson.StreamEvent(ev)
		if err != nil {
			return err
		}
		out, err := pbjson.Marshal(sr)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", out); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	}
	if e := b.core.Stream(r.Context(), method, params, a2aserver.TransportHTTPJSON, emit); e != nil {
		code, _ := grpcCode(e.Code)
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": int(code), "message": e.Message, "status": code.String()}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
	}
}

func (b *Binding) card(w http.ResponseWriter, _ *http.Request) {
	card, err := pbjson.AgentCard(marshal(b.core.Card(true)))
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, codes.Internal, err.Error())
		return
	}
	writeProto(w, card)
}

func (b *Binding) listTasks(w http.ResponseWriter, r *http.Request) {
	resp, e := b.list(r.Context())
	if e != nil {
		httpError(w, e)
		return
	}
	writeProto(w, resp)
}

// tasks routes /v1/tasks/{id}[:cancel|:subscribe|/pushNotificationConfigs[/{configId}]].
func (b *Binding) tasks(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	id, op := rest, ""
	if i := strings.IndexAny(rest, ":/"); i >= 0 {
		id, op = rest[:i], rest[i:]
	}
	if id == "" {
		writeStatus(w, http.StatusNotFound, codes.NotFound, "no task id")
		return
	}
	ctx := r.Context()
	switch {
	case op == "" && r.Method == http.MethodGet:
		var hl *int
		if v := r.URL.Query().Get("historyLength"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				hl = &n
			}
		}
		t, e := b.getTask(ctx, id, hl)
		if e != nil {
			httpError(w, e)
			return
		}
		writeProto(w, t)
	case op == ":cancel" && r.Method == http.MethodPost:
		t, e := b.cancel(ctx, id)
		if e != nil {
			httpError(w, e)
			return
		}
		writeProto(w, t)
	case op == ":subscribe" && r.Method == http.MethodPost:
		b.sse(w, r, "tasks/resubscribe", marshal(map[string]any{"id": id}))
	case op == "/pushNotificationConfigs" && r.Method == http.MethodPost:
		var cfg a2apb.TaskPushNotificationConfig
		if !readProto(w, r, &cfg) {
			return
		}
		req := &a2apb.CreateTaskPushNotificationConfigRequest{Parent: pbjson.TaskName(id), ConfigId: r.URL.Query().Get("configId"), Config: &cfg}
		out, e := b.createPush(ctx, req)
		if e != nil {
			httpError(w, e)
			return
		}
		writeProto(w, out)
	case op == "/pushNotificationConfigs" && r.Method == http.MethodGet:
		out, e := b.listPush(ctx, id)
		if e != nil {
			httpError(w, e)
			return
		}
		writeProto(w, out)
	case strings.HasPrefix(op, "/pushNotificationConfigs/") && r.Method == http.MethodGet:
		out, e := b.getPush(ctx, id, strings.TrimPrefix(op, "/pushNotificationConfigs/"))
		if e != nil {
			httpError(w, e)
			return
		}
		writeProto(w, out)
	case strings.HasPrefix(op, "/pushNotificationConfigs/") && r.Method == http.MethodDelete:
		if e := b.deletePush(ctx, id, strings.TrimPrefix(op, "/pushNotificationConfigs/")); e != nil {
			httpError(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeStatus(w, http.StatusNotFound, codes.NotFound, "no such route")
	}
}

// Shared method implementations: proto in, Core, proto out.

func marshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func internalErr(err error) *a2aserver.Error {
	return &a2aserver.Error{Code: a2aserver.CodeInternal, Message: err.Error()}
}

func (b *Binding) sendMessage(ctx context.Context, req *a2apb.SendMessageRequest, transport string) (*a2apb.SendMessageResponse, *a2aserver.Error) {
	params, err := pbjson.SendParamsJSON(req)
	if err != nil {
		return nil, &a2aserver.Error{Code: a2aserver.CodeInvalidParams, Message: err.Error()}
	}
	result, e := b.core.Handle(ctx, "message/send", params, transport)
	if e != nil {
		return nil, e
	}
	resp, err := pbjson.SendResponse(result)
	if err != nil {
		return nil, internalErr(err)
	}
	return resp, nil
}

func (b *Binding) getTask(ctx context.Context, id string, historyLength *int) (*a2apb.Task, *a2aserver.Error) {
	result, e := b.core.Handle(ctx, "tasks/get", marshal(map[string]any{"id": id, "historyLength": historyLength}), a2aserver.TransportHTTPJSON)
	if e != nil {
		return nil, e
	}
	t, err := pbjson.Task(result)
	if err != nil {
		return nil, internalErr(err)
	}
	return t, nil
}

func (b *Binding) cancel(ctx context.Context, id string) (*a2apb.Task, *a2aserver.Error) {
	result, e := b.core.Handle(ctx, "tasks/cancel", marshal(map[string]any{"id": id}), a2aserver.TransportHTTPJSON)
	if e != nil {
		return nil, e
	}
	t, err := pbjson.Task(result)
	if err != nil {
		return nil, internalErr(err)
	}
	return t, nil
}

func (b *Binding) list(ctx context.Context) (*a2apb.ListTasksResponse, *a2aserver.Error) {
	result, e := b.core.Handle(ctx, "tasks/list", nil, a2aserver.TransportHTTPJSON)
	if e != nil {
		return nil, e
	}
	var list struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(result, &list); err != nil {
		return nil, internalErr(err)
	}
	out := &a2apb.ListTasksResponse{}
	for _, raw := range list.Tasks {
		t, err := pbjson.Task(raw)
		if err != nil {
			return nil, internalErr(err)
		}
		out.Tasks = append(out.Tasks, t)
	}
	return out, nil
}

func (b *Binding) createPush(ctx context.Context, req *a2apb.CreateTaskPushNotificationConfigRequest) (*a2apb.TaskPushNotificationConfig, *a2aserver.Error) {
	params, err := pbjson.CreatePushJSON(req)
	if err != nil {
		return nil, &a2aserver.Error{Code: a2aserver.CodeInvalidParams, Message: err.Error()}
	}
	result, e := b.core.Handle(ctx, "tasks/pushNotificationConfig/set", params, a2aserver.TransportHTTPJSON)
	if e != nil {
		return nil, e
	}
	out, err := pbjson.TaskPushConfig(result)
	if err != nil {
		return nil, internalErr(err)
	}
	return out, nil
}

func (b *Binding) getPush(ctx context.Context, id, configID string) (*a2apb.TaskPushNotificationConfig, *a2aserver.Error) {
	result, e := b.core.Handle(ctx, "tasks/pushNotificationConfig/get", marshal(map[string]any{"id": id, "pushNotificationConfigId": configID}), a2aserver.TransportHTTPJSON)
	if e != nil {
		return nil, e
	}
	out, err := pbjson.TaskPushConfig(result)
	if err != nil {
		return nil, internalErr(err)
	}
	return out, nil
}

func (b *Binding) listPush(ctx context.Context, id string) (*a2apb.ListTaskPushNotificationConfigResponse, *a2aserver.Error) {
	result, e := b.core.Handle(ctx, "tasks/pushNotificationConfig/list", marshal(map[string]any{"id": id}), a2aserver.TransportHTTPJSON)
	if e != nil {
		return nil, e
	}
	var list []json.RawMessage
	if err := json.Unmarshal(result, &list); err != nil {
		return nil, internalErr(err)
	}
	out := &a2apb.ListTaskPushNotificationConfigResponse{}
	for _, raw := range list {
		c, err := pbjson.TaskPushConfig(raw)
		if err != nil {
			return nil, internalErr(err)
		}
		out.Configs = append(out.Configs, c)
	}
	return out, nil
}

func (b *Binding) deletePush(ctx context.Context, id, configID string) *a2aserver.Error {
	_, e := b.core.Handle(ctx, "tasks/pushNotificationConfig/delete", marshal(map[string]any{"id": id, "pushNotificationConfigId": configID}), a2aserver.TransportHTTPJSON)
	return e
}

// gRPC

func authorization(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) > 0 {
		return v[0]
	}
	return ""
}

func (b *Binding) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if !b.core.Authorized(authorization(ctx)) {
		return nil, status.Error(codes.Unauthenticated, "a bearer token is required")
	}
	return handler(ctx, req)
}

func (b *Binding) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if !b.core.Authorized(authorization(ss.Context())) {
		return status.Error(codes.Unauthenticated, "a bearer token is required")
	}
	return handler(srv, ss)
}

func statusErr(e *a2aserver.Error) error {
	code, _ := grpcCode(e.Code)
	return status.Error(code, e.Message)
}

// service is the generated A2AService, answered through Core.
type service struct {
	a2apb.UnimplementedA2AServiceServer
	b *Binding
}

func (s *service) SendMessage(ctx context.Context, req *a2apb.SendMessageRequest) (*a2apb.SendMessageResponse, error) {
	resp, e := s.b.sendMessage(ctx, req, a2aserver.TransportGRPC)
	if e != nil {
		return nil, statusErr(e)
	}
	return resp, nil
}

func (s *service) SendStreamingMessage(req *a2apb.SendMessageRequest, st grpc.ServerStreamingServer[a2apb.StreamResponse]) error {
	params, err := pbjson.SendParamsJSON(req)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return s.relay(st, "message/stream", params)
}

func (s *service) relay(st grpc.ServerStreamingServer[a2apb.StreamResponse], method string, params json.RawMessage) error {
	emit := func(ev json.RawMessage) error {
		sr, err := pbjson.StreamEvent(ev)
		if err != nil {
			return err
		}
		return st.Send(sr)
	}
	if e := s.b.core.Stream(st.Context(), method, params, a2aserver.TransportGRPC, emit); e != nil {
		return statusErr(e)
	}
	return nil
}

func (s *service) GetTask(ctx context.Context, req *a2apb.GetTaskRequest) (*a2apb.Task, error) {
	id, err := pbjson.TaskID(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var hl *int
	if req.GetHistoryLength() != 0 {
		n := int(req.GetHistoryLength())
		hl = &n
	}
	t, e := s.b.getTask(ctx, id, hl)
	if e != nil {
		return nil, statusErr(e)
	}
	return t, nil
}

func (s *service) ListTasks(ctx context.Context, _ *a2apb.ListTasksRequest) (*a2apb.ListTasksResponse, error) {
	out, e := s.b.list(ctx)
	if e != nil {
		return nil, statusErr(e)
	}
	return out, nil
}

func (s *service) CancelTask(ctx context.Context, req *a2apb.CancelTaskRequest) (*a2apb.Task, error) {
	id, err := pbjson.TaskID(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	t, e := s.b.cancel(ctx, id)
	if e != nil {
		return nil, statusErr(e)
	}
	return t, nil
}

func (s *service) TaskSubscription(req *a2apb.TaskSubscriptionRequest, st grpc.ServerStreamingServer[a2apb.StreamResponse]) error {
	id, err := pbjson.TaskID(req.GetName())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return s.relay(st, "tasks/resubscribe", marshal(map[string]any{"id": id}))
}

func (s *service) CreateTaskPushNotificationConfig(ctx context.Context, req *a2apb.CreateTaskPushNotificationConfigRequest) (*a2apb.TaskPushNotificationConfig, error) {
	out, e := s.b.createPush(ctx, req)
	if e != nil {
		return nil, statusErr(e)
	}
	return out, nil
}

func (s *service) GetTaskPushNotificationConfig(ctx context.Context, req *a2apb.GetTaskPushNotificationConfigRequest) (*a2apb.TaskPushNotificationConfig, error) {
	id, err := pbjson.TaskID(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	configID, _ := pbjson.ConfigID(req.GetName())
	out, e := s.b.getPush(ctx, id, configID)
	if e != nil {
		return nil, statusErr(e)
	}
	return out, nil
}

func (s *service) ListTaskPushNotificationConfig(ctx context.Context, req *a2apb.ListTaskPushNotificationConfigRequest) (*a2apb.ListTaskPushNotificationConfigResponse, error) {
	id, err := pbjson.TaskID(req.GetParent())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out, e := s.b.listPush(ctx, id)
	if e != nil {
		return nil, statusErr(e)
	}
	return out, nil
}

func (s *service) DeleteTaskPushNotificationConfig(ctx context.Context, req *a2apb.DeleteTaskPushNotificationConfigRequest) (*emptypb.Empty, error) {
	id, err := pbjson.TaskID(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	configID, _ := pbjson.ConfigID(req.GetName())
	if e := s.b.deletePush(ctx, id, configID); e != nil {
		return nil, statusErr(e)
	}
	return &emptypb.Empty{}, nil
}

func (s *service) GetAgentCard(ctx context.Context, _ *a2apb.GetAgentCardRequest) (*a2apb.AgentCard, error) {
	card, err := pbjson.AgentCard(marshal(s.b.core.Card(s.b.core.Authorized(authorization(ctx)))))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return card, nil
}
