package runner

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

// cachePage is a page whose refs change between observations (as a fresh
// browser's would) but whose elements keep their role and name.
type cachePage struct {
	offset  int
	actions []string
	tree    func(offset int) string
	url     string
}

func (p *cachePage) URL(context.Context) (string, error) { return p.url, nil }
func (p *cachePage) Observe(context.Context, bool) (target.Observation, error) {
	return target.Observation{URL: p.url, Title: "shop", Tree: p.tree(p.offset)}, nil
}
func (p *cachePage) Click(_ context.Context, ref int) error {
	p.actions = append(p.actions, "click", itoa(ref))
	return nil
}
func (p *cachePage) Type(_ context.Context, ref int, text string) error {
	p.actions = append(p.actions, "type", itoa(ref), text)
	return nil
}
func (p *cachePage) Select(_ context.Context, ref int, v string) error {
	p.actions = append(p.actions, "select", itoa(ref), v)
	return nil
}
func (p *cachePage) Scroll(_ context.Context, d string) error {
	p.actions = append(p.actions, "scroll", d)
	return nil
}
func (p *cachePage) Navigate(_ context.Context, u string) error {
	p.actions = append(p.actions, "navigate", u)
	return nil
}
func (p *cachePage) Press(context.Context, string) error                { return nil }
func (p *cachePage) Wait(context.Context, time.Duration) error          { return nil }
func (p *cachePage) Screenshot(context.Context) ([]byte, error)         { return nil, nil }
func (p *cachePage) Eval(context.Context, string) (any, error)          { return nil, nil }
func (p *cachePage) Text(context.Context) (string, error)               { return "", nil }
func (p *cachePage) Do(context.Context, map[string]any) (string, error) { return "", nil }

func itoa(i int) string { return strconv.Itoa(i) }

func loginTree(offset int) string {
	return "    heading \"Log in\" (h1)\n[" + itoa(offset+1) + "] textbox \"Username\" value=\"\"\n[" + itoa(offset+2) + "] textbox \"Password\"\n[" + itoa(offset+3) + "] button \"Log in\" (focused)"
}

func TestKeyAndFingerprint(t *testing.T) {
	obs := target.Observation{URL: "http://shop.test/login?next=/cart", Tree: loginTree(0)}
	k1 := Key("Log in as marta", obs)
	assert.Len(t, k1, 24)
	assert.Equal(t, k1, Key("Log in as marta", target.Observation{URL: "http://other.host/login", Tree: loginTree(5)}), "the host and the ref numbers do not matter; the path and the elements do")
	assert.NotEqual(t, k1, Key("Log in as jordi", obs), "another goal")
	assert.NotEqual(t, k1, Key("Log in as marta", target.Observation{URL: "http://shop.test/signup", Tree: loginTree(0)}), "another path")
	assert.NotEqual(t, k1, Key("Log in as marta", target.Observation{URL: obs.URL, Tree: loginTree(0) + "\n[9] button \"Forgot password\""}), "a changed page")
	assert.Equal(t, `button "Log in"`, elementOf(`[3] button "Log in" (focused)`))
	assert.Equal(t, `textbox "Username"`, elementOf(`  [1]   textbox "Username" value="marta"`))
	assert.Equal(t, "", elementOf(`    heading "Log in" (h1)`))
	assert.Equal(t, "combobox", elementOf(`[4] combobox (expanded)`))
}

func TestSaveLoadReplay(t *testing.T) {
	dir := t.TempDir()
	cache := NewActionCache(dir)
	plan := &Plan{Goal: "Log in as marta", Key: "abc", Actions: []target.Action{
		{Name: "type", Args: map[string]any{"ref": 1, "text": "marta", "element": `textbox "Username"`}},
		{Name: "type", Args: map[string]any{"ref": 2, "text": "{{secrets.TEST_PASSWORD}}", "element": `textbox "Password"`}},
		{Name: "click", Args: map[string]any{"ref": 3, "element": `button "Log in"`}},
		{Name: "scroll", Args: map[string]any{"direction": "down"}},
		{Name: "wait", Args: map[string]any{"ms": 10}},
	}}
	require.NoError(t, cache.Save(plan))
	_, err := filepath.Abs(filepath.Join(dir, "actions", "abc.json"))
	require.NoError(t, err)
	loaded, err := cache.Load("abc")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "Log in as marta", loaded.Goal)
	assert.False(t, loaded.CreatedAt.IsZero())
	missing, err := cache.Load("nope")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// Replay on a page whose refs moved by 10.
	page := &cachePage{offset: 10, tree: loginTree, url: "http://shop.test/login"}
	n, err := Replay(context.Background(), page, loaded)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, []string{"type", itoa(11), "marta", "type", itoa(12), "{{secrets.TEST_PASSWORD}}", "click", itoa(13), "scroll", "down"}, page.actions)

	// An element that is gone fails the replay at that action.
	gone := &cachePage{tree: func(int) string { return "[1] textbox \"Username\"" }, url: "http://shop.test/login"}
	n, err = Replay(context.Background(), gone, loaded)
	assert.ErrorIs(t, err, ErrReplay)
	assert.ErrorContains(t, err, `action 2 (type): no element textbox "Password" on the page`)
	assert.Equal(t, 1, n)
	require.NoError(t, cache.Delete("abc"))
	require.NoError(t, cache.Delete("abc"), "deleting twice is fine")
}
