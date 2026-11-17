package browser

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/pinned"
	"github.com/xavidop/tamesu/target"
)

// A snapshot as agent-browser 0.43 prints it.
const agentBrowserSnapshot = `- heading "Sign in" [level=1, ref=e1]
- form
  - LabelText
    - StaticText "Email "
    - textbox "Email" [ref=e6]: a@b.co
      - StaticText "a@b.co"
  - LabelText
    - StaticText "Plan "
    - combobox "Plan" [expanded=false, ref=e7]: Pro
      - MenuListPopup
        - option "Free" [ref=e8]
        - option "Pro" [selected, ref=e9]
  - checkbox "Accept terms" [checked=false, ref=e4]
  - checkbox "Remember \"me\"" [checked=mixed, disabled, ref=e11]
  - button "Log in" [ref=e5]
  - button [ref=e12] hint="close icon"
- paragraph
  - StaticText "hello a@b.co"
- generic [ref=e2] clickable [cursor:pointer, onclick]
  - StaticText "plain"
  - StaticText "div"
- Iframe [ref=e3]
  - button "Inner button" [ref=e10]`

func TestAgentBrowserTree(t *testing.T) {
	tree, refs := agentBrowserTree(agentBrowserSnapshot)
	assert.Equal(t, strings.Join([]string{
		`[1] heading "Sign in" (h1)`,
		`      text "Email"`,
		`[6]   textbox "Email" value="a@b.co"`,
		`       text "a@b.co"`,
		`      text "Plan"`,
		`[7]   combobox "Plan" value="Pro"`,
		`[8]     option "Free"`,
		`[9]     option "Pro" (selected)`,
		`[4]  checkbox "Accept terms"`,
		`[11]  checkbox "Remember \"me\"" (checked=mixed, disabled)`,
		`[5]  button "Log in"`,
		`[12]  button "close icon"`,
		`     text "hello a@b.co"`,
		`[2] generic "plain div"`,
		`     text "plain"`,
		`     text "div"`,
		`[3] Iframe`,
		`[10]  button "Inner button"`,
	}, "\n"), tree)
	assert.Len(t, refs, 12)
	assert.Equal(t, refNode{role: "generic", name: "plain div", ref: 2}, refs[2])
	assert.Equal(t, refNode{role: "button", name: "close icon", ref: 12}, refs[12])

	empty, none := agentBrowserTree("")
	assert.Empty(t, empty)
	assert.Empty(t, none)
}

func TestAgentBrowserTreeIsCapped(t *testing.T) {
	tree, _ := agentBrowserTree(strings.Repeat("- button \"x\" [ref=e1]\n", maxTreeLines+50))
	assert.Len(t, strings.Split(tree, "\n"), maxTreeLines)
}

func TestAgentBrowserActSendsTheCommandOnStdin(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	answer := `[{"error":null,"index":0,"result":{"filled":"@e6"},"success":true}]`
	a := &agentBrowser{run: func(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
		gotArgs, gotStdin = args, string(stdin)
		return []byte(answer), nil
	}}
	ctx := context.Background()

	require.NoError(t, a.fill(ctx, 6, `s3cret "quoted"`))
	assert.Equal(t, []string{"batch", "--bail", "--omit-command"}, gotArgs, "what is typed is not an argument")
	var sent [][]string
	require.NoError(t, json.Unmarshal([]byte(gotStdin), &sent))
	assert.Equal(t, [][]string{{"fill", "@e6", `s3cret "quoted"`}}, sent)

	require.NoError(t, a.click(ctx, 5))
	assert.Contains(t, gotStdin, `["click","@e5"]`)
	require.NoError(t, a.selectOption(ctx, 7, "Pro"))
	assert.Contains(t, gotStdin, `["select","@e7","Pro"]`)

	answer = `[{"error":"Unknown ref: e99","index":0,"success":false}]`
	assert.EqualError(t, a.click(ctx, 99), "unknown ref 99; observe the page first")
	answer = `{"success":false,"error":"Invalid JSON input"}`
	assert.EqualError(t, a.click(ctx, 1), "Invalid JSON input")
	answer = `[{"error":"Element is not visible","index":0,"success":false}]`
	assert.EqualError(t, a.click(ctx, 1), "Element is not visible")
	answer = `not json`
	assert.ErrorContains(t, a.click(ctx, 1), "unreadable output")
	answer = `[]`
	assert.ErrorContains(t, a.click(ctx, 1), "no result")
}

func TestAgentBrowserSnapshotErrors(t *testing.T) {
	answer := `{"success":true,"data":{"snapshot":"- button \"Go\" [ref=e1]","refs":{}},"error":null}`
	a := &agentBrowser{run: func(context.Context, []byte, ...string) ([]byte, error) { return []byte(answer), nil }}
	snap, err := a.snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, `- button "Go" [ref=e1]`, snap)

	answer = `{"success":false,"error":"CDP endpoint is not reachable"}`
	_, err = a.snapshot(context.Background())
	assert.EqualError(t, err, "agent-browser snapshot: CDP endpoint is not reachable")
}

func TestUnknownEngine(t *testing.T) {
	_, err := New("web", Config{URL: "http://127.0.0.1:1/", Engine: "netscape", ExecPath: os.Args[0]}).Start(context.Background(), &target.RunContext{})
	assert.ErrorContains(t, err, `engine "netscape" is not one of chromium, agent-browser`)
}

// agentBrowserAvailable skips the test unless the agent-browser CLI can be
// used without a download: TAMESU_AGENT_BROWSER names one, the pinned version
// is in the cache, or TAMESU_TEST_DOWNLOAD=1 allows fetching it.
func agentBrowserAvailable(t *testing.T) {
	t.Helper()
	tool, err := pinned.Lookup(pinned.AgentBrowser)
	require.NoError(t, err)
	if _, err := tool.Find(context.Background(), pinned.Options{NoDownload: os.Getenv("TAMESU_TEST_DOWNLOAD") != "1"}); err != nil {
		t.Skip("no agent-browser on this machine:", err)
	}
}

// TestAgentBrowserEngine drives the shop page with agent-browser attached to
// the session's Chromium: its tree and refs for the agent, tamesu's own
// navigation, console, HAR, recording and deterministic steps beside it.
func TestAgentBrowserEngine(t *testing.T) {
	agentBrowserAvailable(t)
	srv := server(t)
	page, sess, dir := start(t, srv, Config{Viewport: "1000x700", Engine: EngineAgentBrowser, Recording: RecordingGIF})
	ctx := context.Background()

	obs, err := page.Observe(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "Mochi Shoes", obs.Title)
	assert.Equal(t, srv.URL+"/", obs.URL)
	assert.Contains(t, obs.Tree, `heading "Welcome to Mochi Shoes" (h1)`)
	assert.Regexp(t, `\[\d+\] +textbox "Username"`, obs.Tree)
	assert.Regexp(t, `\[\d+\] +button "Add to cart"`, obs.Tree)
	assert.True(t, strings.HasPrefix(string(obs.Screenshot), "\x89PNG"))

	user := refOf(t, obs, `textbox "Username"`)
	pass := refOf(t, obs, `textbox "Password"`)
	require.NoError(t, page.Type(ctx, user, "marta"))
	require.NoError(t, page.Type(ctx, pass, "{{secrets.TEST_PASSWORD}}"))
	typed, err := page.Eval(ctx, "document.getElementById('pass').value")
	require.NoError(t, err)
	assert.Equal(t, "hunter2", typed, "the secret is rendered only when typed")
	require.NoError(t, page.Click(ctx, refOf(t, obs, `button "Log in"`)))
	text, err := page.Text(ctx)
	require.NoError(t, err)
	assert.Contains(t, text, "Hello marta")

	again, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, user, refOf(t, again, `textbox "Username"`), "refs are stable across observations")
	assert.Contains(t, again.Tree, `value="marta"`)

	require.NoError(t, page.Select(ctx, refOf(t, again, `combobox "Size"`), "43"))
	picked, err := page.Eval(ctx, "document.getElementById('picked').textContent")
	require.NoError(t, err)
	assert.Equal(t, "43", picked)

	// The console is still tamesu's own.
	require.NoError(t, page.Click(ctx, refOf(t, again, `button "Add to cart"`)))
	after, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(after.Console, "\n"), "added to cart")

	err = page.Click(ctx, 9999)
	assert.ErrorContains(t, err, "unknown ref 9999")

	// Deterministic steps and navigation stay on the built-in driver, and the
	// allowed domains still hold.
	did, err := page.Do(ctx, map[string]any{"click": `a[href="/cart"]`})
	require.NoError(t, err)
	assert.Contains(t, did, "clicked")
	cart, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "Cart", cart.Title)
	assert.Contains(t, cart.Tree, `heading "Your cart" (h1)`)
	assert.ErrorContains(t, page.Navigate(ctx, "https://example.org/"), "outside allowed_domains")

	art, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Contains(t, art.Files, HARFile)
	assert.Contains(t, art.Files, ConsoleFile)
	assert.Contains(t, art.Files, "recording.gif")
	_, err = os.Stat(dir + "/recording.gif")
	assert.NoError(t, err)

	// agent-browser's background process went with the session.
	assert.Eventually(t, func() bool {
		out, _ := exec.Command("pgrep", "-f", "--", "--session "+sess.(*session).ab.name+" ").Output()
		return strings.TrimSpace(string(out)) == ""
	}, 10*time.Second, 200*time.Millisecond, "agent-browser is still running")
}
