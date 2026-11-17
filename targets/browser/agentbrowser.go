package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xavidop/tamesu/internal/pinned"
)

// The engines of the browser target: what reads the page's elements and acts
// on a ref. The browser itself, and everything else a session does with it
// (navigation, mocks, the recording, the HAR, the console, storage state),
// is tamesu's own under either engine.
const (
	EngineChromium     = "chromium"
	EngineAgentBrowser = "agent-browser"
)

// Engines lists the engines.
var Engines = []string{EngineChromium, EngineAgentBrowser}

// agentBrowser drives the elements of the page through the agent-browser
// CLI, attached to the Chromium the session launched: its snapshot is the
// observation's tree, its refs (@e7) are the observation's ([7]), and a
// click, a fill or a select on a ref goes through it.
type agentBrowser struct {
	path string
	// port is the DevTools port of the session's Chromium, on loopback.
	port int
	// name is the agent-browser session, one per tamesu session, in a
	// namespace of tamesu's own so a user's sessions are left alone.
	name string
	// timeout bounds one command.
	timeout time.Duration
	// run executes the CLI; tests replace it.
	run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

const agentBrowserNamespace = "tamesu"

var agentBrowserSessions atomic.Int64

// newAgentBrowser finds the CLI (the binary cfg names, TAMESU_AGENT_BROWSER,
// or the pinned version, downloaded when missing) and picks the port the
// session's Chromium will listen on.
func newAgentBrowser(ctx context.Context, cfg Config) (*agentBrowser, error) {
	tool, err := pinned.Lookup(pinned.AgentBrowser)
	if err != nil {
		return nil, err
	}
	found, err := tool.Find(ctx, pinned.Options{Explicit: cfg.AgentBrowserPath, NoDownload: cfg.NoDownload})
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("agent-browser: no free port for the DevTools endpoint: %w", err)
	}
	a := &agentBrowser{path: found.Path, port: port, timeout: cfg.StepTimeout, name: fmt.Sprintf("run-%d-%d", os.Getpid(), agentBrowserSessions.Add(1))}
	a.run = a.exec
	return a, nil
}

// freePort asks the system for a loopback port nothing listens on.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// exec runs one agent-browser command against the session's Chromium and
// returns its JSON. The variables that configure agent-browser are not
// passed on: a session behaves the same on every machine.
func (a *agentBrowser) exec(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	full := append([]string{"--session", a.name, "--namespace", agentBrowserNamespace, "--cdp", strconv.Itoa(a.port), "--json"}, args...)
	cmd := exec.CommandContext(ctx, a.path, full...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AGENT_BROWSER_") {
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
		// A failed command still answers with JSON that says why.
		return stdout.Bytes(), nil
	}
	if err == nil {
		err = errors.New("no output")
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return nil, fmt.Errorf("agent-browser %s: %w%s", args[0], err, excerptOf(stderr.String()))
}

func excerptOf(stderr string) string {
	s := strings.TrimSpace(stderr)
	if s == "" {
		return ""
	}
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return ": " + s
}

// abResult is the envelope of one agent-browser command.
type abResult struct {
	Success bool            `json:"success"`
	Error   json.RawMessage `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// failure is the command's error as text ("" when it has none).
func (r abResult) failure() string {
	if len(r.Error) == 0 || string(r.Error) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(r.Error, &s) == nil {
		return s
	}
	return string(r.Error)
}

// snapshot returns agent-browser's accessibility tree of the page.
func (a *agentBrowser) snapshot(ctx context.Context) (string, error) {
	out, err := a.run(ctx, nil, "snapshot")
	if err != nil {
		return "", err
	}
	var res abResult
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("agent-browser snapshot: unreadable output: %w", err)
	}
	if !res.Success {
		return "", fmt.Errorf("agent-browser snapshot: %s", res.failure())
	}
	var data struct {
		Snapshot string `json:"snapshot"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		return "", fmt.Errorf("agent-browser snapshot: unreadable output: %w", err)
	}
	return data.Snapshot, nil
}

var unknownRefRe = regexp.MustCompile(`(?i)unknown ref:? *@?e(\d+)`)

// act runs one command on a ref. The command travels on standard input, so
// what is typed (a secret, once rendered) is in no process's arguments.
func (a *agentBrowser) act(ctx context.Context, command ...string) error {
	stdin, err := json.Marshal([][]string{command})
	if err != nil {
		return err
	}
	out, err := a.run(ctx, stdin, "batch", "--bail", "--omit-command")
	if err != nil {
		return err
	}
	var results []abResult
	if err := json.Unmarshal(out, &results); err != nil {
		// A batch that could not start answers with one envelope.
		var one abResult
		if json.Unmarshal(out, &one) != nil {
			return fmt.Errorf("agent-browser %s: unreadable output", command[0])
		}
		results = []abResult{one}
	}
	for _, r := range results {
		if r.Success {
			continue
		}
		msg := r.failure()
		if m := unknownRefRe.FindStringSubmatch(msg); m != nil {
			return fmt.Errorf("unknown ref %s; observe the page first", m[1])
		}
		if msg == "" {
			msg = "failed"
		}
		return errors.New(msg)
	}
	if len(results) == 0 {
		return fmt.Errorf("agent-browser %s: no result", command[0])
	}
	return nil
}

func abRef(ref int) string { return "@e" + strconv.Itoa(ref) }

func (a *agentBrowser) click(ctx context.Context, ref int) error {
	return a.act(ctx, "click", abRef(ref))
}

func (a *agentBrowser) fill(ctx context.Context, ref int, text string) error {
	return a.act(ctx, "fill", abRef(ref), text)
}

func (a *agentBrowser) selectOption(ctx context.Context, ref int, value string) error {
	return a.act(ctx, "select", abRef(ref), value)
}

// close ends agent-browser's side of the session (its background process).
func (a *agentBrowser) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = a.run(ctx, nil, "close")
}

// abLineRe reads one line of agent-browser's snapshot:
//
//   - combobox "Plan" [expanded=false, ref=e7]: Free
//   - generic [ref=e2] clickable [cursor:pointer, onclick]
var abLineRe = regexp.MustCompile(`^(\s*)- ([^\s"\[:]+)(?: "((?:[^"\\]|\\.)*)")?(?: \[([^\]]*)\])?(.*)$`)

var abHintRe = regexp.MustCompile(`hint="((?:[^"\\]|\\.)*)"`)

type abLine struct {
	depth  int
	role   string
	name   string
	value  string
	ref    int
	states []string
}

// parseAgentBrowserLine reads one snapshot line; ok is false for a line that
// is not an element.
func parseAgentBrowserLine(line string) (abLine, bool) {
	m := abLineRe.FindStringSubmatch(line)
	if m == nil {
		return abLine{}, false
	}
	l := abLine{depth: len(m[1]) / 2, role: m[2], name: unescape(m[3])}
	for _, attr := range strings.Split(m[4], ",") {
		attr = strings.TrimSpace(attr)
		k, v, has := strings.Cut(attr, "=")
		switch {
		case attr == "":
		case k == "ref":
			if n, err := strconv.Atoi(strings.TrimPrefix(v, "e")); err == nil {
				l.ref = n
			}
		case k == "level":
			l.states = append(l.states, "h"+v)
		case !has || v == "true":
			l.states = append(l.states, k)
		case v != "false":
			l.states = append(l.states, attr)
		}
	}
	rest := m[5]
	if v, ok := strings.CutPrefix(rest, ": "); ok {
		l.value = strings.TrimSpace(v)
	}
	if l.name == "" {
		if h := abHintRe.FindStringSubmatch(rest); h != nil {
			l.name = unescape(h[1])
		}
	}
	return l, true
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s))
}

// agentBrowserTree turns agent-browser's snapshot into the observation's
// tree, in the lines the built-in engine writes (`[ref] role "name" (state)`),
// and returns the elements that have a ref.
func agentBrowserTree(snapshot string) (string, map[int]refNode) {
	var parsed []abLine
	for _, raw := range strings.Split(snapshot, "\n") {
		if l, ok := parseAgentBrowserLine(raw); ok {
			parsed = append(parsed, l)
		}
	}
	refs := map[int]refNode{}
	var lines []string
	for i, l := range parsed {
		if len(lines) >= maxTreeLines {
			break
		}
		text := l.role == "StaticText" || l.role == "paragraph" || l.role == "LabelText"
		switch {
		case l.ref > 0:
			if l.name == "" {
				// An element known by its text only: the text beneath it.
				l.name = textBeneath(parsed, i)
			}
			refs[l.ref] = refNode{role: l.role, name: l.name, ref: l.ref}
		case keepRoles[l.role] && l.name != "":
		default:
			continue
		}
		var b strings.Builder
		if l.ref > 0 {
			fmt.Fprintf(&b, "[%d] ", l.ref)
		} else {
			b.WriteString("    ")
		}
		b.WriteString(strings.Repeat(" ", min(l.depth, 12)))
		if l.ref == 0 && text {
			name := l.name
			if len(name) > 200 {
				name = name[:197] + "..."
			}
			b.WriteString(`text "` + name + `"`)
			lines = append(lines, b.String())
			continue
		}
		b.WriteString(l.role)
		if l.name != "" {
			fmt.Fprintf(&b, " %q", l.name)
		}
		if l.value != "" && l.value != l.name {
			fmt.Fprintf(&b, " value=%q", l.value)
		}
		if len(l.states) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(l.states, ", "))
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n"), refs
}

// textBeneath joins the text under line i, cut to a name's length.
func textBeneath(lines []abLine, i int) string {
	var parts []string
	size := 0
	for _, l := range lines[i+1:] {
		if l.depth <= lines[i].depth || size > 80 {
			break
		}
		if l.role == "StaticText" && l.name != "" {
			parts = append(parts, l.name)
			size += len(l.name)
		}
	}
	text := strings.Join(parts, " ")
	if len(text) > 80 {
		text = text[:77] + "..."
	}
	return text
}

// agentBrowserObserve reads the page's elements through agent-browser and
// remembers what each ref is.
func (s *session) agentBrowserObserve(ctx context.Context) (string, error) {
	snap, err := s.ab.snapshot(ctx)
	if err != nil {
		return "", err
	}
	tree, refs := agentBrowserTree(snap)
	s.mu.Lock()
	for ref, n := range refs {
		s.refs[ref] = n
	}
	s.mu.Unlock()
	return tree, nil
}
