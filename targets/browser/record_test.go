package browser

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecord(t *testing.T) {
	if _, err := FindChromium(context.Background(), "", false); err != nil {
		t.Skip("no Chromium on this machine:", err)
	}
	srv := server(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logged []map[string]any
	steps, err := Record(ctx, srv.URL+"/", RecordOptions{Headless: true, Timeout: 30 * time.Second, Log: func(s map[string]any) { logged = append(logged, s) }, OnReady: func(bctx context.Context) {
		// A person types a username, a password, picks a size, presses Enter
		// in the password field and clicks "Add to cart".
		err := chromedp.Run(bctx, chromedp.Evaluate(`(() => {
			const fire = (el, type, init) => el.dispatchEvent(new (type === 'keydown' ? KeyboardEvent : Event)(type, Object.assign({bubbles: true}, init || {})));
			const user = document.getElementById('user'); user.value = 'mar'; fire(user, 'change'); user.value = 'marta'; fire(user, 'change');
			const pass = document.getElementById('pass'); pass.value = 'hunter2'; fire(pass, 'change');
			const size = document.getElementById('size'); size.value = '43'; fire(size, 'change');
			pass.focus(); fire(pass, 'keydown', {key: 'Enter'});
			document.getElementById('add').click();
			document.querySelector('#login button').click();
			return true;
		})()`, nil))
		if err != nil {
			t.Error(err)
		}
		time.Sleep(300 * time.Millisecond)
		cancel()
	}})
	require.NoError(t, err)
	t.Logf("steps: %v", steps)
	require.GreaterOrEqual(t, len(steps), 6)
	assert.Equal(t, map[string]any{"navigate": srv.URL + "/"}, steps[0], "the opening navigation is the first step")
	assert.Equal(t, map[string]any{"fill": map[string]any{"selector": "#user", "text": "marta"}}, steps[1], "fills of one field collapse into the last value")
	assert.Equal(t, map[string]any{"fill": map[string]any{"selector": "#pass", "text": "{{secrets.PASSWORD}}"}}, steps[2], "a password is never recorded")
	assert.Equal(t, map[string]any{"select": map[string]any{"selector": "#size", "value": "43"}}, steps[3])
	assert.Equal(t, map[string]any{"press": "Enter"}, steps[4])
	assert.Equal(t, map[string]any{"click": "#add"}, steps[5])
	assert.Equal(t, map[string]any{"click": "#login > button"}, steps[6], "a button without an id gets a short path")
	assert.Len(t, logged, len(steps))

	yaml := StepsYAML(steps)
	assert.Contains(t, yaml, "steps:\n  - action: { navigate: \""+srv.URL+"/\" }\n")
	assert.Contains(t, yaml, `  - action: { fill: { selector: "#pass", text: "{{secrets.PASSWORD}}" } }`)
	assert.Contains(t, yaml, `  - action: { select: { selector: "#size", value: "43" } }`)
	assert.Contains(t, yaml, `  - action: { press: "Enter" }`)
	assert.Contains(t, yaml, `  - action: { click: "#add" }`)
}

func TestRecordRejectsBadURL(t *testing.T) {
	_, err := Record(context.Background(), "not a url", RecordOptions{Headless: true})
	assert.ErrorContains(t, err, "not an absolute URL")
}
