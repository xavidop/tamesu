package browser

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/xavidop/tamesu/target"
)

// axeSource is axe-core (assets/AXE-LICENSE), injected by the a11y action.
//
//go:embed assets/axe.min.js
var axeSource string

// Mock answers requests that match it instead of the network.
type Mock struct {
	// URL is a glob on the full URL (** crosses slashes); a pattern without
	// a wildcard matches as a substring.
	URL string
	// Method limits the mock to one method; empty means any.
	Method string
	// Status is the answer's status (default 200).
	Status int
	// Body is the answer: a string as is, anything else as JSON.
	Body any
	// Headers are added to the answer; a JSON body sets Content-Type.
	Headers map[string]string
	// DelayMs holds the answer back.
	DelayMs int
	// Abort fails the request instead of answering it.
	Abort bool
}

func (m Mock) matches(method, url string) bool {
	if m.Method != "" && !strings.EqualFold(m.Method, method) {
		return false
	}
	if strings.ContainsAny(m.URL, "*?[") {
		ok, err := doublestar.Match(m.URL, url)
		return err == nil && ok
	}
	return strings.Contains(url, m.URL)
}

// enableMocks switches request interception on (once).
func (s *session) enableMocks(ctx context.Context) error {
	s.mu.Lock()
	on := s.fetchOn
	s.fetchOn = true
	s.mu.Unlock()
	if on {
		return nil
	}
	return s.do(ctx, fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}))
}

// onRequestPaused answers a paused request from the first matching mock or
// lets it through.
func (s *session) onRequestPaused(ev *fetch.EventRequestPaused) {
	s.mu.Lock()
	rules := append([]Mock(nil), s.mocks...)
	s.mu.Unlock()
	var hit *Mock
	for i := range rules {
		if rules[i].matches(ev.Request.Method, ev.Request.URL) {
			hit = &rules[i]
			break
		}
	}
	go func() {
		if hit == nil {
			_ = chromedp.Run(s.ctx, fetch.ContinueRequest(ev.RequestID))
			return
		}
		if hit.DelayMs > 0 {
			select {
			case <-time.After(time.Duration(hit.DelayMs) * time.Millisecond):
			case <-s.ctx.Done():
				return
			}
		}
		s.mu.Lock()
		s.mocked++
		s.mu.Unlock()
		s.har.mocked(ev.NetworkID)
		s.event("mock", ev.Request.Method+" "+ev.Request.URL)
		if hit.Abort {
			_ = chromedp.Run(s.ctx, fetch.FailRequest(ev.RequestID, network.ErrorReasonBlockedByClient))
			return
		}
		status := hit.Status
		if status == 0 {
			status = 200
		}
		body, ctype := mockBody(hit.Body)
		var headers []*fetch.HeaderEntry
		if ctype != "" {
			headers = append(headers, &fetch.HeaderEntry{Name: "Content-Type", Value: ctype})
		}
		for k, v := range hit.Headers {
			headers = append(headers, &fetch.HeaderEntry{Name: k, Value: v})
		}
		req := fetch.FulfillRequest(ev.RequestID, int64(status)).WithBody(base64.StdEncoding.EncodeToString(body))
		if len(headers) > 0 {
			req = req.WithResponseHeaders(headers)
		}
		_ = chromedp.Run(s.ctx, req)
	}()
}

func mockBody(v any) ([]byte, string) {
	switch x := v.(type) {
	case nil:
		return nil, ""
	case string:
		return []byte(x), ""
	case []byte:
		return x, ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(fmt.Sprint(v)), ""
	}
	return b, "application/json"
}

// mockFromAction reads the mock action's map.
func mockFromAction(arg any) (Mock, error) {
	m, ok := arg.(map[string]any)
	if !ok {
		return Mock{}, fmt.Errorf("mock wants {url, method, status, body, headers, delay_ms, abort}")
	}
	out := Mock{}
	out.URL, _ = m["url"].(string)
	if out.URL == "" {
		return Mock{}, fmt.Errorf("mock wants a url")
	}
	out.Method, _ = m["method"].(string)
	if st, ok := asInt(m["status"]); ok {
		out.Status = st
	}
	out.Body = m["body"]
	if h, ok := m["headers"].(map[string]any); ok {
		out.Headers = map[string]string{}
		for k, v := range h {
			out.Headers[k] = fmt.Sprint(v)
		}
	}
	if d, ok := asInt(m["delay_ms"]); ok {
		out.DelayMs = d
	}
	out.Abort, _ = m["abort"].(bool)
	return out, nil
}

// Accessibility.

var impactRank = map[string]int{"minor": 1, "moderate": 2, "serious": 3, "critical": 4}

type a11yViolation struct {
	ID     string `json:"id"`
	Impact string `json:"impact"`
	Help   string `json:"help"`
	Nodes  int    `json:"nodes"`
}

// a11y runs axe-core on the page and fails the step when violations at or
// above the impact (default serious) exceed max (default 0).
func (s *session) a11y(ctx context.Context, arg any) (string, error) {
	impact, maxN, include := "serious", 0, ""
	if m, ok := arg.(map[string]any); ok {
		if v, ok := m["impact"].(string); ok && v != "" {
			impact = v
		}
		if v, ok := asInt(m["max"]); ok {
			maxN = v
		}
		include, _ = m["include"].(string)
	}
	if _, ok := impactRank[impact]; !ok {
		return "", fmt.Errorf("a11y impact must be minor, moderate, serious or critical")
	}
	var loaded bool
	if err := s.do(ctx, chromedp.Evaluate(`typeof window.axe !== "undefined"`, &loaded)); err != nil {
		return "", fmt.Errorf("a11y: %w", err)
	}
	if !loaded {
		if err := s.do(ctx, chromedp.Evaluate(axeSource, nil)); err != nil {
			return "", fmt.Errorf("a11y: inject axe-core: %w", err)
		}
	}
	ctxArg := "document"
	if include != "" {
		b, _ := json.Marshal(include)
		ctxArg = string(b)
	}
	var raw string
	js := fmt.Sprintf(`axe.run(%s, {resultTypes: ["violations"]}).then(r => JSON.stringify(r.violations.map(v => ({id: v.id, impact: v.impact || "minor", help: v.help, nodes: v.nodes.length}))))`, ctxArg)
	if err := s.do(ctx, chromedp.Evaluate(js, &raw, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
		return "", fmt.Errorf("a11y: %w", err)
	}
	var all []a11yViolation
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return "", fmt.Errorf("a11y: axe answered %q", short(raw))
	}
	sort.Slice(all, func(i, j int) bool { return impactRank[all[i].Impact] > impactRank[all[j].Impact] })
	var counted []a11yViolation
	for _, v := range all {
		if impactRank[v.Impact] >= impactRank[impact] {
			counted = append(counted, v)
		}
	}
	s.mu.Lock()
	s.a11yRuns++
	n := s.a11yRuns
	s.a11yTotal += len(counted)
	s.mu.Unlock()
	rel := ""
	if s.run != nil && s.run.Dir != "" {
		rel = fmt.Sprintf("a11y-%d.json", n)
		if b, err := json.MarshalIndent(map[string]any{"impact": impact, "violations": all}, "", "  "); err == nil {
			if os.WriteFile(filepath.Join(s.run.Dir, rel), b, 0o644) == nil {
				s.mu.Lock()
				s.files[rel] = rel
				s.mu.Unlock()
			}
		}
	}
	s.event("a11y", fmt.Sprintf("%d violations at or above %s (%d in all)", len(counted), impact, len(all)))
	if len(counted) > maxN {
		var names []string
		for i, v := range counted {
			if i == 5 {
				names = append(names, fmt.Sprintf("and %d more", len(counted)-5))
				break
			}
			names = append(names, fmt.Sprintf("%s (%s, %d nodes)", v.ID, v.Impact, v.Nodes))
		}
		return "", &target.StepError{Reason: fmt.Sprintf("%d accessibility violations at or above %s, want at most %d: %s", len(counted), impact, maxN, strings.Join(names, "; "))}
	}
	return fmt.Sprintf("a11y: %d violations at or above %s (%d in all)", len(counted), impact, len(all)), nil
}

// Visual regression.

// visual compares a screenshot with the baseline visual/<name>.png next to
// the suite: a missing baseline is written, a difference ratio above the
// threshold (default 0.01) fails the step and writes a diff image.
func (s *session) visual(ctx context.Context, arg any) (string, error) {
	name, threshold := "", 0.01
	var masks []string
	switch x := arg.(type) {
	case string:
		name = x
	case map[string]any:
		name, _ = x["name"].(string)
		if t, ok := x["threshold"].(float64); ok {
			threshold = t
		} else if t, ok := asInt(x["threshold"]); ok {
			threshold = float64(t)
		}
		if ms, ok := x["mask"].([]any); ok {
			for _, m := range ms {
				if sel, ok := m.(string); ok {
					masks = append(masks, sel)
				}
			}
		}
	}
	if name == "" {
		return "", fmt.Errorf("visual wants a name (or {name, threshold, mask})")
	}
	if s.run == nil || s.run.SuiteDir == "" {
		return "", fmt.Errorf("visual: the run has no suite directory for baselines")
	}
	shot, err := s.Screenshot(ctx)
	if err != nil {
		return "", err
	}
	actual, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		return "", fmt.Errorf("visual: decode screenshot: %w", err)
	}
	rects, err := s.maskRects(ctx, masks)
	if err != nil {
		return "", err
	}
	actualRGBA := toRGBA(actual)
	blackOut(actualRGBA, rects)
	baseline := filepath.Join(s.run.SuiteDir, "visual", slug(name)+".png")
	if s.run.UpdateBaselines {
		if err := writePNG(baseline, actualRGBA); err != nil {
			return "", err
		}
		s.event("visual", name+": baseline updated")
		return "visual " + name + ": baseline updated", nil
	}
	b, err := os.ReadFile(baseline)
	if os.IsNotExist(err) {
		if err := writePNG(baseline, actualRGBA); err != nil {
			return "", err
		}
		s.event("visual", name+": baseline written")
		return "visual " + name + ": no baseline yet, written from this run; review it", nil
	}
	if err != nil {
		return "", fmt.Errorf("visual: %w", err)
	}
	want, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("visual: baseline %s: %w", baseline, err)
	}
	wantRGBA := toRGBA(want)
	blackOut(wantRGBA, rects)
	ratio, diff := diffImages(wantRGBA, actualRGBA)
	s.event("visual", fmt.Sprintf("%s: %.2f%% of pixels differ", name, ratio*100))
	if ratio > threshold {
		s.mu.Lock()
		s.visualDiffs++
		s.mu.Unlock()
		if s.run.Dir != "" {
			for file, img := range map[string]*image.RGBA{slug(name) + "-diff.png": diff, slug(name) + "-actual.png": actualRGBA} {
				rel := filepath.Join("visual", file)
				if writePNG(filepath.Join(s.run.Dir, rel), img) == nil {
					s.mu.Lock()
					s.files[rel] = rel
					s.mu.Unlock()
				}
			}
		}
		return "", &target.StepError{Reason: fmt.Sprintf("visual %s: %.2f%% of pixels differ from the baseline, want at most %.2f%% (visual/%s-diff.png)", name, ratio*100, threshold*100, slug(name))}
	}
	return fmt.Sprintf("visual %s: %.2f%% of pixels differ, within %.2f%%", name, ratio*100, threshold*100), nil
}

// maskRects returns the viewport rectangles of the masked selectors.
func (s *session) maskRects(ctx context.Context, selectors []string) ([]image.Rectangle, error) {
	var out []image.Rectangle
	for _, sel := range selectors {
		b, _ := json.Marshal(sel)
		var rects [][]float64
		js := fmt.Sprintf(`Array.from(document.querySelectorAll(%s)).map(e => { const r = e.getBoundingClientRect(); return [r.x, r.y, r.width, r.height]; })`, b)
		if err := s.do(ctx, chromedp.Evaluate(js, &rects)); err != nil {
			return nil, fmt.Errorf("visual: mask %q: %w", sel, err)
		}
		for _, r := range rects {
			if len(r) == 4 {
				out = append(out, image.Rect(int(r[0]), int(r[1]), int(r[0]+r[2])+1, int(r[1]+r[3])+1))
			}
		}
	}
	return out, nil
}

func toRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

func blackOut(img *image.RGBA, rects []image.Rectangle) {
	for _, r := range rects {
		draw.Draw(img, r.Intersect(img.Bounds()), image.NewUniform(color.Black), image.Point{}, draw.Src)
	}
}

// diffImages returns the share of differing pixels (a size difference counts
// as 1) and a diff image: the actual dimmed, differing pixels in red.
func diffImages(want, got *image.RGBA) (float64, *image.RGBA) {
	const tolerance = 16
	wb, gb := want.Bounds(), got.Bounds()
	diff := image.NewRGBA(gb)
	draw.Draw(diff, gb, got, gb.Min, draw.Src)
	if wb.Dx() != gb.Dx() || wb.Dy() != gb.Dy() {
		return 1, diff
	}
	total, differing := wb.Dx()*wb.Dy(), 0
	for y := 0; y < wb.Dy(); y++ {
		for x := 0; x < wb.Dx(); x++ {
			wi, gi := want.PixOffset(x, y), got.PixOffset(x, y)
			same := true
			for c := 0; c < 3; c++ {
				d := int(want.Pix[wi+c]) - int(got.Pix[gi+c])
				if d < -tolerance || d > tolerance {
					same = false
					break
				}
			}
			di := diff.PixOffset(x, y)
			if same {
				diff.Pix[di] = diff.Pix[di]/3 + 170
				diff.Pix[di+1] = diff.Pix[di+1]/3 + 170
				diff.Pix[di+2] = diff.Pix[di+2]/3 + 170
			} else {
				differing++
				diff.Pix[di], diff.Pix[di+1], diff.Pix[di+2], diff.Pix[di+3] = 229, 72, 47, 255
			}
		}
	}
	if total == 0 {
		return 0, diff
	}
	return float64(differing) / float64(total), diff
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
