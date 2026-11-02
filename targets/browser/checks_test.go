package browser

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

func TestMocks(t *testing.T) {
	srv := server(t)
	page, sess, dir := start(t, srv, Config{Mocks: []Mock{{URL: "**/cart", Body: map[string]any{"items": 3}}}})
	ctx := context.Background()
	require.NoError(t, page.Navigate(ctx, srv.URL+"/cart"))
	text, err := page.Text(ctx)
	require.NoError(t, err)
	assert.Contains(t, text, `"items":3`, "the cart came from the mock, not the server")

	// A mock added at run time wins over the earlier one, and abort fails the request.
	did, err := page.Do(ctx, map[string]any{"mock": map[string]any{"url": "**/cart", "status": 503, "body": "down"}})
	require.NoError(t, err)
	assert.Equal(t, "mock **/cart", did)
	require.NoError(t, page.Navigate(ctx, srv.URL+"/cart"))
	text, _ = page.Text(ctx)
	assert.Contains(t, text, "down")
	_, err = page.Do(ctx, map[string]any{"mock": map[string]any{"url": "**/external", "abort": true}})
	require.NoError(t, err)
	err = page.Navigate(ctx, srv.URL+"/external")
	assert.Error(t, err, "an aborted navigation fails")

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, arts.Metrics["mocked_requests"], 2.0)
	b, err := os.ReadFile(filepath.Join(dir, HARFile))
	require.NoError(t, err)
	var har struct {
		Log struct {
			Entries []struct {
				Mocked  bool                 `json:"_mocked"`
				Request struct{ URL string } `json:"request"`
			} `json:"entries"`
		} `json:"log"`
	}
	require.NoError(t, json.Unmarshal(b, &har))
	mocked := 0
	for _, e := range har.Log.Entries {
		if e.Mocked {
			mocked++
		}
	}
	assert.GreaterOrEqual(t, mocked, 1, "mocked requests are marked in the HAR")
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "mock")
}

func TestMockMatching(t *testing.T) {
	assert.True(t, Mock{URL: "**/api/cart"}.matches("GET", "https://shop.example.com/api/cart"))
	assert.True(t, Mock{URL: "https://shop.example.com/api/*"}.matches("GET", "https://shop.example.com/api/cart"))
	assert.False(t, Mock{URL: "https://shop.example.com/api/*"}.matches("GET", "https://shop.example.com/api/cart/1"))
	assert.True(t, Mock{URL: "/cart"}.matches("GET", "https://shop.example.com/api/cart?x=1"), "no wildcard: a substring")
	assert.False(t, Mock{URL: "/cart", Method: "POST"}.matches("GET", "https://shop.example.com/cart"))
	body, ctype := mockBody(map[string]any{"a": 1})
	assert.JSONEq(t, `{"a":1}`, string(body))
	assert.Equal(t, "application/json", ctype)
	_, err := mockFromAction("nope")
	assert.Error(t, err)
}

func TestA11y(t *testing.T) {
	srv := server(t)
	page, sess, dir := start(t, srv, Config{})
	ctx := context.Background()
	// The shop page has an image without alt text once we add one, and no main landmark issues otherwise.
	_, err := page.Eval(ctx, `document.body.insertAdjacentHTML('beforeend', '<img src="x.png" id="noalt">'); 1`)
	require.NoError(t, err)
	did, err := page.Do(ctx, map[string]any{"a11y": map[string]any{"impact": "minor", "max": 100}})
	require.NoError(t, err)
	assert.Contains(t, did, "a11y:")
	_, err = page.Do(ctx, map[string]any{"a11y": map[string]any{"impact": "critical", "max": 0}})
	var se *target.StepError
	require.ErrorAs(t, err, &se, "image-alt is a critical violation")
	assert.Contains(t, se.Reason, "image-alt")
	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, arts.Metrics["a11y_violations"], 1.0)
	assert.Equal(t, "a11y-1.json", arts.Files["a11y-1.json"])
	b, err := os.ReadFile(filepath.Join(dir, "a11y-2.json"))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"image-alt"`)
}

func TestVisual(t *testing.T) {
	srv := server(t)
	suiteDir := t.TempDir()
	if _, err := FindChromium(context.Background(), "", false); err != nil {
		t.Skip("no Chromium on this machine:", err)
	}
	newSession := func(update bool) (target.Page, target.Session, string) {
		dir := t.TempDir()
		tgt := New("web", Config{URL: srv.URL + "/", Viewport: "800x600"})
		sess, err := tgt.Start(context.Background(), &target.RunContext{TestName: t.Name(), Dir: dir, SuiteDir: suiteDir, UpdateBaselines: update, Render: func(s string) (string, error) { return s, nil }})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = sess.Close(context.Background()) })
		return sess.(target.BrowserSession).Page(), sess, dir
	}
	ctx := context.Background()
	page, _, _ := newSession(false)
	did, err := page.Do(ctx, map[string]any{"visual": "home"})
	require.NoError(t, err)
	assert.Contains(t, did, "no baseline yet, written from this run")
	_, err = os.Stat(filepath.Join(suiteDir, "visual", "home.png"))
	require.NoError(t, err)
	did, err = page.Do(ctx, map[string]any{"visual": map[string]any{"name": "home", "threshold": 0.001}})
	require.NoError(t, err)
	assert.Contains(t, did, "within 0.10%")

	// Change the page: a big red block differs beyond the threshold, unless masked.
	_, err = page.Eval(ctx, `document.body.insertAdjacentHTML('afterbegin', '<div id="banner" style="width:800px;height:200px;background:red"></div>'); 1`)
	require.NoError(t, err)
	_, err = page.Do(ctx, map[string]any{"visual": map[string]any{"name": "home", "threshold": 0.05}})
	var se *target.StepError
	require.ErrorAs(t, err, &se)
	assert.Contains(t, se.Reason, "differ from the baseline")
	did, err = page.Do(ctx, map[string]any{"visual": map[string]any{"name": "home", "threshold": 0.05, "mask": []any{"#banner"}}})
	require.NoError(t, err, "the banner is masked on both sides")
	assert.Contains(t, did, "within 5.00%")
	// The baseline itself can be refreshed.
	page2, sess2, _ := newSession(true)
	did, err = page2.Do(ctx, map[string]any{"visual": "home"})
	require.NoError(t, err)
	assert.Contains(t, did, "baseline updated")
	arts, err := sess2.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0.0, arts.Metrics["visual_diffs"])
}

func TestDiffImages(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 10, 10))
	b := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			a.Set(x, y, color.White)
			b.Set(x, y, color.White)
		}
	}
	ratio, _ := diffImages(a, b)
	assert.Equal(t, 0.0, ratio)
	for x := 0; x < 5; x++ {
		b.Set(x, 0, color.Black)
	}
	ratio, diff := diffImages(a, b)
	assert.InDelta(t, 0.05, ratio, 0.0001)
	r, g, bl, _ := diff.At(0, 0).RGBA()
	assert.Equal(t, []uint32{229, 72, 47}, []uint32{r >> 8, g >> 8, bl >> 8}, "differing pixels are red")
	blackOut(b, []image.Rectangle{image.Rect(0, 0, 10, 1)})
	blackOut(a, []image.Rectangle{image.Rect(0, 0, 10, 1)})
	ratio, _ = diffImages(a, b)
	assert.Equal(t, 0.0, ratio, "masked rows are equal")
	c := image.NewRGBA(image.Rect(0, 0, 5, 5))
	ratio, _ = diffImages(a, c)
	assert.Equal(t, 1.0, ratio, "a size difference is a full difference")
}
