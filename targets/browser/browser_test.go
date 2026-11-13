package browser

import (
	"context"
	"encoding/json"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

const shopPage = `<!doctype html><html><head><title>Mochi Shoes</title></head><body>
<nav><a href="/">Home</a> <a href="/cart">Cart (<span id="count">0</span>)</a></nav>
<main>
<h1>Welcome to Mochi Shoes</h1>
<form id="login" onsubmit="event.preventDefault(); document.getElementById('who').textContent = 'Hello ' + document.getElementById('user').value;">
  <label for="user">Username</label><input id="user" name="user">
  <label for="pass">Password</label><input id="pass" name="pass" type="password">
  <button type="submit">Log in</button>
</form>
<p id="who"></p>
<label for="size">Size</label>
<select id="size" onchange="document.getElementById('picked').textContent = this.value"><option value="">pick</option><option value="42">42</option><option value="43">43</option></select>
<span id="picked"></span>
<button id="add" onclick="document.getElementById('count').textContent = '1'; console.error('added to cart'); window.scrollBy(0, 0)">Add to cart</button>
<a href="/external" id="ext">external</a>
<div style="height: 3000px"></div>
<p id="bottom">the end</p>
</main></body></html>`

func server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if c, err := r.Cookie("session"); err == nil {
			_, _ = w.Write([]byte(strings.Replace(shopPage, "<p id=\"who\"></p>", "<p id=\"who\">cookie "+c.Value+"</p>", 1)))
			return
		}
		_, _ = w.Write([]byte(shopPage))
	})
	mux.HandleFunc("/cart", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Cart</title></head><body><h1>Your cart</h1><p>1 item</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func start(t *testing.T, srv *httptest.Server, cfg Config) (target.Page, target.Session, string) {
	t.Helper()
	if _, err := FindChromium(context.Background(), "", false); err != nil {
		t.Skip("no Chromium on this machine:", err)
	}
	if cfg.URL == "" {
		cfg.URL = srv.URL + "/"
	}
	dir := t.TempDir()
	tgt := New("web", cfg)
	sess, err := tgt.Start(context.Background(), &target.RunContext{TestName: t.Name(), Dir: dir, Render: func(s string) (string, error) {
		return strings.ReplaceAll(s, "{{secrets.TEST_PASSWORD}}", "hunter2"), nil
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sess.Close(context.Background()) })
	bs, ok := sess.(target.BrowserSession)
	require.True(t, ok)
	return bs.Page(), sess, dir
}

func refOf(t *testing.T, obs target.Observation, contains string) int {
	t.Helper()
	for _, line := range strings.Split(obs.Tree, "\n") {
		if strings.Contains(line, contains) && strings.HasPrefix(line, "[") {
			var ref int
			_, err := fmtSscanf(line, &ref)
			require.NoError(t, err)
			return ref
		}
	}
	t.Fatalf("no ref line containing %q in:\n%s", contains, obs.Tree)
	return 0
}

func fmtSscanf(line string, ref *int) (int, error) {
	end := strings.Index(line, "]")
	n, err := parseInt(line[1:end])
	*ref = n
	return 1, err
}

func TestObserveAndAct(t *testing.T) {
	srv := server(t)
	page, sess, dir := start(t, srv, Config{Viewport: "1000x700"})
	ctx := context.Background()

	obs, err := page.Observe(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "Mochi Shoes", obs.Title)
	assert.Equal(t, srv.URL+"/", obs.URL)
	assert.Contains(t, obs.Tree, `heading "Welcome to Mochi Shoes" (h1)`)
	assert.Regexp(t, `\[\d+\] +textbox "Username"`, obs.Tree)
	assert.Regexp(t, `\[\d+\] +button "Add to cart"`, obs.Tree)
	assert.NotEmpty(t, obs.Screenshot, "a PNG came with the observation")
	assert.True(t, strings.HasPrefix(string(obs.Screenshot), "\x89PNG"))

	user := refOf(t, obs, `textbox "Username"`)
	pass := refOf(t, obs, `textbox "Password"`)
	login := refOf(t, obs, `button "Log in"`)
	require.NoError(t, page.Type(ctx, user, "marta"))
	require.NoError(t, page.Type(ctx, pass, "{{secrets.TEST_PASSWORD}}"))
	typed, err := page.Eval(ctx, "document.getElementById('pass').value")
	require.NoError(t, err)
	assert.Equal(t, "hunter2", typed, "the secret is rendered only when typed")
	require.NoError(t, page.Click(ctx, login))
	text, err := page.Text(ctx)
	require.NoError(t, err)
	assert.Contains(t, text, "Hello marta")

	again, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, user, refOf(t, again, `textbox "Username"`), "refs are stable across observations")
	assert.Contains(t, again.Tree, `value="marta"`)

	size := refOf(t, again, `combobox "Size"`)
	require.NoError(t, page.Select(ctx, size, "43"))
	picked, err := page.Eval(ctx, "document.getElementById('picked').textContent")
	require.NoError(t, err)
	assert.Equal(t, "43", picked)
	assert.ErrorContains(t, page.Select(ctx, size, "99"), "no such option")

	add := refOf(t, again, `button "Add to cart"`)
	require.NoError(t, page.Click(ctx, add))
	time.Sleep(100 * time.Millisecond)
	obs3, err := page.Observe(ctx, false)
	require.NoError(t, err)
	require.NotEmpty(t, obs3.Console, "console errors are observed")
	assert.Contains(t, obs3.Console[0], "added to cart")

	require.NoError(t, page.Scroll(ctx, "bottom"))
	y, err := page.Eval(ctx, "window.scrollY")
	require.NoError(t, err)
	assert.Greater(t, y.(float64), 1000.0)
	require.NoError(t, page.Scroll(ctx, "top"))
	assert.ErrorContains(t, page.Scroll(ctx, "sideways"), "direction must be")

	require.NoError(t, page.Press(ctx, "Tab"))
	assert.ErrorContains(t, page.Press(ctx, "Hyperspace"), "unknown key")
	assert.ErrorContains(t, page.Click(ctx, 9999), "unknown ref 9999")

	// Navigation inside the host is fine; another host is refused.
	require.NoError(t, page.Navigate(ctx, srv.URL+"/cart"))
	loc, err := page.URL(ctx)
	require.NoError(t, err)
	assert.Equal(t, srv.URL+"/cart", loc)
	err = page.Navigate(ctx, "https://example.com/")
	assert.ErrorContains(t, err, "outside allowed_domains")

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, HARFile, arts.Files[HARFile])
	assert.Equal(t, ConsoleFile, arts.Files[ConsoleFile])
	harBytes, err := os.ReadFile(filepath.Join(dir, HARFile))
	require.NoError(t, err)
	var har struct {
		Log struct {
			Version string `json:"version"`
			Entries []struct {
				Request  struct{ URL string }
				Response struct{ Status int }
			} `json:"entries"`
		} `json:"log"`
	}
	require.NoError(t, json.Unmarshal(harBytes, &har))
	assert.Equal(t, "1.2", har.Log.Version)
	require.NotEmpty(t, har.Log.Entries)
	assert.Equal(t, srv.URL+"/", har.Log.Entries[0].Request.URL)
	assert.Equal(t, 200, har.Log.Entries[0].Response.Status)
	assert.GreaterOrEqual(t, arts.Metrics["requests"], 2.0)
	assert.Equal(t, 1.0, arts.Metrics["console_errors"])
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "navigation_refused")
	assert.Contains(t, kinds, "type")
	assert.Contains(t, kinds, "click")
}

func TestDeterministicActions(t *testing.T) {
	srv := server(t)
	// A short step timeout: the click on a missing element waits it out.
	page, _, dir := start(t, srv, Config{StepTimeout: 10 * time.Second})
	ctx := context.Background()
	steps := []struct {
		action map[string]any
		want   string
	}{
		{map[string]any{"fill": map[string]any{"selector": "#user", "text": "jordi"}}, "filled #user"},
		{map[string]any{"fill": map[string]any{"selector": "#pass", "text": "{{secrets.TEST_PASSWORD}}"}}, "filled #pass"},
		{map[string]any{"click": "#login button"}, "clicked #login button"},
		{map[string]any{"wait": map[string]any{"text": "Hello jordi"}}, `waited for text "Hello jordi"`},
		{map[string]any{"assert_text": "Hello jordi"}, `text contains "Hello jordi"`},
		{map[string]any{"assert_text": map[string]any{"selector": "#who", "contains": "jordi"}}, `text matches contains "jordi"`},
		{map[string]any{"assert_url": map[string]any{"matches": "^http://127"}}, "url is "},
		{map[string]any{"eval_js": "1 + 1"}, "eval_js → 2"},
		{map[string]any{"wait": 50}, "waited 50 ms"},
		{map[string]any{"press": "Escape"}, "pressed Escape"},
		{map[string]any{"screenshot": "after login"}, "screenshot screenshots/after-login.png"},
		{map[string]any{"navigate": srv.URL + "/cart"}, "navigated to " + srv.URL + "/cart"},
		{map[string]any{"assert_url": srv.URL + "/cart"}, "url is "},
	}
	for _, st := range steps {
		got, err := page.Do(ctx, st.action)
		require.NoError(t, err, "%v", st.action)
		assert.Contains(t, got, st.want)
	}
	_, err := os.Stat(filepath.Join(dir, "screenshots", "after-login.png"))
	assert.NoError(t, err)

	// Failing checks are StepErrors; malformed actions are errors.
	_, err = page.Do(ctx, map[string]any{"assert_text": "nothing like this"})
	var se *target.StepError
	require.ErrorAs(t, err, &se)
	assert.Contains(t, se.Reason, `text does not contain "nothing like this"`)
	_, err = page.Do(ctx, map[string]any{"assert_url": map[string]any{"equals": "http://nope/"}})
	require.ErrorAs(t, err, &se)
	_, err = page.Do(ctx, map[string]any{"click": "#does-not-exist"})
	assert.Error(t, err)
	assert.NotErrorAs(t, err, &se, "a missing element is an error, not a failed check")
	_, err = page.Do(ctx, map[string]any{"click": "a", "fill": "b"})
	assert.ErrorContains(t, err, "exactly one of")
	_, err = page.Do(ctx, map[string]any{"teleport": "x"})
	assert.ErrorContains(t, err, `unknown action "teleport"`)
}

func TestStorageStateAndRefHelpers(t *testing.T) {
	srv := server(t)
	u, _ := url.Parse(srv.URL)
	state := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(state, []byte(`{"cookies":[{"name":"session","value":"abc","domain":"`+u.Hostname()+`","path":"/"}],"origins":[{"origin":"`+srv.URL+`","localStorage":[{"name":"theme","value":"dark"}]}]}`), 0o644))
	page, _, _ := start(t, srv, Config{StorageState: state})
	text, err := page.Text(context.Background())
	require.NoError(t, err)
	assert.Contains(t, text, "cookie abc", "the cookie was sent with the first request")
	theme, err := page.Eval(context.Background(), "localStorage.getItem('theme')")
	require.NoError(t, err)
	assert.Equal(t, "dark", theme)

	assert.True(t, hostAllowed("shop.example.com", []string{"example.com"}))
	assert.True(t, hostAllowed("example.com", []string{"*.example.com"}))
	assert.False(t, hostAllowed("evil-example.com", []string{"example.com"}))
	w, h, err := parseViewport("")
	require.NoError(t, err)
	assert.Equal(t, [2]int{1280, 800}, [2]int{w, h})
	_, _, err = parseViewport("wide")
	assert.Error(t, err)
	assert.Equal(t, "after-login", slug("After Login!"))
}

func TestFindChromiumErrors(t *testing.T) {
	_, err := FindChromium(context.Background(), filepath.Join(t.TempDir(), "nope"), false)
	assert.ErrorContains(t, err, "chromium binary")
	t.Setenv(EnvChromium, filepath.Join(t.TempDir(), "missing"))
	_, err = FindChromium(context.Background(), "", false)
	assert.ErrorContains(t, err, EnvChromium)
}

func parseInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, &target.StepError{Reason: "not a number: " + s}
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func TestRecordingGIF(t *testing.T) {
	srv := server(t)
	page, sess, dir := start(t, srv, Config{Viewport: "1000x700", Recording: RecordingGIF})
	ctx := context.Background()
	obs, err := page.Observe(ctx, false)
	require.NoError(t, err)
	require.NoError(t, page.Type(ctx, refOf(t, obs, `textbox "Username"`), "marta"))
	require.NoError(t, page.Navigate(ctx, srv.URL+"/cart"))
	time.Sleep(300 * time.Millisecond) // let Chromium paint and send the frames
	art, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, RecordingGIFFile, art.Files[RecordingGIFFile], "the recording is an artifact")
	assert.GreaterOrEqual(t, art.Metrics["recording_frames"], 1.0)
	f, err := os.Open(filepath.Join(dir, RecordingGIFFile))
	require.NoError(t, err)
	g, err := gif.DecodeAll(f)
	require.NoError(t, f.Close())
	require.NoError(t, err)
	assert.NotEmpty(t, g.Image, "at least one frame")
	assert.LessOrEqual(t, g.Image[0].Bounds().Dx(), recordingMaxWidth, "frames are scaled to the recording width")
	var sawRecording bool
	for _, e := range art.Events {
		sawRecording = sawRecording || (e.Kind == "recording" && e.Detail == RecordingGIFFile)
	}
	assert.True(t, sawRecording, "the recording is on the timeline")
}

func TestRecordingWebM(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg on this machine")
	}
	srv := server(t)
	page, sess, dir := start(t, srv, Config{Recording: RecordingAuto})
	ctx := context.Background()
	require.NoError(t, page.Navigate(ctx, srv.URL+"/cart"))
	time.Sleep(300 * time.Millisecond)
	art, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, RecordingWebMFile, art.Files[RecordingWebMFile], "auto picks WebM when ffmpeg is installed")
	info, err := os.Stat(filepath.Join(dir, RecordingWebMFile))
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(100))
}

func TestRecordingOff(t *testing.T) {
	srv := server(t)
	_, sess, dir := start(t, srv, Config{Recording: RecordingOff})
	art, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, art.Files, RecordingGIFFile)
	assert.NotContains(t, art.Files, RecordingWebMFile)
	assert.NotContains(t, art.Metrics, "recording_frames")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "recording")
	}
}

func TestFrameDurations(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	frames := []frame{{at: t0}, {at: t0.Add(10 * time.Millisecond)}, {at: t0.Add(time.Second)}, {at: t0.Add(2 * time.Second)}}
	got := frameDurations(frames, t0.Add(30*time.Second))
	assert.Equal(t, []time.Duration{50 * time.Millisecond, 990 * time.Millisecond, time.Second, 3 * time.Second}, got,
		"too short is stretched, the long tail is clamped")
}
