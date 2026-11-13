package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// widgetsPage has what a page built without semantic markup has: elements
// that answer a click by a handler, a listener or one on an ancestor,
// wrappers around real controls, a label that is the control, things that
// are not shown, and iframes. CROSS is the origin of the cross-site one.
const widgetsPage = `<!doctype html><html><head><title>Widgets</title><style>.card{cursor:pointer}</style></head><body>
<div id="app">
  <div id="menu" style="cursor:pointer" onclick="this.textContent = 'menu opened'">Open menu</div>
  <div id="listener">Listener only</div>
  <div class="card"><span>Delegated</span> <b>card</b></div>
  <div id="wrapper"><button id="buy">Buy</button><span>wrapper text</span></div>
  <label for="dark" style="cursor:pointer">Dark mode</label><input id="dark" type="checkbox" style="display:none">
  <label for="user">Username</label><input id="user">
  <div id="editor" contenteditable="true">Edit me</div>
  <div style="display:none" onclick="1">Not rendered</div>
  <div style="visibility:hidden" onclick="1">Not visible</div>
  <details><summary>More info</summary><p>details body</p></details>
  <span id="plain">Plain text</span>
</div>
<p id="log"></p>
<script>
const log = text => { document.getElementById('log').textContent += text + ';' };
document.getElementById('listener').addEventListener('click', () => log('listener'));
document.getElementById('wrapper').addEventListener('click', () => log('wrapper'));
document.getElementById('app').addEventListener('click', e => { if (e.target.closest('.card')) log('card'); });
</script>
<iframe id="same" srcdoc="<button onclick=&quot;parent.document.getElementById('log').textContent += 'same;'&quot;>Same-origin button</button>"></iframe>
<iframe id="cross" src="CROSS/pay"></iframe>
</body></html>`

const payPage = `<!doctype html><html><body>
<label for="card">Card number</label><input id="card">
<select aria-label="Currency" onchange="document.getElementById('paid').textContent = 'in ' + this.value"><option value="eur">EUR</option><option value="usd">USD</option></select>
<div style="cursor:pointer" onclick="document.getElementById('paid').textContent = 'paid with ' + document.getElementById('card').value">Pay now</div>
<p id="paid"></p>
</body></html>`

// widgets serves widgetsPage on 127.0.0.1 with payPage in an iframe from
// localhost: another site, so Chromium runs it in a process of its own.
func widgets(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var cross string
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(widgetsPage, "CROSS", cross)))
	})
	mux.HandleFunc("/pay", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(payPage))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cross = strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	return srv
}

func hasRef(tree, contains string) bool {
	for _, line := range strings.Split(tree, "\n") {
		if strings.Contains(line, contains) {
			return strings.HasPrefix(line, "[")
		}
	}
	return false
}

// Elements that answer a click without a role that says so get a ref, named
// by their text; a wrapper leaves it to the control inside it.
func TestObserveClickableElements(t *testing.T) {
	page, _, _ := start(t, widgets(t), Config{Recording: RecordingOff})
	ctx := context.Background()
	obs, err := page.Observe(ctx, false)
	require.NoError(t, err)

	for _, line := range []string{`generic "Open menu"`, `generic "Listener only"`, `generic "Delegated card"`, `button "Buy"`, `LabelText "Dark mode"`, `generic "Edit me"`, `DisclosureTriangle "More info"`, `textbox "Username"`} {
		assert.True(t, hasRef(obs.Tree, line), "%s has a ref in:\n%s", line, obs.Tree)
	}
	assert.False(t, hasRef(obs.Tree, `"wrapper text"`), "text is not a control")
	assert.False(t, hasRef(obs.Tree, `"Plain text"`))
	assert.False(t, hasRef(obs.Tree, `text "Username"`), "a label is its field's, which has the ref")
	assert.NotContains(t, obs.Tree, "Not rendered")
	assert.False(t, hasRef(obs.Tree, "Not visible"))
	assert.Equal(t, 1, strings.Count(obs.Tree, `"Buy"`)-strings.Count(obs.Tree, `text "Buy"`), "the wrapper around Buy has no line of its own")

	require.NoError(t, page.Click(ctx, refOf(t, obs, `generic "Open menu"`)))
	require.NoError(t, page.Click(ctx, refOf(t, obs, `generic "Listener only"`)))
	require.NoError(t, page.Click(ctx, refOf(t, obs, `generic "Delegated card"`)))
	require.NoError(t, page.Click(ctx, refOf(t, obs, `LabelText "Dark mode"`)))
	require.NoError(t, page.Type(ctx, refOf(t, obs, `generic "Edit me"`), "edited"))
	log, err := page.Eval(ctx, "document.getElementById('log').textContent")
	require.NoError(t, err)
	assert.Equal(t, "listener;card;", log)
	dark, err := page.Eval(ctx, "document.getElementById('dark').checked")
	require.NoError(t, err)
	assert.Equal(t, true, dark, "the label is the only way to the hidden checkbox")
	edited, err := page.Eval(ctx, "document.getElementById('editor').textContent")
	require.NoError(t, err)
	assert.Equal(t, "edited", edited)

	again, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, refOf(t, obs, `generic "Listener only"`), refOf(t, again, `generic "Listener only"`), "refs are stable across observations")
	assert.Equal(t, refOf(t, obs, `generic "Open menu"`), refOf(t, again, `generic "menu opened"`), "the same element under its new text")
}

// The elements of an iframe are in the observation, under the iframe, and
// act like any other: for one of the page's own process and for a cross-site
// one, which Chromium runs apart.
func TestObserveFrames(t *testing.T) {
	page, _, _ := start(t, widgets(t), Config{Recording: RecordingOff, StepTimeout: 10 * time.Second})
	ctx := context.Background()
	obs, err := page.Observe(ctx, false)
	require.NoError(t, err)

	require.NoError(t, page.Click(ctx, refOf(t, obs, `button "Same-origin button"`)))
	log, err := page.Eval(ctx, "document.getElementById('log').textContent")
	require.NoError(t, err)
	assert.Equal(t, "same;", log)

	card := refOf(t, obs, `textbox "Card number"`)
	pay := refOf(t, obs, `generic "Pay now"`)
	require.NoError(t, page.Type(ctx, card, "{{secrets.TEST_PASSWORD}}"))
	require.NoError(t, page.Click(ctx, pay))
	require.NoError(t, page.Select(ctx, refOf(t, obs, `combobox "Currency"`), "USD"))
	assert.ErrorContains(t, page.Select(ctx, refOf(t, obs, `combobox "Currency"`), "GBP"), "no such option")
	again, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Contains(t, again.Tree, `text "in usd"`)
	assert.Equal(t, card, refOf(t, again, `textbox "Card number"`), "refs are stable across observations")
	assert.Contains(t, again.Tree, `value="hunter2"`, "the secret was rendered where it was typed")

	// A deterministic step takes the same refs.
	require.NoError(t, page.Type(ctx, card, "4242"))
	did, err := page.Do(ctx, map[string]any{"click": pay})
	require.NoError(t, err)
	assert.Contains(t, did, "clicked [")
	paid, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.Contains(t, paid.Tree, `text "paid with 4242"`)

	// Once the iframe is gone, so are its elements.
	_, err = page.Eval(ctx, "document.getElementById('cross').remove()")
	require.NoError(t, err)
	gone, err := page.Observe(ctx, false)
	require.NoError(t, err)
	assert.NotContains(t, gone.Tree, "Card number")
	assert.ErrorContains(t, page.Click(ctx, pay), "the iframe is gone; observe the page again")
}
