package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/a2aproject/a2a-go/a2apb"
	"google.golang.org/protobuf/proto"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/a2a/internal/pbjson"
)

// httpTransport is the HTTP+JSON binding: REST paths under /v1 carrying the
// protobuf messages as JSON. Requests and results are converted to the
// protocol's JSON at the edge.
type httpTransport struct {
	s    *session
	base string
}

func newHTTPTransport(s *session, base string) *httpTransport {
	return &httpTransport{s: s, base: strings.TrimRight(base, "/")}
}

// do performs one request, retrying while the agent cannot be reached.
func (h *httpTransport) do(ctx context.Context, method, path string, body []byte, accept string) (*http.Response, error) {
	h.s.calls++
	return target.Connect(ctx, h.s.t.cfg.Connect, h.s.run.Logger, "a2a "+h.s.t.name, func(ctx context.Context) (*http.Response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, h.base+path, rd)
		if err != nil {
			return nil, target.NoRetry(err)
		}
		req.Header = h.s.hdr.Clone()
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if body == nil {
			req.Header.Del("Content-Type")
		}
		resp, err := h.s.t.cfg.Client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return resp, nil
	})
}

// unary performs a request whose answer is one protobuf message.
func (h *httpTransport) unary(ctx context.Context, method, path string, in, out proto.Message) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = pbjson.Marshal(in); err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	resp, err := h.do(ctx, method, path, body, "application/json")
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("%s %s: reading the response: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, statusMessage(raw))
	}
	if out == nil {
		return nil
	}
	if err := pbjson.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: unexpected response: %s", method, path, excerpt(raw))
	}
	return nil
}

// statusMessage reads the message of a google.rpc.Status error body, or
// returns an excerpt of the body.
func statusMessage(raw []byte) string {
	var st struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if json.Unmarshal(raw, &st) == nil {
		if st.Message != "" {
			return st.Message
		}
		if m, ok := st.Error.(map[string]any); ok {
			if msg, ok := m["message"].(string); ok && msg != "" {
				return msg
			}
		}
	}
	return excerpt(raw)
}

// streamed performs a request answered with a stream of StreamResponse.
func (h *httpTransport) streamed(ctx context.Context, path string, in proto.Message) (events, error) {
	body, err := pbjson.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	resp, err := h.do(ctx, http.MethodPost, path, body, "text/event-stream")
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, statusMessage(raw))
	}
	return newSSE(resp.Body, func(payload []byte) (json.RawMessage, error) {
		var sr a2apb.StreamResponse
		if err := pbjson.Unmarshal(payload, &sr); err != nil {
			// A JSON-RPC style error object in the stream.
			var e struct {
				Error *rpcError `json:"error"`
			}
			if json.Unmarshal(payload, &e) == nil && e.Error != nil {
				return nil, e.Error
			}
			return nil, fmt.Errorf("POST %s: not a stream response: %s", path, excerpt(payload))
		}
		return pbjson.StreamEventJSON(&sr)
	}), nil
}

func (h *httpTransport) send(ctx context.Context, p sendParams) (json.RawMessage, error) {
	params, _ := json.Marshal(p)
	req, err := pbjson.SendRequest(params)
	if err != nil {
		return nil, fmt.Errorf("message:send: %w", err)
	}
	var resp a2apb.SendMessageResponse
	if err := h.unary(ctx, http.MethodPost, "/v1/message:send", req, &resp); err != nil {
		return nil, err
	}
	return pbjson.SendResponseJSON(&resp)
}

func (h *httpTransport) stream(ctx context.Context, p sendParams) (events, error) {
	params, _ := json.Marshal(p)
	req, err := pbjson.SendRequest(params)
	if err != nil {
		return nil, fmt.Errorf("message:stream: %w", err)
	}
	return h.streamed(ctx, "/v1/message:stream", req)
}

func (h *httpTransport) get(ctx context.Context, id string) (json.RawMessage, error) {
	var t a2apb.Task
	if err := h.unary(ctx, http.MethodGet, "/v1/"+pbjson.TaskName(id), nil, &t); err != nil {
		return nil, err
	}
	return pbjson.TaskJSON(&t)
}

func (h *httpTransport) cancel(ctx context.Context, id string) (json.RawMessage, error) {
	req, err := pbjson.CancelTaskRequest(id)
	if err != nil {
		return nil, err
	}
	var t a2apb.Task
	if err := h.unary(ctx, http.MethodPost, "/v1/"+pbjson.TaskName(id)+":cancel", req, &t); err != nil {
		return nil, err
	}
	return pbjson.TaskJSON(&t)
}

func (h *httpTransport) resubscribe(ctx context.Context, id string) (events, error) {
	req, err := pbjson.SubscribeRequest(id)
	if err != nil {
		return nil, err
	}
	return h.streamed(ctx, "/v1/"+pbjson.TaskName(id)+":subscribe", req)
}

func (h *httpTransport) setPush(ctx context.Context, id string, cfg pushConfig) error {
	b, _ := json.Marshal(map[string]any{"taskId": id, "pushNotificationConfig": cfg})
	req, err := pbjson.CreatePushRequest(b)
	if err != nil {
		return err
	}
	path := "/v1/" + pbjson.TaskName(id) + "/pushNotificationConfigs"
	if req.GetConfigId() != "" {
		path += "?configId=" + url.QueryEscape(req.GetConfigId())
	}
	var out a2apb.TaskPushNotificationConfig
	return h.unary(ctx, http.MethodPost, path, req.GetConfig(), &out)
}

func (h *httpTransport) extendedCard(ctx context.Context) (*Card, error) {
	var c a2apb.AgentCard
	if err := h.unary(ctx, http.MethodGet, "/v1/card", nil, &c); err != nil {
		return nil, err
	}
	raw, err := pbjson.AgentCardJSON(&c)
	if err != nil {
		return nil, err
	}
	return parseCard(raw)
}

func (h *httpTransport) close() {}
