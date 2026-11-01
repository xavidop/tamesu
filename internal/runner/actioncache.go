package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xavidop/tamesu/target"
)

// DefaultCacheDir is where action plans are kept, relative to the working
// directory (cache.dir in tamesu.yaml overrides it).
const DefaultCacheDir = ".tamesu/cache"

// Plan is a goal's resolved actions, replayable without a model: each action
// carries the element line it acted on (`button "Log in"`), which is matched
// against a fresh observation at replay time.
type Plan struct {
	Goal      string          `json:"goal"`
	URLPath   string          `json:"url_path"`
	Key       string          `json:"key"`
	Actions   []target.Action `json:"actions"`
	Reason    string          `json:"reason,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Hits      int             `json:"hits"`
}

// ActionCache stores plans as JSON files under dir/actions.
type ActionCache struct{ dir string }

// NewActionCache returns the cache rooted at dir (DefaultCacheDir when empty).
func NewActionCache(dir string) *ActionCache {
	if dir == "" {
		dir = DefaultCacheDir
	}
	return &ActionCache{dir: filepath.Join(dir, "actions")}
}

// Key identifies a goal on a page: the goal text, the URL path and a
// fingerprint of the interactive elements (role and name), so the same goal
// on a changed page plans again.
func Key(goal string, obs target.Observation) string {
	path := obs.URL
	if u, err := url.Parse(obs.URL); err == nil {
		path = u.Path
	}
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(goal)))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(fingerprint(obs.Tree)))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// fingerprint is the sorted set of `role "name"` of the ref lines, capped.
func fingerprint(tree string) string {
	var elems []string
	for _, line := range strings.Split(tree, "\n") {
		if el := elementOf(line); el != "" {
			elems = append(elems, el)
		}
	}
	sort.Strings(elems)
	if len(elems) > 80 {
		elems = elems[:80]
	}
	return strings.Join(elems, "\n")
}

// elementOf returns the `role "name"` part of a ref line, "" otherwise.
func elementOf(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") {
		return ""
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[end+1:])
	// Drop the trailing value/state: keep `role "name"`.
	if i := strings.Index(rest, `" value=`); i >= 0 {
		rest = rest[:i+1]
	}
	if i := strings.Index(rest, `" (`); i >= 0 {
		rest = rest[:i+1]
	}
	if !strings.Contains(rest, `"`) {
		if i := strings.Index(rest, " ("); i >= 0 {
			rest = rest[:i]
		}
	}
	return rest
}

func (c *ActionCache) path(key string) string { return filepath.Join(c.dir, key+".json") }

// Load returns the plan for key, or nil.
func (c *ActionCache) Load(key string) (*Plan, error) {
	b, err := os.ReadFile(c.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", c.path(key), err)
	}
	return &p, nil
}

// Save writes the plan.
func (c *ActionCache) Save(p *Plan) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path(p.Key), append(b, '\n'), 0o644)
}

// Delete removes a plan.
func (c *ActionCache) Delete(key string) error {
	err := os.Remove(c.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ErrReplay is wrapped by every replay failure.
var ErrReplay = errors.New("replay failed")

// Replay runs the plan's actions on the page, resolving each element on a
// fresh observation. It returns the number of actions run and an error
// wrapping ErrReplay when an element is gone or an action fails.
func Replay(ctx context.Context, page target.Page, plan *Plan) (int, error) {
	done := 0
	for i, a := range plan.Actions {
		if err := replayOne(ctx, page, a); err != nil {
			return done, fmt.Errorf("%w: action %d (%s): %v", ErrReplay, i+1, a.Name, err)
		}
		done++
		_ = page.Wait(ctx, 300*time.Millisecond)
	}
	return done, nil
}

func replayOne(ctx context.Context, page target.Page, a target.Action) error {
	ref := func() (int, error) {
		el, _ := a.Args["element"].(string)
		if el == "" {
			return 0, errors.New("the plan has no element to match")
		}
		obs, err := page.Observe(ctx, false)
		if err != nil {
			return 0, err
		}
		r, ok := findElement(obs.Tree, el)
		if !ok {
			return 0, fmt.Errorf("no element %s on the page", el)
		}
		return r, nil
	}
	switch a.Name {
	case "click":
		r, err := ref()
		if err != nil {
			return err
		}
		return page.Click(ctx, r)
	case "type":
		r, err := ref()
		if err != nil {
			return err
		}
		text, _ := a.Args["text"].(string)
		return page.Type(ctx, r, text)
	case "select":
		r, err := ref()
		if err != nil {
			return err
		}
		value, _ := a.Args["value"].(string)
		return page.Select(ctx, r, value)
	case "scroll":
		dir, _ := a.Args["direction"].(string)
		return page.Scroll(ctx, dir)
	case "navigate":
		u, _ := a.Args["url"].(string)
		return page.Navigate(ctx, u)
	case "wait":
		ms, _ := a.Args["ms"].(float64)
		if n, ok := a.Args["ms"].(int); ok {
			ms = float64(n)
		}
		return page.Wait(ctx, time.Duration(ms)*time.Millisecond)
	}
	return fmt.Errorf("unknown action %q", a.Name)
}

// findElement returns the ref whose line names element (`role "name"`).
func findElement(tree, element string) (int, bool) {
	for _, line := range strings.Split(tree, "\n") {
		if elementOf(line) != element {
			continue
		}
		t := strings.TrimSpace(line)
		end := strings.Index(t, "]")
		var ref int
		if _, err := fmt.Sscanf(t[1:end], "%d", &ref); err == nil {
			return ref, true
		}
	}
	return 0, false
}
