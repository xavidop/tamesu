package llm

import (
	"context"
	"encoding/json"
	"github.com/firebase/genkit/go/genkit"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/firebase/genkit/go/plugins/mcp"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExec records the commands the tools run and answers them; a run writes
// a summary.json into its --out directory, a generate writes its --out file.
type fakeExec struct{ calls [][]string }

func (f *fakeExec) exec(_ context.Context, args []string) (string, string, int) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "run":
		out := flagValue(args, "--out")
		_ = os.MkdirAll(out, 0o755)
		_ = os.WriteFile(filepath.Join(out, "summary.json"), []byte(`{"tests":[{"name":"t","passed":true}]}`), 0o644)
		return "PASS t\n1 passed", "", 0
	case "generate":
		_ = os.WriteFile(flagValue(args, "--out"), []byte("suite: drafted\n"), 0o644)
		return "", "", 0
	case "validate":
		if len(args) > 0 && args[len(args)-1] == "broken.yaml" {
			return "", "broken.yaml:3: error: boom", 2
		}
		return "1 suite, 2 tests, 0 errors", "", 0
	}
	return "ok", "", 0
}

// decode reads a tool's raw output (a JSON-shaped map) into its result type.
func decode[T any](t *testing.T, v any) T {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestMCPTools(t *testing.T) {
	ctx := context.Background()
	g, err := initBareGenkit(ctx)
	require.NoError(t, err)
	fe := &fakeExec{}
	tools := DefineMCPTools(g, MCPOptions{Exec: fe.exec, Schema: func(which string) (string, error) { return `{"which":"` + which + `"}`, nil }})
	byName := map[string]int{}
	for i, tool := range tools {
		byName[tool.Name()] = i
	}
	for _, name := range []string{"tamesu_validate", "tamesu_list", "tamesu_run", "tamesu_generate", "tamesu_doctor", "tamesu_init", "tamesu_schema"} {
		assert.Contains(t, byName, name)
	}

	out, err := tools[byName["tamesu_validate"]].RunRaw(ctx, map[string]any{"paths": []any{"a.yaml"}, "config": "p/tamesu.yaml", "strict": true})
	require.NoError(t, err)
	res := decode[execResult](t, out)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "1 suite, 2 tests, 0 errors", res.Stdout)
	assert.Equal(t, []string{"validate", "--config", "p/tamesu.yaml", "--strict", "a.yaml"}, fe.calls[0])

	out, err = tools[byName["tamesu_validate"]].RunRaw(ctx, map[string]any{"paths": []any{"broken.yaml"}})
	require.NoError(t, err, "a failing command is a result, not a tool error")
	assert.Equal(t, 2, decode[execResult](t, out).ExitCode)
	assert.Contains(t, decode[execResult](t, out).Stderr, "boom")

	_, err = tools[byName["tamesu_list"]].RunRaw(ctx, map[string]any{"filter": "refund", "tags": []any{"smoke", "returns"}, "target": "api"})
	require.NoError(t, err)
	assert.Equal(t, []string{"list", "--json", "--filter", "refund", "--tags", "smoke,returns", "--target", "api"}, fe.calls[2])

	out, err = tools[byName["tamesu_run"]].RunRaw(ctx, map[string]any{"paths": []any{"suite.yaml"}, "repeat": 2.0, "mode": "text", "max_cost_usd": 0.5, "replay": true})
	require.NoError(t, err)
	rr := decode[runResult](t, out)
	assert.Equal(t, 0, rr.ExitCode)
	assert.NotEmpty(t, rr.OutDir, "a temporary run directory was made")
	assert.JSONEq(t, `{"tests":[{"name":"t","passed":true}]}`, string(rr.Summary))
	assert.Contains(t, rr.Console, "PASS t")
	call := fe.calls[3]
	assert.Equal(t, "run", call[0])
	assert.Contains(t, strings.Join(call, " "), "--reporter console --reporter json --out "+rr.OutDir)
	assert.Contains(t, strings.Join(call, " "), "--repeat 2 --mode text --max-cost-usd 0.5 --replay suite.yaml")
	_ = os.RemoveAll(rr.OutDir)

	out, err = tools[byName["tamesu_generate"]].RunRaw(ctx, map[string]any{"source": "# PRD\nreturns within 30 days", "target": "api", "type": "rest", "count": 3.0})
	require.NoError(t, err)
	gr := decode[mcpGenerateResult](t, out)
	assert.Equal(t, 0, gr.ExitCode)
	assert.Equal(t, "suite: drafted\n", gr.SuiteYAML)
	assert.Empty(t, gr.Out, "no out requested: the draft is only returned")
	call = fe.calls[4]
	assert.Equal(t, "generate", call[0])
	src, err := os.ReadFile(flagValue(call, "--from"))
	assert.Error(t, err, "the temporary source file is removed")
	_ = src
	assert.Contains(t, strings.Join(call, " "), "--target api")
	assert.Contains(t, strings.Join(call, " "), "--type rest --count 3")

	_, err = tools[byName["tamesu_generate"]].RunRaw(ctx, map[string]any{"target": "api"})
	assert.Error(t, err, "neither source nor file")

	out, err = tools[byName["tamesu_schema"]].RunRaw(ctx, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, `{"which":"suite"}`, out)
	out, err = tools[byName["tamesu_schema"]].RunRaw(ctx, map[string]any{"which": "config"})
	require.NoError(t, err)
	assert.Equal(t, `{"which":"config"}`, out)

	_, err = tools[byName["tamesu_init"]].RunRaw(ctx, map[string]any{"dir": "proj", "force": true})
	require.NoError(t, err)
	assert.Equal(t, []string{"init", "--force", "proj"}, fe.calls[len(fe.calls)-1])
}

// TestMCPServerListsAndCallsTools goes through the MCP protocol with an
// in-process client: tools/list advertises the tools with their schemas and
// tools/call runs one.
func TestMCPServerListsAndCallsTools(t *testing.T) {
	ctx := context.Background()
	g, err := initBareGenkit(ctx)
	require.NoError(t, err)
	fe := &fakeExec{}
	DefineMCPTools(g, MCPOptions{Exec: fe.exec, Schema: func(string) (string, error) { return "{}", nil }, RunsDir: t.TempDir()})
	srv := mcp.NewMCPServer(g, mcp.MCPServerOptions{Name: "tamesu", Version: "test"})
	c, err := mcpclient.NewInProcessClient(srv.GetServer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.Start(ctx))
	_, err = c.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION, ClientInfo: mcpgo.Implementation{Name: "test", Version: "0"}}})
	require.NoError(t, err)

	list, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	require.NoError(t, err)
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		if tool.Name == "tamesu_run" {
			b, _ := json.Marshal(tool.InputSchema)
			assert.Contains(t, string(b), `"max_cost_usd"`, "the input schema travels with the tool")
		}
	}
	assert.ElementsMatch(t, []string{"tamesu_validate", "tamesu_list", "tamesu_run", "tamesu_generate", "tamesu_doctor", "tamesu_init", "tamesu_schema"}, names)

	req := mcpgo.CallToolRequest{}
	req.Params.Name = "tamesu_validate"
	req.Params.Arguments = map[string]any{"paths": []any{"suite.yaml"}}
	res, err := c.CallTool(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, `"exit_code":0`)
	assert.Contains(t, text.Text, "1 suite, 2 tests, 0 errors")
	assert.Equal(t, []string{"validate", "suite.yaml"}, fe.calls[0])
}

// runsFixture writes one run directory with a summary and a test result.
func runsFixture(t *testing.T) string {
	t.Helper()
	runs := t.TempDir()
	dir := filepath.Join(runs, "2026-11-02T15-11-36")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tests", "refund", "run-1"), 0o755))
	summary := `{"started_at":"2026-11-02T15:11:36Z","tests":[{"name":"refund","passed":true,"runs":[{"passed":true}]},{"name":"other","passed":false,"runs":[{"passed":false}]}]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), []byte(summary), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html>report</html>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tests", "refund", "run-1", "result.json"), []byte(`{"passed":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runs, "notes.txt"), []byte("not a run"), 0o644))
	return runs
}

func mcpClient(t *testing.T, g *genkit.Genkit) *mcpclient.Client {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewMCPServer(g, mcp.MCPServerOptions{Name: "tamesu", Version: "test"})
	c, err := mcpclient.NewInProcessClient(srv.GetServer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.Start(ctx))
	_, err = c.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION, ClientInfo: mcpgo.Implementation{Name: "test", Version: "0"}}})
	require.NoError(t, err)
	return c
}

func readText(t *testing.T, c *mcpclient.Client, uri string) string {
	t.Helper()
	req := mcpgo.ReadResourceRequest{}
	req.Params.URI = uri
	res, err := c.ReadResource(context.Background(), req)
	require.NoError(t, err, uri)
	require.NotEmpty(t, res.Contents)
	text, ok := res.Contents[0].(mcpgo.TextResourceContents)
	require.True(t, ok)
	return text.Text
}

func TestMCPResources(t *testing.T) {
	ctx := context.Background()
	g, err := initBareGenkit(ctx)
	require.NoError(t, err)
	runs := runsFixture(t)
	DefineMCPResources(g, runs)
	c := mcpClient(t, g)

	list, err := c.ListResources(ctx, mcpgo.ListResourcesRequest{})
	require.NoError(t, err)
	var uris []string
	for _, r := range list.Resources {
		uris = append(uris, r.URI)
	}
	assert.Contains(t, uris, RunsURI)
	templates, err := c.ListResourceTemplates(ctx, mcpgo.ListResourceTemplatesRequest{})
	require.NoError(t, err)
	var tpls []string
	for _, r := range templates.ResourceTemplates {
		tpls = append(tpls, r.URITemplate.Raw())
	}
	assert.ElementsMatch(t, []string{RunFileTemplate, RunTestURITemplate}, tpls)

	var entries []runEntry
	require.NoError(t, json.Unmarshal([]byte(readText(t, c, RunsURI)), &entries))
	require.Len(t, entries, 1, "notes.txt is not a run")
	assert.Equal(t, runEntry{Run: "2026-11-02T15-11-36", URI: "tamesu://runs/2026-11-02T15-11-36/summary.json", StartedAt: "2026-11-02T15:11:36Z", Passed: 1, Failed: 1, Tests: 2}, entries[0])

	assert.Contains(t, readText(t, c, "tamesu://runs/2026-11-02T15-11-36/summary.json"), `"name":"refund"`)
	assert.Equal(t, "<html>report</html>", readText(t, c, "tamesu://runs/2026-11-02T15-11-36/report.html"))
	assert.Equal(t, `{"passed":true}`, readText(t, c, "tamesu://runs/2026-11-02T15-11-36/tests/refund/run-1/result.json"))

	for _, bad := range []string{
		"tamesu://runs/2026-11-02T15-11-36/notes.txt", // not a run file
		"tamesu://runs/../notes.txt",                  // traversal
		"tamesu://runs/2026-11-02T15-11-36/tests/refund/run-1/secrets.env",
	} {
		req := mcpgo.ReadResourceRequest{}
		req.Params.URI = bad
		_, err := c.ReadResource(ctx, req)
		assert.Error(t, err, bad)
	}
}

func TestServeMCPOverHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	fe := &fakeExec{}
	done := make(chan error, 1)
	go func() {
		done <- ServeMCP(ctx, MCPOptions{Version: "test", Exec: fe.exec, Schema: func(string) (string, error) { return "{}", nil },
			HTTPAddr: addr, HTTPPath: "/tamesu", RunsDir: t.TempDir()})
	}()
	var c *mcpclient.Client
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "the HTTP server listens")
	c, err = mcpclient.NewStreamableHttpClient("http://" + addr + "/tamesu")
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.Start(ctx))
	_, err = c.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION, ClientInfo: mcpgo.Implementation{Name: "test", Version: "0"}}})
	require.NoError(t, err)
	list, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	require.NoError(t, err)
	assert.Len(t, list.Tools, 7)
	assert.Contains(t, readText(t, c, RunsURI), "[]", "no runs yet")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "a cancelled context is a clean stop")
	case <-time.After(10 * time.Second):
		t.Fatal("ServeMCP did not return after cancel")
	}
}

func TestServeMCPOverHTTPWithToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	done := make(chan error, 1)
	go func() {
		done <- ServeMCP(ctx, MCPOptions{Exec: (&fakeExec{}).exec, Schema: func(string) (string, error) { return "{}", nil },
			HTTPAddr: addr, HTTPToken: "s3cret", RunsDir: t.TempDir()})
	}()
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	post := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusUnauthorized, post(""), "no token")
	assert.Equal(t, http.StatusUnauthorized, post("wrong"), "wrong token")
	assert.Equal(t, http.StatusOK, post("s3cret"), "the token opens the door")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("ServeMCP did not return")
	}
}

func TestServeMCPRefusesOpenBind(t *testing.T) {
	err := ServeMCP(context.Background(), MCPOptions{Exec: (&fakeExec{}).exec, Schema: func(string) (string, error) { return "{}", nil }, HTTPAddr: ":0", RunsDir: t.TempDir()})
	assert.ErrorIs(t, err, ErrTokenRequired)
	assert.True(t, loopbackAddr("127.0.0.1:8765"))
	assert.True(t, loopbackAddr("localhost:8765"))
	assert.True(t, loopbackAddr("[::1]:8765"))
	assert.False(t, loopbackAddr(":8765"))
	assert.False(t, loopbackAddr("0.0.0.0:8765"))
	assert.False(t, loopbackAddr("10.0.0.5:8765"))
}
