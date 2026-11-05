package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/target"
)

// Transports are the protocol bindings the target speaks.
const (
	TransportJSONRPC = "jsonrpc"
	TransportHTTP    = "http"
	TransportGRPC    = "grpc"
)

// Transports lists them.
var Transports = []string{TransportJSONRPC, TransportHTTP, TransportGRPC}

// cardTransports map the card's transport names onto ours.
var cardTransports = map[string]string{"JSONRPC": TransportJSONRPC, "HTTP+JSON": TransportHTTP, "GRPC": TransportGRPC}

// transport is one binding of the protocol. Every method takes and returns
// the protocol's JSON, whatever the wire carries, so the session logic is
// written once.
type transport interface {
	// send is message/send; the result is a task or a message.
	send(ctx context.Context, p sendParams) (json.RawMessage, error)
	// stream is message/stream; events end with io.EOF.
	stream(ctx context.Context, p sendParams) (events, error)
	get(ctx context.Context, id string) (json.RawMessage, error)
	cancel(ctx context.Context, id string) (json.RawMessage, error)
	// resubscribe is tasks/resubscribe: the events of a task whose stream
	// was lost.
	resubscribe(ctx context.Context, id string) (events, error)
	// setPush subscribes a URL to a task's updates.
	setPush(ctx context.Context, id string, cfg pushConfig) error
	// extendedCard is agent/getAuthenticatedExtendedCard.
	extendedCard(ctx context.Context) (*Card, error)
	close()
}

// events is a stream of protocol events; next returns io.EOF at the end.
type events interface {
	next() (json.RawMessage, error)
	close()
}

// sendParams are the parameters of message/send and message/stream.
type sendParams struct {
	Message       message     `json:"message"`
	Configuration *sendConfig `json:"configuration,omitempty"`
}

type sendConfig struct {
	Blocking            *bool       `json:"blocking,omitempty"`
	AcceptedOutputModes []string    `json:"acceptedOutputModes,omitempty"`
	HistoryLength       *int        `json:"historyLength,omitempty"`
	Push                *pushConfig `json:"pushNotificationConfig,omitempty"`
}

// pushConfig tells the agent where to post a task's updates.
type pushConfig struct {
	ID    string `json:"id,omitempty"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// acceptedOutputModes is what every send asks for.
var acceptedOutputModes = []string{"text/plain", "text/markdown", "application/json"}

// jsonrpcTransport is JSON-RPC over HTTP, the protocol's default binding.
type jsonrpcTransport struct {
	s   *session
	url string
}

// call performs one JSON-RPC request and returns the raw response.
func (j *jsonrpcTransport) call(ctx context.Context, method string, params any, accept string) (*http.Response, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: uuid.NewString(), Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	j.s.calls++
	return target.Connect(ctx, j.s.t.cfg.Connect, j.s.run.Logger, "a2a "+j.s.t.name, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.url, bytes.NewReader(body))
		if err != nil {
			return nil, target.NoRetry(err)
		}
		req.Header = j.s.hdr.Clone()
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := j.s.t.cfg.Client.Do(req)
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

// callJSON performs a JSON-RPC request with a JSON result.
func (j *jsonrpcTransport) callJSON(ctx context.Context, method string, params any) (json.RawMessage, error) {
	resp, err := j.call(ctx, method, params, "application/json")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, excerpt(raw))
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: not a JSON-RPC response: %s", method, excerpt(raw))
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s: %w", method, out.Error)
	}
	return out.Result, nil
}

// callStream performs a JSON-RPC request answered with server-sent events.
func (j *jsonrpcTransport) callStream(ctx context.Context, method string, params any) (events, error) {
	resp, err := j.call(ctx, method, params, "text/event-stream")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, excerpt(raw))
	}
	return newSSE(resp.Body, func(payload []byte) (json.RawMessage, error) {
		var out rpcResponse
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, fmt.Errorf("%s: not a JSON-RPC event: %s", method, excerpt(payload))
		}
		if out.Error != nil {
			return nil, fmt.Errorf("%s: %w", method, out.Error)
		}
		return out.Result, nil
	}), nil
}

func (j *jsonrpcTransport) send(ctx context.Context, p sendParams) (json.RawMessage, error) {
	return j.callJSON(ctx, "message/send", p)
}

func (j *jsonrpcTransport) stream(ctx context.Context, p sendParams) (events, error) {
	return j.callStream(ctx, "message/stream", p)
}

func (j *jsonrpcTransport) get(ctx context.Context, id string) (json.RawMessage, error) {
	return j.callJSON(ctx, "tasks/get", map[string]any{"id": id})
}

func (j *jsonrpcTransport) cancel(ctx context.Context, id string) (json.RawMessage, error) {
	return j.callJSON(ctx, "tasks/cancel", map[string]any{"id": id})
}

func (j *jsonrpcTransport) resubscribe(ctx context.Context, id string) (events, error) {
	return j.callStream(ctx, "tasks/resubscribe", map[string]any{"id": id})
}

func (j *jsonrpcTransport) setPush(ctx context.Context, id string, cfg pushConfig) error {
	_, err := j.callJSON(ctx, "tasks/pushNotificationConfig/set", map[string]any{"taskId": id, "pushNotificationConfig": cfg})
	return err
}

func (j *jsonrpcTransport) extendedCard(ctx context.Context) (*Card, error) {
	raw, err := j.callJSON(ctx, "agent/getAuthenticatedExtendedCard", nil)
	if err != nil {
		return nil, err
	}
	return parseCard(raw)
}

func (j *jsonrpcTransport) close() {}

// parseCard reads a card out of JSON.
func parseCard(raw []byte) (*Card, error) {
	var card Card
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("not an agent card: %s", excerpt(raw))
	}
	if card.Name == "" && len(card.Skills) == 0 {
		return nil, errors.New("not an agent card: no name and no skills")
	}
	return &card, nil
}

// sse reads server-sent events (data: lines up to a blank line) or, for a
// server that answers newline-delimited JSON instead, one object per line.
// decode turns a payload into the protocol event it carries.
type sse struct {
	body   io.ReadCloser
	sc     *bufio.Scanner
	decode func(payload []byte) (json.RawMessage, error)
}

func newSSE(body io.ReadCloser, decode func([]byte) (json.RawMessage, error)) *sse {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 32<<20)
	return &sse{body: body, sc: sc, decode: decode}
}

func (e *sse) next() (json.RawMessage, error) {
	var data strings.Builder
	for e.sc.Scan() {
		line := e.sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				return e.decode([]byte(strings.TrimSpace(data.String())))
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "{") && data.Len() == 0:
			return e.decode([]byte(line))
		}
	}
	if data.Len() > 0 {
		return e.decode([]byte(strings.TrimSpace(data.String())))
	}
	if err := e.sc.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (e *sse) close() { _ = e.body.Close() }
