package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

// fake stands in for the agent-desktop CLI: it answers each command with the
// envelope a real one prints and remembers what it was asked.
type fake struct {
	mu sync.Mutex
	// calls are the commands, arguments joined by a space; stdin what the
	// last command read.
	calls []string
	stdin string
	// answer overrides the reply to a command (keyed by its name).
	answer map[string]string
	// running says the app was open before the session.
	running bool
	// sheet adds a sheet on top of the window.
	sheet bool
	// snapshots counts them, so each has an id of its own.
	snapshots int
	// refuseGraceful makes close-app fail unless it is forced.
	refuseGraceful bool
	// platform is what status reports (linux when empty).
	platform string
}

func ok(command, data string) string {
	return `{"version":"2.6","ok":true,"command":"` + command + `","data":` + data + `}`
}

func failed(command, code, message string) string {
	return `{"version":"2.6","ok":false,"command":"` + command + `","error":{"code":"` + code + `","message":"` + message + `","suggestion":"Try again.","disposition":{"delivery":"not_delivered","retry":"safe"}}}`
}

func (f *fake) run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if stdin != nil {
		f.stdin = string(stdin)
	}
	cmd := args[0]
	if cmd == "close-app" && f.refuseGraceful && !strings.Contains(strings.Join(args, " "), "--force") {
		return []byte(failed(cmd, "ACTION_FAILED", "Process 42 has 1 open dialog(s)")), nil
	}
	if a, ok := f.answer[cmd]; ok {
		return []byte(a), nil
	}
	switch cmd {
	case "status":
		platform := f.platform
		if platform == "" {
			platform = "linux"
		}
		return []byte(ok(cmd, `{"permissions":{"accessibility":{"state":"granted"},"screen_recording":{"state":"granted"}},"platform":"`+platform+`"}`)), nil
	case "list-apps":
		if f.running {
			return []byte(ok(cmd, `{"apps":[{"name":"Notes","pid":1}]}`)), nil
		}
		return []byte(ok(cmd, `{"apps":[]}`)), nil
	case "launch":
		return []byte(ok(cmd, `{"app":"Notes","pid":42,"window":{"id":"w-1","title":"All Notes"}}`)), nil
	case "list-surfaces":
		if f.sheet {
			return []byte(ok(cmd, `{"surfaces":[{"id":"w-1","type":"window"},{"id":"w-1","type":"sheet"}]}`)), nil
		}
		return []byte(ok(cmd, `{"surfaces":[{"id":"w-1","type":"window"}]}`)), nil
	case "snapshot":
		f.snapshots++
		id := "s" + string(rune('a'+f.snapshots))
		if strings.Contains(strings.Join(args, " "), "--surface menubar") {
			return []byte(ok(cmd, `{"app":"Notes","snapshot_id":"`+id+`","complete":true,"tree":{"role":"menubar","children":[
				{"role":"menubaritem","name":"File","ref_id":"@`+id+`:e1","children":[{"role":"menu","children":[
					{"role":"menuitem","name":"New","ref_id":"@`+id+`:e2"},{"role":"menuitem","name":"Save","ref_id":"@`+id+`:e3"}]}]},
				{"role":"menubaritem","name":"Edit","ref_id":"@`+id+`:e4"}]}}`)), nil
		}
		if strings.Contains(strings.Join(args, " "), "--surface sheet") {
			return []byte(ok(cmd, `{"app":"Notes","snapshot_id":"`+id+`","complete":true,"tree":{"role":"sheet","name":"Delete note?","children":[
				{"role":"button","name":"Delete","ref_id":"@`+id+`:e1"},{"role":"button","name":"Cancel","ref_id":"@`+id+`:e2"}]}}`)), nil
		}
		return []byte(ok(cmd, `{"app":"Notes","snapshot_id":"`+id+`","complete":true,"window":{"id":"w-1","title":"All Notes"},"tree":{"role":"window","name":"All Notes","children":[
			{"role":"group","children":[
				{"role":"button","name":"New note","ref_id":"@`+id+`:e1","available_actions":["Click"]},
				{"role":"button","description":"Delete the selected note","ref_id":"@`+id+`:e2","states":["disabled"]},
				{"role":"textfield","name":"Title","value":"Groceries","ref_id":"@`+id+`:e3"},
				{"role":"statictext","name":"3 notes"},
				{"role":"group","name":"Sidebar","children":[
					{"role":"button","name":"Delete","ref_id":"@`+id+`:e4"},
					{"role":"button","name":"Delete","ref_id":"@`+id+`:e5"}]},
				{"role":"slider","name":"Zoom","value":75,"ref_id":"@`+id+`:e6"}]}]}}`)), nil
	case "find":
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--name Missing") {
			return []byte(ok(cmd, `{"match":null,"roles_present":["button","textfield"]}`)), nil
		}
		if strings.Contains(joined, "--name Edit") {
			// The Edit menu is in the menu bar, not in the window.
			if strings.Contains(joined, "--surface menubar") {
				return []byte(ok(cmd, `{"match":{"ref_id":"@sbar:e4","role":"menubaritem","name":"Edit"},"snapshot_id":"sbar"}`)), nil
			}
			return []byte(ok(cmd, `{"match":null,"roles_present":["button"]}`)), nil
		}
		if strings.Contains(joined, "--name Cancel") && !strings.Contains(joined, "--surface sheet") {
			return []byte(ok(cmd, `{"match":null}`)), nil
		}
		return []byte(ok(cmd, `{"match":{"ref_id":"@sfound:e9","role":"button","name":"New note"},"snapshot_id":"sfound"}`)), nil
	case "screenshot":
		return []byte(ok(cmd, `{"format":"png","data":"`+base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))+`"}`)), nil
	}
	return []byte(ok(cmd, `{"action":"`+cmd+`","disposition":{"delivery":"delivered_verified","retry":"unsafe"}}`)), nil
}

func (f *fake) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fake) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func start(t *testing.T, f *fake, cfg Config) (target.Page, target.Session, string) {
	t.Helper()
	if cfg.App == "" {
		cfg.App = "Notes"
	}
	cfg.run = f.run
	dir := t.TempDir()
	sess, err := New("notes", cfg).Start(context.Background(), &target.RunContext{TestName: t.Name(), Dir: dir, Render: func(s string) (string, error) {
		return strings.ReplaceAll(s, "{{secrets.TEST_PASSWORD}}", "hunter2"), nil
	}})
	require.NoError(t, err)
	bs, ok := sess.(target.BrowserSession)
	require.True(t, ok)
	return bs.Page(), sess, dir
}

func TestStartLaunchesAndCloseQuits(t *testing.T) {
	f := &fake{}
	_, sess, dir := start(t, f, Config{Args: []string{"--new", "{{secrets.TEST_PASSWORD}}"}})
	assert.Equal(t, []string{
		"status",
		"list-apps --app Notes",
		"launch Notes --activate --arg=--new --arg=hunter2 --trace " + filepath.Join(dir, TraceFile),
	}, f.calls)

	require.NoError(t, os.WriteFile(filepath.Join(dir, TraceFile), []byte("{}\n"), 0o644))
	art, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(f.last(), "close-app Notes"), "an app the session launched is closed")
	assert.Equal(t, TraceFile, art.Files[TraceFile])
	assert.Equal(t, "opened", art.Events[0].Kind)
	assert.Equal(t, "closed", art.Events[len(art.Events)-1].Kind)

	// Closing twice quits once.
	n := len(f.calls)
	_, err = sess.Close(context.Background())
	require.NoError(t, err)
	assert.Len(t, f.calls, n)
}

func TestAnAppThatWillNotQuitIsTerminated(t *testing.T) {
	f := &fake{}
	_, sess, _ := start(t, f, Config{})
	f.answer = map[string]string{"close-app": failed("close-app", "ACTION_FAILED", "Process 42 has 1 open dialog(s)")}
	art, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(f.last(), "close-app Notes --force"))
	assert.Equal(t, "close_failed", art.Events[len(art.Events)-1].Kind, "the stand-in refuses the forced close too")

	g := &fake{}
	_, sess, _ = start(t, g, Config{})
	g.mu.Lock()
	g.refuseGraceful = true
	g.mu.Unlock()
	art, err = sess.Close(context.Background())
	require.NoError(t, err)
	last := art.Events[len(art.Events)-1]
	assert.Equal(t, "closed", last.Kind)
	assert.Contains(t, last.Detail, "terminated: Process 42 has 1 open dialog(s)")
}

func TestClosedMenusAreLeftOut(t *testing.T) {
	n := 0
	b := newTreeBuilder(func(string) int { n++; return n })
	b.add("window", node{Role: "window", Name: "Editor", Children: []node{
		{Role: "menu", Name: "File", RefID: "@s:e1", Children: []node{
			{Role: "menuitem", Name: "Save", RefID: "@s:e2", States: []string{"offscreen"}},
			{Role: "menu", Name: "Recent", RefID: "@s:e3", States: []string{"offscreen"}, Children: []node{{Role: "menuitem", Name: "a.txt", RefID: "@s:e4"}}},
		}},
		{Role: "button", Name: "Save", RefID: "@s:e5", States: []string{"offscreen"}},
	}}, 0)
	assert.Equal(t, []string{
		`    window "Editor"`,
		`[1]  menu "File"`,
		`[2]  button "Save" (offscreen)`,
	}, b.lines)
	assert.Equal(t, []string{"Editor", "File", "Save"}, b.text)
}

// TestWhatSaysNothingIsLeftOut: hidden elements, the separators of a menu and
// values that belong to nothing a person could name are not in the tree.
func TestWhatSaysNothingIsLeftOut(t *testing.T) {
	n := 0
	b := newTreeBuilder(func(string) int { n++; return n })
	raw := func(s string) []byte { return []byte(s) }
	b.add("window", node{Role: "window", Name: "Untitled", Children: []node{
		{Role: "textfield", RefID: "@s:e1", Value: raw(`"second draft"`), States: []string{"focused"}},
		{Role: "scrollbar", RefID: "@s:e2", Value: raw(`0.18`), States: []string{"disabled", "hidden"}, Children: []node{{Role: "button", Name: "Page up", RefID: "@s:e3"}}},
		{Role: "ruler", RefID: "@s:e4", Children: []node{{Role: "rulermarker", Value: raw(`20.3`)}, {Role: "rulermarker", Value: raw(`0`)}}},
		{Role: "splitter", Value: raw(`256`), States: []string{"disabled"}},
		{Role: "statictext", Value: raw(`"Edited"`)},
		{Role: "menuitem", RefID: "@s:e5", States: []string{"disabled"}},
		{Role: "menuitem", Name: "Redo", RefID: "@s:e6", States: []string{"disabled"}},
		{Role: "button", RefID: "@s:e7"},
	}}, 0)
	assert.Equal(t, []string{
		`    window "Untitled"`,
		`[1]  textfield value="second draft" (focused)`,
		`[2]  ruler`,
		`     text "Edited"`,
		`[3]  menuitem "Redo" (disabled)`,
		`[4]  button`,
	}, b.lines)
	assert.Equal(t, []string{"Untitled", "second draft", "Edited", "Redo"}, b.text, "the window's text is what a person reads in it")
}

func TestAnOpenMenuTheWindowListsIsNotWrittenTwice(t *testing.T) {
	n := 0
	b := newTreeBuilder(func(string) int { n++; return n })
	window := snapshot{Tree: node{Role: "window", Name: "Editor", Children: []node{
		{Role: "menu", Name: "Edit", RefID: "@a:e1", Children: []node{{Role: "menuitem", Name: "Undo", RefID: "@a:e2"}}},
	}}}
	same := snapshot{Tree: node{Role: "menu", Name: "Edit", RefID: "@b:e1", Children: []node{{Role: "menuitem", Name: "Undo", RefID: "@b:e2"}}}}
	other := snapshot{Tree: node{Role: "sheet", Name: "Save?", Children: []node{{Role: "button", Name: "Save", RefID: "@c:e1"}}}}
	b.build([]snapshot{window, same, other}, []string{"window", "menu", "sheet"})
	assert.Equal(t, []string{
		`    window "Editor"`,
		`[1]  menu "Edit"`,
		`[2]   menuitem "Undo"`,
		`    sheet "Save?"`,
		`[3]  button "Save"`,
	}, b.lines)
}

// TestTheMenuBarOutsideTheWindow: on macOS the menu bar belongs to the app,
// not to its window. Its titles close the observation (what a menu holds is
// listed once it is open), and a step that names one finds it there.
func TestTheMenuBarOutsideTheWindow(t *testing.T) {
	f := &fake{platform: "macos"}
	page, _, _ := start(t, f, Config{})
	ctx := context.Background()
	obs, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(obs.Tree, strings.Join([]string{
		`[6]   slider "Zoom" value="75"`,
		`[7]  menubaritem "File"`,
		`[8]  menubaritem "Edit"`,
	}, "\n")), obs.Tree)
	assert.NotContains(t, obs.Tree, `menuitem "Save"`)

	did, err := page.Do(ctx, map[string]any{"click": map[string]any{"name": "Edit"}})
	require.NoError(t, err)
	assert.Equal(t, `clicked "Edit"`, did)
	assert.Regexp(t, `^click @sbar:e4 `, f.last())
	assert.True(t, f.called("find --app Notes --exact --first --surface menubar --name Edit"))

	// Elsewhere the menu bar is part of the window and is not asked for.
	g := &fake{}
	page, _, _ = start(t, g, Config{})
	_, err = page.Observe(ctx, false)
	require.NoError(t, err)
	assert.False(t, g.called("snapshot --app Notes --surface menubar"))
	_, err = page.Do(ctx, map[string]any{"click": "Edit"})
	assert.ErrorContains(t, err, `no element is "Edit"`)
	assert.False(t, g.called("find --app Notes --exact --first --surface menubar"))
}

// TestAStepFindsWhatIsOnTopFirst: with a sheet open, a step that names one
// of its buttons is looked for in the sheet before the window.
func TestAStepFindsWhatIsOnTopFirst(t *testing.T) {
	f := &fake{sheet: true}
	page, _, _ := start(t, f, Config{})
	did, err := page.Do(context.Background(), map[string]any{"click": map[string]any{"role": "button", "name": "Cancel"}})
	require.NoError(t, err)
	assert.Equal(t, `clicked button "Cancel"`, did)
	var finds []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "find ") {
			finds = append(finds, c)
		}
	}
	require.Len(t, finds, 1, "the sheet answers, so the window is not searched")
	assert.Contains(t, finds[0], "--surface sheet")
}

func TestAnAppThatWasRunningIsLeftOpen(t *testing.T) {
	f := &fake{running: true}
	_, sess, _ := start(t, f, Config{})
	_, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.False(t, f.called("close-app"))
}

func TestStartErrors(t *testing.T) {
	ctx := context.Background()
	_, err := New("d", Config{App: "Notes", Engine: "xdotool"}).Start(ctx, nil)
	assert.ErrorContains(t, err, `engine "xdotool" is not one of agent-desktop`)

	_, err = New("d", Config{App: " ", run: (&fake{}).run}).Start(ctx, nil)
	assert.ErrorContains(t, err, "no app to launch")

	denied := &fake{answer: map[string]string{"status": ok("status", `{"permissions":{"accessibility":{"state":"denied","suggestion":"Run agent-desktop permissions --guide."}}}`)}}
	_, err = New("d", Config{App: "Notes", run: denied.run}).Start(ctx, nil)
	assert.ErrorContains(t, err, "the Accessibility permission is denied. Run agent-desktop permissions --guide.")
	assert.False(t, denied.called("launch"))

	missing := &fake{answer: map[string]string{"launch": failed("launch", "APP_NOT_FOUND", "No application named Nope")}}
	_, err = New("d", Config{App: "Nope", run: missing.run}).Start(ctx, nil)
	assert.ErrorContains(t, err, "launch Nope: No application named Nope. Try again.")

	old := &fake{answer: map[string]string{"status": `{"version":"3.0","ok":true,"command":"status","data":{}}`}}
	_, err = New("d", Config{App: "Notes", run: old.run}).Start(ctx, nil)
	assert.ErrorContains(t, err, `output version "3.0"; this tamesu reads version 2.x`)

	broken := &fake{answer: map[string]string{"status": "not json"}}
	_, err = New("d", Config{App: "Notes", run: broken.run}).Start(ctx, nil)
	assert.ErrorContains(t, err, "unreadable output")
}

func TestObserve(t *testing.T) {
	f := &fake{}
	page, _, _ := start(t, f, Config{})
	ctx := context.Background()

	obs, err := page.Observe(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "desktop://Notes", obs.URL)
	assert.Equal(t, "All Notes", obs.Title)
	assert.Equal(t, strings.Join([]string{
		`    window "All Notes"`,
		`[1]   button "New note"`,
		`[2]   button "Delete the selected note" (disabled)`,
		`[3]   textfield "Title" value="Groceries"`,
		`      text "3 notes"`,
		`      group "Sidebar"`,
		`[4]    button "Delete"`,
		`[5]    button "Delete"`,
		`[6]   slider "Zoom" value="75"`,
	}, "\n"), obs.Tree)
	assert.Equal(t, "\x89PNG fake", string(obs.Screenshot))
	assert.True(t, f.called("snapshot --app Notes --compact --max-depth 50"))

	// The same elements keep their numbers, though agent-desktop's own refs
	// are new with every snapshot.
	again, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, obs.Tree, again.Tree)

	url, err := page.URL(ctx)
	require.NoError(t, err)
	assert.Equal(t, "desktop://Notes", url)
}

func TestObserveAddsWhatIsOnTop(t *testing.T) {
	f := &fake{sheet: true}
	page, _, _ := start(t, f, Config{})
	obs, err := page.Observe(context.Background(), false)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(obs.Tree, strings.Join([]string{
		`    sheet "Delete note?"`,
		`[7]  button "Delete"`,
		`[8]  button "Cancel"`,
	}, "\n")), obs.Tree)

	// The sheet's Delete is not the sidebar's.
	require.NoError(t, page.Click(context.Background(), 7))
	assert.Regexp(t, `^click @s\w:e1 --trace `, f.last())

	text, err := page.Text(context.Background())
	require.NoError(t, err)
	assert.Contains(t, text, "Delete note?")
	assert.Contains(t, text, "Groceries")
	assert.Contains(t, text, "3 notes")
}

func TestActions(t *testing.T) {
	f := &fake{}
	page, sess, _ := start(t, f, Config{})
	ctx := context.Background()

	assert.ErrorContains(t, page.Click(ctx, 1), "unknown ref 1; observe the window first")
	_, err := page.Observe(ctx, false)
	require.NoError(t, err)

	require.NoError(t, page.Click(ctx, 1))
	assert.Regexp(t, `^click @s\w:e1 --trace `, f.last())

	require.NoError(t, page.Type(ctx, 3, "{{secrets.TEST_PASSWORD}}"))
	assert.Regexp(t, `^type @s\w:e3 --stdin --trace `, f.last())
	assert.Equal(t, "hunter2", f.stdin, "the secret is rendered when typed and travels on standard input")
	assert.True(t, f.called("clear @"), "the field is emptied first")
	for _, c := range f.calls {
		assert.NotContains(t, c, "hunter2", "what is typed is never an argument")
	}

	require.NoError(t, page.Select(ctx, 6, "100"))
	assert.Regexp(t, `^select @s\w:e6 100 --trace `, f.last())

	require.NoError(t, page.Press(ctx, "Enter"))
	assert.Regexp(t, `^press return --window-id w-1 --trace `, f.last())
	require.NoError(t, page.Press(ctx, "Cmd + Shift+ArrowDown"))
	assert.Regexp(t, `^press cmd\+shift\+down `, f.last())
	assert.ErrorContains(t, page.Press(ctx, " "), "no key")

	assert.ErrorContains(t, page.Click(ctx, 99), "unknown ref 99; observe the window first (known refs: [1 2 3 4 5 6])")
	assert.ErrorIs(t, page.Scroll(ctx, "down"), ErrUnsupported)
	assert.ErrorIs(t, page.Navigate(ctx, "https://example.org"), ErrUnsupported)
	_, err = page.Eval(ctx, "1")
	assert.ErrorIs(t, err, ErrUnsupported)
	require.NoError(t, page.Wait(ctx, time.Millisecond))

	art, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5.0, art.Metrics["desktop_actions"])
	assert.Equal(t, 1.0, art.Metrics["observations"])
}

func TestAStaleRefAsksForANewObservation(t *testing.T) {
	f := &fake{}
	page, _, _ := start(t, f, Config{})
	ctx := context.Background()
	_, err := page.Observe(ctx, false)
	require.NoError(t, err)

	f.answer = map[string]string{"click": failed("click", "STALE_REF", "The element is gone")}
	err = page.Click(ctx, 1)
	assert.EqualError(t, err, `click [1] button "New note": the element is not where it was (STALE_REF); observe the window again and use its new refs`)
	var cliErr *Error
	require.True(t, errors.As(err, &cliErr))
	assert.Equal(t, "STALE_REF", cliErr.Code)

	// A field that has no clear action is typed into as it is; a stale one is not.
	f.answer = map[string]string{"clear": failed("clear", "ACTION_NOT_SUPPORTED", "No clear action")}
	require.NoError(t, page.Type(ctx, 3, "x"))
	f.answer = map[string]string{"clear": failed("clear", "STALE_REF", "gone")}
	assert.ErrorContains(t, page.Type(ctx, 3, "x"), "observe the window again")
}

func TestScreenshotsStopAfterARefusal(t *testing.T) {
	f := &fake{answer: map[string]string{"screenshot": failed("screenshot", "PERM_DENIED", "Screen Recording permission not granted")}}
	page, _, _ := start(t, f, Config{})
	ctx := context.Background()
	_, err := page.Screenshot(ctx)
	assert.ErrorContains(t, err, "Screen Recording permission not granted")
	n := len(f.calls)
	_, err = page.Screenshot(ctx)
	assert.ErrorContains(t, err, "Screen Recording permission not granted")
	assert.Len(t, f.calls, n, "the system is not asked again")

	// An observation still works without one.
	obs, err := page.Observe(ctx, true)
	require.NoError(t, err)
	assert.Empty(t, obs.Screenshot)
}

func TestDeterministicSteps(t *testing.T) {
	f := &fake{}
	page, sess, dir := start(t, f, Config{StepTimeout: 400 * time.Millisecond})
	ctx := context.Background()

	did, err := page.Do(ctx, map[string]any{"click": "New note"})
	require.NoError(t, err)
	assert.Equal(t, `clicked "New note"`, did)
	assert.True(t, f.called("find --app Notes --exact --first --name New note"))
	assert.Regexp(t, `^click @sfound:e9 `, f.last())

	did, err = page.Do(ctx, map[string]any{"click": map[string]any{"role": "button", "name": "New note"}})
	require.NoError(t, err)
	assert.Equal(t, `clicked button "New note"`, did)
	assert.True(t, f.called("find --app Notes --exact --first --role button --name New note"))

	_, err = page.Do(ctx, map[string]any{"click": "Missing"})
	assert.EqualError(t, err, `click: no element is "Missing" (roles in the window: button, textfield)`)
	_, err = page.Do(ctx, map[string]any{"click": map[string]any{}})
	assert.ErrorContains(t, err, "wants a ref, a name or {role, name}")

	_, err = page.Observe(ctx, false)
	require.NoError(t, err)
	did, err = page.Do(ctx, map[string]any{"click": 1})
	require.NoError(t, err)
	assert.Equal(t, `clicked [1] button "New note"`, did)

	did, err = page.Do(ctx, map[string]any{"fill": map[string]any{"name": "Title", "text": "{{secrets.TEST_PASSWORD}}"}})
	require.NoError(t, err)
	assert.Equal(t, `filled "Title"`, did)
	assert.Equal(t, "hunter2", f.stdin)
	did, err = page.Do(ctx, map[string]any{"fill": map[string]any{"ref": 3, "text": "Milk"}})
	require.NoError(t, err)
	assert.Equal(t, `filled [3] textfield "Title"`, did)
	_, err = page.Do(ctx, map[string]any{"fill": "Title"})
	assert.ErrorContains(t, err, "fill wants {ref or name, text}")

	did, err = page.Do(ctx, map[string]any{"press": "cmd+s"})
	require.NoError(t, err)
	assert.Equal(t, "pressed cmd+s", did)

	did, err = page.Do(ctx, map[string]any{"wait": 1})
	require.NoError(t, err)
	assert.Equal(t, "waited 1 ms", did)
	_, err = page.Do(ctx, map[string]any{"wait": map[string]any{"text": "3 notes"}})
	require.NoError(t, err)
	_, err = page.Do(ctx, map[string]any{"wait": map[string]any{"text": "never"}})
	assert.ErrorContains(t, err, `wait for text "never": not there after 400ms`)

	did, err = page.Do(ctx, map[string]any{"screenshot": "After save"})
	require.NoError(t, err)
	assert.Equal(t, "screenshot "+filepath.Join("screenshots", "after-save.png"), did)
	png, err := os.ReadFile(filepath.Join(dir, "screenshots", "after-save.png"))
	require.NoError(t, err)
	assert.Equal(t, "\x89PNG fake", string(png))

	did, err = page.Do(ctx, map[string]any{"assert_text": "Groceries"})
	require.NoError(t, err)
	assert.Equal(t, `text contains "Groceries"`, did)
	_, err = page.Do(ctx, map[string]any{"assert_text": map[string]any{"matches": `\d+ notes`}})
	require.NoError(t, err)
	_, err = page.Do(ctx, map[string]any{"assert_text": "Bananas"})
	var se *target.StepError
	require.True(t, errors.As(err, &se), "a failed check fails the step, not the run")
	assert.Contains(t, se.Reason, `text does not contain "Bananas"`)

	for _, name := range []string{"navigate", "eval_js", "assert_url", "mock", "a11y", "visual"} {
		_, err = page.Do(ctx, map[string]any{name: "x"})
		assert.ErrorContains(t, err, `unknown action "`+name+`" on a desktop target`)
	}
	_, err = page.Do(ctx, map[string]any{"click": 1, "press": "a"})
	assert.ErrorContains(t, err, "an action is exactly one of")

	art, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Contains(t, art.Files, filepath.Join("screenshots", "after-save.png"))
}

func TestCombo(t *testing.T) {
	for in, want := range map[string]string{
		"Enter": "return", "Escape": "escape", "Tab": "tab", "ArrowUp": "up", "cmd+s": "cmd+s",
		"Control+Shift+P": "ctrl+shift+p", "Meta+Q": "cmd+q", "F5": "f5", "a": "a",
	} {
		assert.Equal(t, want, combo(in), in)
	}
}
