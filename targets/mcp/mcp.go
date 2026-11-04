// Package mcp is the tamesu target for an MCP server: the server's tools are
// offered to a model driven by the simulated user, and every tool call is
// executed against the server for real. It tests what an agent built on the
// server would experience: the tool descriptions, the argument schemas, the
// errors, the answers. A conversation is the model target's (see
// targets/model) with the tools fetched from the server at the start of each
// session.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/xavidop/tamesu/target"
	modeltarget "github.com/xavidop/tamesu/targets/model"
)

// DefaultSystem is the prompt the model gets when the suite gives none.
const DefaultSystem = "You are an assistant that helps the user by calling the tools available to you. Use a tool whenever it can answer the question or perform the request; report what the tool returned, truthfully, and say so when a tool fails or returns nothing."

// DefaultCallTimeout bounds one tool call.
const DefaultCallTimeout = 60 * time.Second

// Config is an MCP target.
type Config struct {
	// URL is a streamable HTTP server; Command (with Args and Env) a stdio
	// server to spawn. One of the two.
	URL     string
	Headers map[string]string
	Command string
	Args    []string
	Env     map[string]string
	// Tools restricts the tools offered to the model (all when empty).
	Tools []string
	// CallTimeout bounds one tool call (DefaultCallTimeout when zero).
	CallTimeout time.Duration

	// Model is the model target's configuration: the prompt, the model, how
	// many tool rounds a turn may take. Tools and Execute are set by this
	// target.
	Model modeltarget.Config

	// Connect overrides how the server is reached (tests: an in-process
	// server). It returns a started client.
	Connect func(ctx context.Context) (*mcpclient.Client, error)
}

// Target is an MCP target.
type Target struct {
	name string
	cfg  Config
}

// New builds the target.
func New(name string, cfg Config) *Target {
	if cfg.Model.System == "" {
		cfg.Model.System = DefaultSystem
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultCallTimeout
	}
	return &Target{name: name, cfg: cfg}
}

// Name implements target.Target.
func (t *Target) Name() string { return t.name }

// Start connects to the server, lists its tools and opens a model session
// that executes them.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if run.Chat == nil {
		return nil, fmt.Errorf("mcp %s: %w", t.name, modeltarget.ErrNoModel)
	}
	c, err := t.connect(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", t.name, err)
	}
	tools, err := t.listTools(ctx, c)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s: %w", t.name, err)
	}
	s := &session{t: t, client: c, listed: tools}
	cfg := t.cfg.Model
	cfg.Tools = tools
	cfg.Execute = s.execute
	inner, err := modeltarget.New(t.name, cfg).Start(ctx, run)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	s.Session = inner
	return s, nil
}

// connect reaches the server and completes the MCP handshake.
func (t *Target) connect(ctx context.Context, run *target.RunContext) (*mcpclient.Client, error) {
	var c *mcpclient.Client
	var err error
	switch {
	case t.cfg.Connect != nil:
		c, err = t.cfg.Connect(ctx)
	case t.cfg.URL != "":
		u := t.cfg.URL
		if run.Render != nil {
			if u, err = run.Render(u); err != nil {
				return nil, fmt.Errorf("url: %w", err)
			}
		}
		headers := make(map[string]string, len(t.cfg.Headers))
		for k, v := range t.cfg.Headers {
			if run.Render != nil {
				if v, err = run.Render(v); err != nil {
					return nil, fmt.Errorf("header %s: %w", k, err)
				}
			}
			headers[k] = v
		}
		opts := []transport.StreamableHTTPCOption{transport.WithHTTPTimeout(t.cfg.CallTimeout)}
		if len(headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(headers))
		}
		c, err = mcpclient.NewStreamableHttpClient(u, opts...)
		if err == nil {
			err = c.Start(ctx)
		}
	case t.cfg.Command != "":
		env := os.Environ()
		keys := make([]string, 0, len(t.cfg.Env))
		for k := range t.cfg.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := t.cfg.Env[k]
			if run.Render != nil {
				if v, err = run.Render(v); err != nil {
					return nil, fmt.Errorf("env %s: %w", k, err)
				}
			}
			env = append(env, k+"="+v)
		}
		c, err = mcpclient.NewStdioMCPClient(t.cfg.Command, env, t.cfg.Args...)
	default:
		return nil, errors.New("url or command is required")
	}
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	init := mcpgo.InitializeRequest{}
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "tamesu", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return c, nil
}

// listTools fetches the server's tools as model tools, filtered by Tools.
func (t *Target) listTools(ctx context.Context, c *mcpclient.Client) ([]modeltarget.Tool, error) {
	res, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	allowed := map[string]bool{}
	for _, name := range t.cfg.Tools {
		allowed[name] = true
	}
	var out []modeltarget.Tool
	for _, tl := range res.Tools {
		if len(allowed) > 0 && !allowed[tl.Name] {
			continue
		}
		out = append(out, modeltarget.Tool{Name: tl.Name, Description: tl.Description, Parameters: schemaOf(tl)})
	}
	if len(out) == 0 {
		return nil, errors.New("the server offers no tools" + restriction(t.cfg.Tools))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func restriction(tools []string) string {
	if len(tools) == 0 {
		return ""
	}
	return " named " + strings.Join(tools, ", ")
}

// schemaOf is a tool's input schema as a plain JSON object.
func schemaOf(tl mcpgo.Tool) map[string]any {
	var raw []byte
	if len(tl.RawInputSchema) > 0 {
		raw = tl.RawInputSchema
	} else {
		raw, _ = json.Marshal(tl.InputSchema)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return map[string]any{"type": "object"}
	}
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	return m
}

// session is a model session whose tools run on the server.
type session struct {
	target.Session
	t      *Target
	client *mcpclient.Client
	listed []modeltarget.Tool
}

// execute runs one tool call on the server and returns its content: the
// structured content when the server gives one, else the text contents
// (one string, or a list), and an error when the server reports one.
func (s *session) execute(ctx context.Context, call target.ToolCall) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, s.t.cfg.CallTimeout)
	defer cancel()
	req := mcpgo.CallToolRequest{}
	req.Params.Name = call.Name
	req.Params.Arguments = call.Args
	res, err := s.client.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/call %s: %w", call.Name, err)
	}
	var texts []string
	for _, c := range res.Content {
		if tc, ok := mcpgo.AsTextContent(c); ok {
			texts = append(texts, tc.Text)
		}
	}
	if res.IsError {
		return nil, fmt.Errorf("%s: %s", call.Name, strings.Join(texts, "\n"))
	}
	if res.StructuredContent != nil {
		return res.StructuredContent, nil
	}
	switch len(texts) {
	case 0:
		return map[string]any{}, nil
	case 1:
		var doc any
		if json.Unmarshal([]byte(texts[0]), &doc) == nil {
			if _, isMap := doc.(map[string]any); isMap {
				return doc, nil
			}
			if _, isList := doc.([]any); isList {
				return doc, nil
			}
		}
		return texts[0], nil
	}
	return texts, nil
}

// Close ends the model session and disconnects from the server; the tools
// the server offered are listed as an event.
func (s *session) Close(ctx context.Context) (target.Artifacts, error) {
	arts, err := s.Session.Close(ctx)
	names := make([]string, 0, len(s.listed))
	for _, tl := range s.listed {
		names = append(names, tl.Name)
	}
	arts.Events = append(arts.Events, target.Event{At: time.Now(), Kind: "mcp_tools", Detail: strings.Join(names, ", ")})
	if arts.Metrics == nil {
		arts.Metrics = map[string]float64{}
	}
	arts.Metrics["mcp_tools"] = float64(len(s.listed))
	_ = s.client.Close()
	return arts, err
}

// ToolNames lists the tools the server offered to this session.
func (s *session) ToolNames() []string {
	names := make([]string, 0, len(s.listed))
	for _, tl := range s.listed {
		names = append(names, tl.Name)
	}
	return names
}
