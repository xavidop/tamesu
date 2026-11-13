package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xavidop/tamesu/internal/config"
)

// DefaultHookTimeout bounds a hook without a timeout of its own.
const DefaultHookTimeout = 60 * time.Second

// hookRun is one set of hooks to run with their context: the scope to render
// templates, the environment of commands and the log file to append to.
type hookRun struct {
	stage  string
	hooks  []config.Hook
	scope  config.Scope
	env    map[string]string
	log    string
	client *http.Client
}

// hooksOf joins the project's and the suite's hooks for a stage, the
// project's first.
func hooksOf(p config.Project, s *config.Suite, pick func(*config.Hooks) []config.Hook) []config.Hook {
	var out []config.Hook
	if p.Hooks != nil {
		out = append(out, pick(p.Hooks)...)
	}
	if s != nil && s.Hooks != nil {
		out = append(out, pick(s.Hooks)...)
	}
	return out
}

func beforeAll(h *config.Hooks) []config.Hook  { return h.BeforeAll }
func afterAll(h *config.Hooks) []config.Hook   { return h.AfterAll }
func beforeEach(h *config.Hooks) []config.Hook { return h.BeforeEach }
func afterEach(h *config.Hooks) []config.Hook  { return h.AfterEach }

// run runs the hooks in order and stops at the first failure, which names
// the stage and the hook. Output and errors are appended to the log file;
// the rendered command is never logged, since it may carry a secret.
func (h hookRun) run(ctx context.Context) error {
	for i, hk := range h.hooks {
		timeout := hk.Timeout.Std()
		if timeout <= 0 {
			timeout = DefaultHookTimeout
		}
		hctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := h.one(hctx, hk)
		cancel()
		h.appendLog(fmt.Sprintf("== %s[%d] %s\n%s", h.stage, i, describeHook(hk), out))
		if err != nil {
			h.appendLog(fmt.Sprintf("-- failed: %v\n", err))
			return fmt.Errorf("%s hook %d (%s): %w", h.stage, i, describeHook(hk), err)
		}
	}
	return nil
}

func describeHook(hk config.Hook) string {
	if hk.HTTP != nil {
		m := hk.HTTP.Method
		if m == "" {
			m = http.MethodPost
		}
		return m + " " + hk.HTTP.URL
	}
	return "command " + strings.Join(strings.Fields(hk.Command), " ")
}

func (h hookRun) one(ctx context.Context, hk config.Hook) (string, error) {
	if hk.HTTP != nil {
		return h.httpHook(ctx, hk.HTTP)
	}
	cmdText, err := config.Render(hk.Command, h.scope)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdText)
	cmd.Env = os.Environ()
	for k, v := range h.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return buf.String(), fmt.Errorf("timed out")
		}
		return buf.String(), err
	}
	return buf.String(), nil
}

func (h hookRun) httpHook(ctx context.Context, t *config.HTTPTrigger) (string, error) {
	u, err := config.Render(t.URL, h.scope)
	if err != nil {
		return "", fmt.Errorf("render url: %w", err)
	}
	method := t.Method
	if method == "" {
		method = http.MethodPost
	}
	var body io.Reader
	if t.Body != nil {
		rendered, err := renderValue(t.Body, h.scope)
		if err != nil {
			return "", fmt.Errorf("render body: %w", err)
		}
		if s, ok := rendered.(string); ok {
			body = strings.NewReader(s)
		} else {
			b, err := json.Marshal(rendered)
			if err != nil {
				return "", err
			}
			body = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return "", err
	}
	if t.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range t.Headers {
		rv, err := config.Render(v, h.scope)
		if err != nil {
			return "", fmt.Errorf("render header %s: %w", k, err)
		}
		req.Header.Set(k, rv)
	}
	client := h.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	out := fmt.Sprintf("%s\n%s\n", resp.Status, b)
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("%s", resp.Status)
	}
	return out, nil
}

// renderValue renders every string leaf of v through the scope.
func renderValue(v any, sc config.Scope) (any, error) {
	switch x := v.(type) {
	case string:
		return config.Render(x, sc)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := renderValue(e, sc)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := renderValue(e, sc)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

func (h hookRun) appendLog(text string) {
	if h.log == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.log), 0o755)
	f, err := os.OpenFile(h.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(text)
	_ = f.Close()
}

// suiteScope is the scope of a suite's before_all / after_all hooks: vars
// (the project's, then the suite's, then the run's) and secrets, no run
// values.
func (r *run) suiteScope(s *config.Suite) config.Scope {
	vars := map[string]string{}
	for k, v := range r.o.Project.Vars {
		vars[k] = v
	}
	for k, v := range s.Vars {
		vars[k] = v
	}
	for k, v := range r.o.Vars {
		vars[k] = v
	}
	sc := config.Scope{Vars: vars, Env: r.o.Env, Values: map[string]string{"suite": s.Suite}}
	if r.o.Secrets != nil {
		sc.Secrets = r.o.Secrets.Lookup
	}
	return sc
}

// runSuiteHooks runs before_all or after_all for every selected suite.
func (r *run) runSuiteHooks(ctx context.Context, items []*item, pick func(*config.Hooks) []config.Hook, stage string) error {
	seen := map[*config.Suite]bool{}
	for _, it := range items {
		if seen[it.suite] {
			continue
		}
		seen[it.suite] = true
		hooks := hooksOf(r.o.Project, it.suite, pick)
		if len(hooks) == 0 {
			continue
		}
		hr := hookRun{stage: stage, hooks: hooks, scope: r.suiteScope(it.suite), log: filepath.Join(r.o.OutDir, "hooks.log"),
			env: map[string]string{"TAMESU_SUITE": it.suite.Suite, "TAMESU_OUT_DIR": r.o.OutDir}}
		if err := hr.run(ctx); err != nil {
			return fmt.Errorf("suite %s: %w", it.suite.Suite, err)
		}
	}
	return nil
}
