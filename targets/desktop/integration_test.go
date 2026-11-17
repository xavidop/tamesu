//go:build integration

package desktop

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/pinned"
	"github.com/xavidop/tamesu/target"
)

// editor is the app the integration test drives on a system: a plain text
// editor every installation has (Linux: the one scripts/integration-desktop.sh
// installs).
type editor struct {
	app string
	// menu and item are a menu of the editor and an item of it; menuRole is
	// what the system calls a title of the menu bar.
	menu, item, menuRole string
	// measured says the editor's tree has been seen in a real run, so the
	// test holds it to that; elsewhere what is found is logged for the first
	// runs.
	measured bool
}

var editors = map[string]editor{
	"linux":   {app: "mousepad", menu: "Edit", item: "Select All", menuRole: "menu", measured: true},
	"darwin":  {app: "TextEdit", menu: "Edit", item: "Select All", menuRole: "menuitem", measured: true},
	"windows": {app: "notepad", menu: "Edit", item: "Select all", menuRole: "menuitem"},
}

// The line of a document's text area, whatever the toolkit calls it, and
// failing that the line of a single-line field (which is where a search box
// or a file name goes, so it is only the second choice).
var (
	areaRe  = regexp.MustCompile(`^\[\d+\] +(textarea|document|edit)\b`)
	fieldRe = regexp.MustCompile(`^\[\d+\] +(textfield|textbox|entry|text)\b`)
)

// textArea returns the ref of the editor's text area in a tree.
func textArea(tree string) (int, bool) {
	if ref, ok := refWhere(tree, areaRe.MatchString); ok {
		return ref, true
	}
	return refWhere(tree, fieldRe.MatchString)
}

var refLineRe = regexp.MustCompile(`^\[(\d+)\]`)

// refWhere returns the ref of the first line the predicate accepts.
func refWhere(tree string, accept func(line string) bool) (int, bool) {
	for _, l := range strings.Split(tree, "\n") {
		if m := refLineRe.FindStringSubmatch(l); m != nil && accept(l) {
			n, err := strconv.Atoi(m[1])
			return n, err == nil
		}
	}
	return 0, false
}

// TestIntegrationDesktop drives a real editor with the real agent-desktop
// CLI: the pinned version (downloaded and checked against its pin when
// TAMESU_TEST_DOWNLOAD=1), or the one TAMESU_AGENT_DESKTOP names.
// TAMESU_DESKTOP_TEST_APP names another editor.
func TestIntegrationDesktop(t *testing.T) {
	ed, known := editors[runtime.GOOS]
	if app := os.Getenv("TAMESU_DESKTOP_TEST_APP"); app != "" {
		ed = editor{app: app, menu: ed.menu, item: ed.item}
	} else if !known {
		t.Skipf("no editor is known for %s; set TAMESU_DESKTOP_TEST_APP", runtime.GOOS)
	}
	tool, err := pinned.Lookup(pinned.AgentDesktop)
	require.NoError(t, err)
	found, err := tool.Find(context.Background(), pinned.Options{NoDownload: os.Getenv("TAMESU_TEST_DOWNLOAD") != "1"})
	require.NoError(t, err, "agent-desktop is needed for the integration test")
	version, err := pinned.VersionOf(context.Background(), found.Path)
	require.NoError(t, err)
	if found.Pinned {
		assert.Equal(t, tool.Version, version, "the binary in the cache is the pinned version")
	}
	t.Logf("agent-desktop %s (%s, pin %s) driving %s on %s", version, found.Path, tool.Version, ed.app, runtime.GOOS)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The test types into the editor's front document and closes the editor:
	// it does not touch one that is already open with someone's work in it.
	probe := &cli{path: found.Path, timeout: DefaultStepTimeout}
	probe.run = probe.exec
	var running struct {
		Apps []struct {
			Name string `json:"name"`
		} `json:"apps"`
	}
	if err := probe.call(ctx, nil, &running, "list-apps", "--app", ed.app); err == nil && len(running.Apps) > 0 {
		t.Skipf("%s is already running; close it first: the test types into its front document and quits it", ed.app)
	}

	dir := t.TempDir()
	sess, err := New("editor", Config{App: ed.app, ExecPath: found.Path}).Start(ctx, &target.RunContext{TestName: t.Name(), Dir: dir, Render: func(s string) (string, error) {
		return strings.ReplaceAll(s, "{{secrets.TEST_PASSWORD}}", "hunter2"), nil
	}})
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_, _ = sess.Close(context.Background())
		}
	})
	page := sess.(target.BrowserSession).Page()

	obs, err := page.Observe(ctx, false)
	require.NoError(t, err)
	t.Logf("first observation (%s, %q), %d lines:\n%s", obs.URL, obs.Title, strings.Count(obs.Tree, "\n")+1, obs.Tree)
	assert.NotEmpty(t, obs.URL)
	require.NotEmpty(t, obs.Tree)
	if ed.measured {
		assert.Equal(t, "desktop://"+ed.app, obs.URL)
		assert.NotEmpty(t, obs.Title)
		assert.Less(t, strings.Count(obs.Tree, "\n"), 120, "closed menus are not listed")
		assert.Contains(t, obs.Tree, ed.menuRole+` "Help"`, "the menu bar's titles are listed")
		assert.NotContains(t, obs.Tree, "(hidden)")
	}
	if ed.app == "mousepad" {
		assert.NotContains(t, obs.Tree, `menuitem "About"`, "what a closed menu holds is not")
	}

	// An editor that opens on a file picker instead of a document (TextEdit)
	// is asked for a new one: by the picker's own button, else by the keyboard.
	reobserve := func(why string) {
		require.NoError(t, page.Wait(ctx, time.Second))
		obs, err = page.Observe(ctx, false)
		require.NoError(t, err)
		t.Logf("after %s (%q), %d lines:\n%s", why, obs.Title, strings.Count(obs.Tree, "\n")+1, obs.Tree)
	}
	if button, ok := refWhere(obs.Tree, func(l string) bool { return strings.Contains(l, `button "New Document"`) }); ok {
		require.NoError(t, page.Click(ctx, button))
		reobserve(`clicking "New Document"`)
	}
	field, ok := textArea(obs.Tree)
	if !ok {
		newDoc := "ctrl+n"
		if runtime.GOOS == "darwin" {
			newDoc = "cmd+n"
		}
		require.NoError(t, page.Press(ctx, newDoc))
		reobserve("pressing " + newDoc)
		field, ok = textArea(obs.Tree)
	}
	require.True(t, ok, "the editor shows no text area")

	// Typed through the accessibility API; a secret is rendered on the way.
	err = page.Type(ctx, field, "tamesu drove this with {{secrets.TEST_PASSWORD}}")
	if err != nil && !ed.measured {
		// Where the tree is not measured yet, a refusal is worth a second
		// look: does the same text go in when agent-desktop may take the
		// focus and use the real keyboard?
		t.Logf("type into [%d] was refused: %v", field, err)
		s := sess.(*session)
		e, eerr := s.element(field)
		require.NoError(t, eerr)
		herr := s.cli.call(ctx, []byte("tamesu drove this with hunter2"), nil, "type", e.ref, "--stdin", "--headed")
		t.Logf("the same with --headed: %v", herr)
		require.NoError(t, herr, "the text area takes text neither way")
		err = nil
	}
	require.NoError(t, err)
	did, err := page.Do(ctx, map[string]any{"wait": map[string]any{"text": "tamesu drove this with hunter2"}})
	require.NoError(t, err, did)
	did, err = page.Do(ctx, map[string]any{"assert_text": map[string]any{"matches": `drove this with hunter\d`}})
	require.NoError(t, err, did)
	_, err = page.Do(ctx, map[string]any{"assert_text": "this was never typed"})
	var se *target.StepError
	require.ErrorAs(t, err, &se)

	// The same field keeps its number, and typing again replaces what was there.
	again, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Contains(t, again.Tree, "tamesu drove this with hunter2")
	sameField, ok := textArea(again.Tree)
	require.True(t, ok)
	assert.Equal(t, field, sameField, "the field keeps its number across observations")
	if err := page.Type(ctx, field, "second draft"); err != nil && !ed.measured {
		t.Logf("typing again into [%d] was refused: %v", field, err)
	} else {
		require.NoError(t, err)
		text, err := page.Text(ctx)
		require.NoError(t, err)
		assert.Contains(t, text, "second draft")
		assert.NotContains(t, text, "hunter2")
	}

	// A menu lists its items once it is open; a step names one, and a key
	// goes to the app.
	if ed.measured {
		menu, ok := refWhere(again.Tree, func(l string) bool { return strings.Contains(l, ed.menuRole+` "`+ed.menu+`"`) })
		require.True(t, ok, "no %s menu in:\n%s", ed.menu, again.Tree)
		require.NoError(t, page.Click(ctx, menu))
		opened, err := page.Observe(ctx, false)
		require.NoError(t, err)
		assert.Contains(t, opened.Tree, `menuitem "`+ed.item+`"`, "an open menu lists its items")
		did, err = page.Do(ctx, map[string]any{"click": map[string]any{"role": "menuitem", "name": ed.item}})
		require.NoError(t, err, did)
		did, err = page.Do(ctx, map[string]any{"press": "Escape"})
		require.NoError(t, err, did)
	} else {
		// Not held to anything yet on this system: what happens is logged.
		did, err = page.Do(ctx, map[string]any{"click": ed.menu})
		t.Logf("click %q: %q %v", ed.menu, did, err)
		opened, oerr := page.Observe(ctx, false)
		t.Logf("with the %s menu asked for (%v), %d lines:\n%s", ed.menu, oerr, strings.Count(opened.Tree, "\n")+1, opened.Tree)
		did, err = page.Do(ctx, map[string]any{"click": ed.item})
		t.Logf("click %q: %q %v", ed.item, did, err)
		did, err = page.Do(ctx, map[string]any{"press": "Escape"})
		t.Logf("press Escape: %q %v", did, err)
	}
	_, err = page.Do(ctx, map[string]any{"click": "No such control anywhere"})
	assert.ErrorContains(t, err, "no element is")

	// A ref that is not in the last observation is refused before
	// agent-desktop is asked.
	assert.ErrorContains(t, page.Click(ctx, 99999), "unknown ref 99999")

	if png, err := page.Screenshot(ctx); err == nil {
		assert.True(t, strings.HasPrefix(string(png), "\x89PNG"))
		t.Logf("screenshot: %d bytes", len(png))
	} else {
		t.Logf("no screenshot on this desktop: %v", err)
	}

	art, err := sess.Close(ctx)
	closed = true
	require.NoError(t, err)
	assert.Contains(t, art.Files, TraceFile)
	_, err = os.Stat(filepath.Join(dir, TraceFile))
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, art.Metrics["desktop_actions"], 2.0)
	kinds := make([]string, 0, len(art.Events))
	for _, e := range art.Events {
		kinds = append(kinds, e.Kind)
		t.Logf("event %s: %s", e.Kind, e.Detail)
	}
	assert.Equal(t, "opened", kinds[0])
	assert.Equal(t, "closed", kinds[len(kinds)-1], "the app the session launched is closed")
}
