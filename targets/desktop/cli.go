package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// contractMajor is the major version of agent-desktop's JSON output this
// package reads ("2.6" is 2).
const contractMajor = "2"

// cli runs the agent-desktop CLI: every command prints one JSON envelope.
type cli struct {
	path string
	// trace is the file agent-desktop appends its trace of the session to;
	// empty for none.
	trace string
	// timeout bounds one command.
	timeout time.Duration
	// run executes the CLI; tests replace it.
	run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

// envelope is what every agent-desktop command prints.
type envelope struct {
	Version string          `json:"version"`
	OK      bool            `json:"ok"`
	Command string          `json:"command"`
	Data    json.RawMessage `json:"data"`
	Error   *Error          `json:"error"`
}

// Error is a failed agent-desktop command.
type Error struct {
	// Code is agent-desktop's error code, such as STALE_REF or PERM_DENIED.
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

func (e *Error) Error() string {
	switch e.Code {
	case "STALE_REF", "AMBIGUOUS_TARGET", "SNAPSHOT_NOT_FOUND":
		return fmt.Sprintf("the element is not where it was (%s); observe the window again and use its new refs", e.Code)
	}
	msg := e.Message
	if msg == "" {
		msg = e.Code
	}
	if e.Suggestion != "" {
		msg += ". " + e.Suggestion
	}
	return msg
}

// exec runs one command. The variables that point agent-desktop at a session
// of the user's are not passed on.
func (c *cli) exec(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.path, args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AGENT_DESKTOP_SESSION=") && !strings.HasPrefix(kv, "AGENT_DESKTOP_AGENT_ID=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if stdout.Len() > 0 {
		// A failed command still prints an envelope that says why.
		return stdout.Bytes(), nil
	}
	if err == nil {
		err = errors.New("no output")
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	detail := strings.TrimSpace(stderr.String())
	if len(detail) > 300 {
		detail = detail[:297] + "..."
	}
	if detail != "" {
		detail = ": " + detail
	}
	return nil, fmt.Errorf("agent-desktop %s: %w%s", args[0], err, detail)
}

// call runs a command and decodes its data into out (when out is not nil).
// stdin carries what must not be an argument (text that is typed).
func (c *cli) call(ctx context.Context, stdin []byte, out any, args ...string) error {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	if c.trace != "" && traced[args[0]] {
		args = append(args, "--trace", c.trace)
	}
	raw, err := c.run(ctx, stdin, args...)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(bytes.TrimSpace(raw), &env); err != nil {
		return fmt.Errorf("agent-desktop %s: unreadable output: %w", args[0], err)
	}
	if major, _, _ := strings.Cut(env.Version, "."); major != contractMajor {
		return fmt.Errorf("agent-desktop %s answered in output version %q; this tamesu reads version %s.x", args[0], env.Version, contractMajor)
	}
	if !env.OK {
		if env.Error == nil {
			return fmt.Errorf("agent-desktop %s failed without saying why", args[0])
		}
		return env.Error
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("agent-desktop %s: unreadable data: %w", args[0], err)
	}
	return nil
}

// traced are the commands whose trace is worth keeping: what was seen and
// what was done.
var traced = map[string]bool{
	"snapshot": true, "find": true, "click": true, "type": true, "clear": true, "select": true, "press": true,
	"launch": true, "close-app": true,
}

// hasCode reports whether err is an agent-desktop error with one of the codes.
func hasCode(err error, codes ...string) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	for _, c := range codes {
		if e.Code == c {
			return true
		}
	}
	return false
}
