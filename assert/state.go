package assert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	jp "github.com/ohler55/ojg/jp"
)

// DefaultStateTimeout bounds one state check when none is configured.
const DefaultStateTimeout = 30 * time.Second

// HTTPCheck is a state assertion against your backend: after the
// conversation, a request is made and its status and body are checked. It
// closes the loop between what the agent said and what it did.
type HTTPCheck struct {
	URL     string
	Method  string // GET when empty
	Headers map[string]string
	Body    any // a string is sent as is; anything else as JSON
	// Status is the expected status code; nil means any code under 400.
	Status *int
	// JSONPath, when set, must exist in the JSON body and, with Equals not
	// nil, equal it.
	JSONPath string
	Equals   any
	// Contains and Regex check the body text.
	Contains *string
	Regex    *string
	Timeout  time.Duration
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

// HTTPState builds the assertion for an HTTPCheck.
func HTTPState(c HTTPCheck) (Assertion, error) {
	method := strings.ToUpper(c.Method)
	if method == "" {
		method = http.MethodGet
	}
	var expr jp.Expr
	if c.JSONPath != "" {
		var err error
		if expr, err = jp.ParseString(c.JSONPath); err != nil {
			return nil, fmt.Errorf("http json_path %q: %w", c.JSONPath, err)
		}
	}
	var re *regexp.Regexp
	if c.Regex != nil {
		var err error
		if re, err = regexp.Compile(*c.Regex); err != nil {
			return nil, fmt.Errorf("http regex %q: %w", *c.Regex, err)
		}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultStateTimeout
	}
	name := "http " + method + " " + c.URL
	return Func(name, func(ctx context.Context, _ *RunResult) Result {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var body io.Reader
		isJSON := false
		switch b := c.Body.(type) {
		case nil:
		case string:
			body = strings.NewReader(b)
		default:
			raw, err := json.Marshal(b)
			if err != nil {
				return Result{Pass: false, Reason: fmt.Sprintf("encoding the request body: %v", err)}
			}
			body, isJSON = bytes.NewReader(raw), true
		}
		req, err := http.NewRequestWithContext(ctx, method, c.URL, body)
		if err != nil {
			return Result{Pass: false, Reason: fmt.Sprintf("building the request: %v", err)}
		}
		if isJSON {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}
		client := c.Client
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(req)
		if err != nil {
			return Result{Pass: false, Reason: fmt.Sprintf("%s %s: %v", method, c.URL, err)}
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		text := string(raw)
		evidence := []string{fmt.Sprintf("%s %s → %s", method, c.URL, resp.Status), snippet(text)}
		switch {
		case c.Status != nil && resp.StatusCode != *c.Status:
			return Result{Pass: false, Reason: fmt.Sprintf("%s %s returned HTTP %d, want %d", method, c.URL, resp.StatusCode, *c.Status), Evidence: evidence}
		case c.Status == nil && resp.StatusCode >= 400:
			return Result{Pass: false, Reason: fmt.Sprintf("%s %s returned %s", method, c.URL, resp.Status), Evidence: evidence}
		}
		if expr != nil {
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return Result{Pass: false, Reason: fmt.Sprintf("the response is not JSON (%v), so %s cannot be read", err, c.JSONPath), Evidence: evidence}
			}
			got := expr.Get(doc)
			if len(got) == 0 {
				return Result{Pass: false, Reason: fmt.Sprintf("%s is absent from the response", c.JSONPath), Evidence: evidence}
			}
			if c.Equals != nil && !jsonEqual(got[0], c.Equals) {
				return Result{Pass: false, Reason: fmt.Sprintf("%s = %s, want %s", c.JSONPath, jsonString(got[0]), jsonString(c.Equals)), Evidence: evidence}
			}
		}
		if c.Contains != nil && !strings.Contains(text, *c.Contains) {
			return Result{Pass: false, Reason: fmt.Sprintf("the response does not contain %s", quote(*c.Contains)), Evidence: evidence}
		}
		if re != nil && !re.MatchString(text) {
			return Result{Pass: false, Reason: fmt.Sprintf("the response does not match %s", quote(re.String())), Evidence: evidence}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("%s %s returned %s and the checks hold", method, c.URL, resp.Status), Evidence: evidence}
	}), nil
}

// CommandCheck is a state assertion that runs a shell command after the
// conversation and checks its exit code and output.
type CommandCheck struct {
	Run string // sh -c
	// Exit is the expected exit code; nil means 0.
	Exit *int
	// Contains and Regex check the combined output.
	Contains *string
	Regex    *string
	Timeout  time.Duration
	// Dir is the working directory ("" for the current one).
	Dir string
	// Env is added to the command's environment (KEY=value).
	Env []string
}

// CommandState builds the assertion for a CommandCheck.
func CommandState(c CommandCheck) (Assertion, error) {
	if strings.TrimSpace(c.Run) == "" {
		return nil, fmt.Errorf("command needs something to run")
	}
	var re *regexp.Regexp
	if c.Regex != nil {
		var err error
		if re, err = regexp.Compile(*c.Regex); err != nil {
			return nil, fmt.Errorf("command regex %q: %w", *c.Regex, err)
		}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultStateTimeout
	}
	want := 0
	if c.Exit != nil {
		want = *c.Exit
	}
	return Func("command "+quote(oneLine(c.Run, 60)), func(ctx context.Context, _ *RunResult) Result {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", c.Run)
		cmd.Dir = c.Dir
		if len(c.Env) > 0 {
			cmd.Env = append(cmd.Environ(), c.Env...)
		}
		out, err := cmd.CombinedOutput()
		text := string(out)
		code := 0
		if err != nil {
			var exit *exec.ExitError
			switch {
			case ctx.Err() != nil:
				return Result{Pass: false, Reason: fmt.Sprintf("the command did not finish within %s", timeout), Evidence: []string{snippet(text)}}
			case asExitError(err, &exit):
				code = exit.ExitCode()
			default:
				return Result{Pass: false, Reason: fmt.Sprintf("running the command: %v", err), Evidence: []string{snippet(text)}}
			}
		}
		evidence := []string{fmt.Sprintf("exit %d", code), snippet(text)}
		if code != want {
			return Result{Pass: false, Reason: fmt.Sprintf("the command exited with %d, want %d", code, want), Evidence: evidence}
		}
		if c.Contains != nil && !strings.Contains(text, *c.Contains) {
			return Result{Pass: false, Reason: fmt.Sprintf("the output does not contain %s", quote(*c.Contains)), Evidence: evidence}
		}
		if re != nil && !re.MatchString(text) {
			return Result{Pass: false, Reason: fmt.Sprintf("the output does not match %s", quote(re.String())), Evidence: evidence}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("the command exited with %d and the checks hold", code), Evidence: evidence}
	}), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// snippet is the first 300 characters of a body or an output, on one line.
func snippet(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	return oneLine(s, 300)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
