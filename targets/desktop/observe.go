package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xavidop/tamesu/target"
)

// maxTreeLines caps the observation so prompts stay bounded.
const maxTreeLines = 400

// maxDepth is how deep a snapshot reads: desktop toolkits nest a control
// under many unnamed groups.
const maxDepth = "50"

// overlays are the surfaces that sit on top of a window, in the order they
// are added to the observation. Each is a snapshot of its own.
var overlays = []string{"alert", "sheet", "popover", "menu"}

// menubar is the surface of an application's menu bar where the system keeps
// it outside the window (macOS).
const menubar = "menubar"

// node is one element of agent-desktop's accessibility tree.
type node struct {
	Role        string          `json:"role"`
	Name        string          `json:"name"`
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description"`
	States      []string        `json:"states"`
	RefID       string          `json:"ref_id"`
	Children    []node          `json:"children"`
}

// text is the node's value as text ("" for none).
func (n node) text() string {
	if len(n.Value) == 0 || string(n.Value) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(n.Value, &s) == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(n.Value))
}

// snapshot is the data of a snapshot command.
type snapshot struct {
	App        string `json:"app"`
	SnapshotID string `json:"snapshot_id"`
	Complete   bool   `json:"complete"`
	Window     *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"window"`
	Tree node `json:"tree"`
}

// textRoles are written as text, not as an element.
var textRoles = map[string]bool{"statictext": true, "text": true, "label": true, "paragraph": true}

// element is a node the agent may act on: agent-desktop's ref of the current
// observation and what the node is.
type element struct {
	ref  string
	role string
	name string
	// offscreen is true for an element outside what the window shows, which
	// has to be scrolled to before it answers.
	offscreen bool
}

// treeBuilder writes the lines of an observation and hands out refs.
type treeBuilder struct {
	lines []string
	// seen counts the elements with the same surface, role and name, so the
	// second "Delete" button keeps a number of its own.
	seen map[string]int
	// assign returns the session's number for an element key.
	assign func(key string) int
	refs   map[int]element
	text   []string
	// known are the elements written so far, by role and name.
	known map[string]bool
	// limit, when positive, is how deep the surface being written is read.
	limit int
}

// newTreeBuilder returns a builder that numbers elements with assign.
func newTreeBuilder(assign func(key string) int) *treeBuilder {
	return &treeBuilder{seen: map[string]int{}, refs: map[int]element{}, known: map[string]bool{}, assign: assign}
}

// build writes the window and then what is open on top of it. A toolkit that
// keeps an open menu inside the window's own tree reports it a second time as
// a surface; what the window already lists is not written twice.
func (b *treeBuilder) build(snaps []snapshot, names []string) {
	for i, snap := range snaps {
		if i > 0 && b.lists(snap.Tree) {
			continue
		}
		// The menu bar is its titles: what a menu holds is listed when the
		// menu is open, as a surface of its own.
		b.limit = 0
		if names[i] == menubar {
			b.limit = 1
		}
		b.add(names[i], snap.Tree, 0)
	}
	b.limit = 0
}

// lists reports whether every element of the tree that can be acted on is
// one the builder has written already.
func (b *treeBuilder) lists(n node) bool {
	any, all := false, true
	var visit func(n node)
	visit = func(n node) {
		if hidden(n) {
			return
		}
		if n.RefID != "" {
			any = true
			if !b.known[n.Role+"|"+label(n)] {
				all = false
			}
		}
		for _, c := range n.Children {
			visit(c)
		}
	}
	visit(n)
	return any && all
}

// label is the name an element goes by: its own, or its description when it
// has none.
func label(n node) string {
	if name := strings.TrimSpace(n.Name); name != "" {
		return name
	}
	return strings.TrimSpace(n.Description)
}

// hidden reports what is in the tree without being shown: an element the app
// marks hidden (a collapsed scroll bar, a status line that is not up), and
// what a closed menu holds, whose items are in the tree, marked off screen,
// long before anyone opens it. Neither answers anything, so they are left out
// of what the agent reads (an app's menus can hold hundreds) and of the
// window's text.
func hidden(n node) bool {
	if has(n.States, "hidden") {
		return true
	}
	return (n.Role == "menu" || n.Role == "menuitem") && has(n.States, "offscreen")
}

func has(states []string, state string) bool {
	for _, st := range states {
		if st == state {
			return true
		}
	}
	return false
}

// add writes the tree of one surface.
func (b *treeBuilder) add(surface string, n node, depth int) {
	if hidden(n) || (b.limit > 0 && depth > b.limit) {
		return
	}
	name := strings.TrimSpace(n.Name)
	value := n.text()
	if name != "" {
		b.text = append(b.text, name)
	}
	// A value counts as the window's text when it belongs to something a
	// person reads or fills: a control, a named element, a piece of text.
	if value != "" && value != name && (n.RefID != "" || name != "" || textRoles[n.Role]) {
		b.text = append(b.text, value)
	}
	switch {
	case len(b.lines) >= maxTreeLines:
	case n.RefID != "" && label(n) == "" && value == "" && has(n.States, "disabled"):
		// Nothing to read and nothing to do: a separator between menu items.
	case n.RefID != "":
		// A control with no name of its own is known by its description.
		name = label(n)
		b.known[n.Role+"|"+name] = true
		key := surface + "|" + n.Role + "|" + name
		b.seen[key]++
		ref := b.assign(fmt.Sprintf("%s|%d", key, b.seen[key]))
		b.refs[ref] = element{ref: n.RefID, role: n.Role, name: name, offscreen: has(n.States, "offscreen")}
		b.lines = append(b.lines, line(fmt.Sprintf("[%d] ", ref), depth, n.Role, name, value, n.States))
	case name != "" && textRoles[n.Role]:
		if len(name) > 200 {
			name = name[:197] + "..."
		}
		b.lines = append(b.lines, "    "+strings.Repeat(" ", min(depth, 12))+`text "`+name+`"`)
	case name == "" && value != "" && textRoles[n.Role]:
		if len(value) > 200 {
			value = value[:197] + "..."
		}
		b.lines = append(b.lines, "    "+strings.Repeat(" ", min(depth, 12))+`text "`+value+`"`)
	case name != "":
		// A value with no name on something that cannot be acted on (a ruler's
		// markers, a splitter's position) says nothing to the agent.
		b.lines = append(b.lines, line("    ", depth, n.Role, name, value, n.States))
	}
	for _, c := range n.Children {
		b.add(surface, c, depth+1)
	}
}

// line is one element in the form the agent reads: `[ref] role "name" (state)`.
func line(prefix string, depth int, role, name, value string, states []string) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(strings.Repeat(" ", min(depth, 12)))
	b.WriteString(role)
	if name != "" {
		fmt.Fprintf(&b, " %q", name)
	}
	if value != "" && value != name {
		if len(value) > 200 {
			value = value[:197] + "..."
		}
		fmt.Fprintf(&b, " value=%q", value)
	}
	if len(states) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(states, ", "))
	}
	return b.String()
}

// open lists what is open on top of the app's window, topmost kind first.
func (s *session) open(ctx context.Context) []string {
	var listed struct {
		Surfaces []struct {
			Type string `json:"type"`
		} `json:"surfaces"`
	}
	if err := s.cli.call(ctx, nil, &listed, "list-surfaces", "--app", s.app); err != nil {
		return nil
	}
	var out []string
	for _, kind := range overlays {
		for _, sf := range listed.Surfaces {
			if sf.Type == kind {
				out = append(out, kind)
				break
			}
		}
	}
	return out
}

// read takes the snapshots an observation is made of: the app's window, then
// whatever is open on top of it (an alert, a sheet, a popover, a menu), then
// the menu bar where it is not part of the window. Each is a snapshot of its
// own; only the window is required.
func (s *session) read(ctx context.Context) ([]snapshot, []string, error) {
	var win snapshot
	if err := s.cli.call(ctx, nil, &win, "snapshot", "--app", s.app, "--compact", "--max-depth", maxDepth); err != nil {
		return nil, nil, err
	}
	snaps, names := []snapshot{win}, []string{"window"}
	kinds := s.open(ctx)
	if s.menubar {
		kinds = append(kinds, menubar)
	}
	for _, kind := range kinds {
		var over snapshot
		if err := s.cli.call(ctx, nil, &over, "snapshot", "--app", s.app, "--surface", kind, "--compact", "--max-depth", maxDepth); err == nil {
			snaps, names = append(snaps, over), append(names, kind)
		}
	}
	return snaps, names, nil
}

// Observe reads the app's window and what is open on top of it.
func (s *session) Observe(ctx context.Context, withScreenshot bool) (target.Observation, error) {
	obs := target.Observation{URL: s.location()}
	snaps, names, err := s.read(ctx)
	if err != nil {
		return obs, fmt.Errorf("observe: %w", err)
	}
	s.mu.Lock()
	b := newTreeBuilder(func(key string) int {
		ref, ok := s.byKey[key]
		if !ok {
			s.nextRef++
			ref = s.nextRef
			s.byKey[key] = ref
		}
		return ref
	})
	b.build(snaps, names)
	// Only the refs of this observation can be acted on: agent-desktop's refs
	// belong to the snapshot they came from.
	s.refs = b.refs
	s.observations++
	s.mu.Unlock()
	obs.Tree = strings.Join(b.lines, "\n")
	if w := snaps[0].Window; w != nil {
		obs.Title = w.Title
		s.mu.Lock()
		s.windowID = w.ID
		s.mu.Unlock()
	}
	if obs.Title == "" {
		obs.Title = snaps[0].Tree.Name
	}
	if withScreenshot {
		if png, err := s.Screenshot(ctx); err == nil {
			obs.Screenshot = png
		}
	}
	return obs, nil
}

// Text is every name and value of the window and what is open on top of it,
// one per line.
func (s *session) Text(ctx context.Context) (string, error) {
	snaps, names, err := s.read(ctx)
	if err != nil {
		return "", fmt.Errorf("text: %w", err)
	}
	b := newTreeBuilder(func(string) int { return 0 })
	b.build(snaps, names)
	return strings.Join(b.text, "\n"), nil
}

// Screenshot captures the app's window as PNG. It needs the system's
// permission to record the screen (macOS); once that is refused the session
// stops asking.
func (s *session) Screenshot(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	refused := s.noScreenshots
	s.mu.Unlock()
	if refused != nil {
		return nil, refused
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := s.cli.call(ctx, nil, &shot, "screenshot", "--app", s.app); err != nil {
		err = fmt.Errorf("screenshot: %w", err)
		if hasCode(err, "PERM_DENIED", "PLATFORM_NOT_SUPPORTED") {
			s.mu.Lock()
			s.noScreenshots = err
			s.mu.Unlock()
		}
		return nil, err
	}
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return nil, fmt.Errorf("screenshot: unreadable image: %w", err)
	}
	return png, nil
}
