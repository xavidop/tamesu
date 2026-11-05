package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"

	"github.com/xavidop/tamesu/internal/config"
)

// openSession performs a target's session.open request once per run: one
// HTTP call (create a conversation, mint a token) whose captures are
// returned by name, to render as {{session.NAME}} in the target's
// configuration whatever its type.
func openSession(ctx context.Context, open *config.RESTOpen, render func(string) (string, error), timeout time.Duration, logger *slog.Logger) (map[string]string, error) {
	method := strings.ToUpper(open.Method)
	if method == "" {
		method = http.MethodPost
	}
	u, err := render(open.URL)
	if err != nil {
		return nil, fmt.Errorf("session.open: url: %w", err)
	}
	var body []byte
	if open.Body != nil {
		rendered, err := renderOpenValue(open.Body, render)
		if err != nil {
			return nil, fmt.Errorf("session.open: body: %w", err)
		}
		if str, ok := rendered.(string); ok {
			body = []byte(str)
		} else if body, err = oj.Marshal(rendered); err != nil {
			return nil, fmt.Errorf("session.open: body: %w", err)
		}
	}
	captures := map[string]jp.Expr{}
	for name, path := range open.Capture {
		x, err := jp.ParseString(path)
		if err != nil {
			return nil, fmt.Errorf("session.open: capture %s %q: %w", name, path, err)
		}
		captures[name] = x
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(rctx, method, u, rd)
	if err != nil {
		return nil, fmt.Errorf("session.open: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range open.Headers {
		rv, err := render(v)
		if err != nil {
			return nil, fmt.Errorf("session.open: header %s: %w", k, err)
		}
		req.Header.Set(k, rv)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("session.open: %s %s: %w", method, u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return nil, fmt.Errorf("session.open: %s %s: HTTP %d: %s", method, u, resp.StatusCode, msg)
	}
	out := map[string]string{}
	if len(captures) > 0 {
		data, err := oj.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("session.open: %s %s: the reply is not JSON", method, u)
		}
		for name, x := range captures {
			if got := x.Get(data); len(got) > 0 {
				out[name] = scalarOf(got[0])
			}
		}
	}
	if logger != nil {
		logger.Debug("session opened", "method", method, "url", u, "status", resp.StatusCode, "captures", len(out))
	}
	return out, nil
}

// renderOpenValue renders every string of a JSON-like value.
func renderOpenValue(v any, render func(string) (string, error)) (any, error) {
	switch x := v.(type) {
	case string:
		return render(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			r, err := renderOpenValue(val, render)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			r, err := renderOpenValue(val, render)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

func scalarOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, err := oj.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}
