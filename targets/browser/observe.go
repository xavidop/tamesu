package browser

import (
	"context"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/domsnapshot"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// axTree is the accessibility tree of one frame.
type axTree struct {
	root accessibility.NodeID
	byID map[accessibility.NodeID]*accessibility.Node
}

func newAXTree(nodes []*accessibility.Node) *axTree {
	if len(nodes) == 0 {
		return nil
	}
	t := &axTree{root: nodes[0].NodeID, byID: make(map[accessibility.NodeID]*accessibility.Node, len(nodes))}
	for _, n := range nodes {
		t.byID[n.NodeID] = n
	}
	return t
}

// pageView is what an observation is built from, for the page or for a
// cross-site iframe (which Chromium runs as a target of its own, with its
// own node ids): the accessibility tree, the tree of every frame of the same
// process under the iframe element that holds it, the views of the
// cross-site iframes likewise, and the elements that answer a click without
// a role that says so.
type pageView struct {
	// frame is the cross-site iframe's target; empty for the page.
	frame     target.ID
	main      *axTree
	frames    map[cdp.BackendNodeID]*axTree
	remote    map[cdp.BackendNodeID]*pageView
	clickable map[cdp.BackendNodeID]bool
}

// nodeKey names a DOM node of the session: node ids repeat between the page
// and a cross-site iframe.
type nodeKey struct {
	frame   target.ID
	backend cdp.BackendNodeID
}

// readPage reads the view. Only the page's own tree is required: a frame or
// a snapshot the browser will not give leaves the observation without it.
func readPage(ctx context.Context, t *chromedp.Target) (pageView, error) {
	res, err := cdp.Call(ctx, t, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
	if err != nil {
		return pageView{}, err
	}
	v := pageView{main: newAXTree(res.Nodes), frames: map[cdp.BackendNodeID]*axTree{}}
	if ft, err := cdp.Call(ctx, t, page.GetFrameTree, cdp.Empty{}); err == nil && ft.FrameTree != nil {
		var visit func(f *page.FrameTree)
		visit = func(f *page.FrameTree) {
			for _, c := range f.ChildFrames {
				if c.Frame == nil {
					continue
				}
				owner, oerr := cdp.Call(ctx, t, dom.GetFrameOwner, dom.GetFrameOwnerParams{FrameID: c.Frame.ID})
				ax, aerr := cdp.Call(ctx, t, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{FrameID: c.Frame.ID})
				if oerr == nil && aerr == nil && owner.BackendNodeID != 0 {
					if sub := newAXTree(ax.Nodes); sub != nil {
						v.frames[owner.BackendNodeID] = sub
					}
				}
				visit(c)
			}
		}
		visit(ft.FrameTree)
	}
	v.clickable = clickableNodes(ctx, t)
	return v, nil
}

// clickableNodes are the visible elements, of the page and its frames, that
// answer a click: the ones with a mouse listener of their own (or that can
// be edited), and the outermost element of each run with a pointer cursor,
// which is how a page marks an element whose listener sits on an ancestor.
func clickableNodes(ctx context.Context, t *chromedp.Target) map[cdp.BackendNodeID]bool {
	snap, err := cdp.Call(ctx, t, domsnapshot.CaptureSnapshot, domsnapshot.CaptureSnapshotParams{ComputedStyles: []string{"cursor", "visibility"}})
	if err != nil {
		return nil
	}
	str := func(i int64) string {
		if i < 0 || int(i) >= len(snap.Strings) {
			return ""
		}
		return snap.Strings[i]
	}
	out := map[cdp.BackendNodeID]bool{}
	for _, d := range snap.Documents {
		if d.Nodes == nil || d.Layout == nil {
			continue
		}
		n := len(d.Nodes.BackendNodeID)
		pointer, visible, listens := make([]bool, n), make([]bool, n), make([]bool, n)
		for i, ni := range d.Layout.NodeIndex {
			if ni < 0 || int(ni) >= n || i >= len(d.Layout.Styles) || len(d.Layout.Styles[i]) < 2 {
				continue
			}
			pointer[ni] = str(d.Layout.Styles[i][0]) == "pointer"
			visible[ni] = str(d.Layout.Styles[i][1]) == "visible"
		}
		if d.Nodes.IsClickable != nil {
			for _, i := range d.Nodes.IsClickable.Index {
				if i >= 0 && int(i) < n {
					listens[i] = true
				}
			}
		}
		for i := range n {
			if !visible[i] || i >= len(d.Nodes.NodeName) {
				continue
			}
			tag := str(int64(d.Nodes.NodeName[i]))
			if tag == "HTML" || tag == "BODY" || strings.HasPrefix(tag, "#") || strings.HasPrefix(tag, ":") {
				continue
			}
			head := pointer[i]
			if head && i < len(d.Nodes.ParentIndex) {
				if p := d.Nodes.ParentIndex[i]; p >= 0 && int(p) < n && pointer[p] {
					head = false
				}
			}
			// A label answers a click for its field, which has a ref of its
			// own; only a pointer cursor says the label is the control.
			if head || (listens[i] && tag != "LABEL") {
				out[d.Nodes.BackendNodeID[i]] = true
			}
		}
	}
	return out
}

// axText joins the text under n, cut to a name's length: the name of a
// clickable element that has no accessible one.
func axText(t *axTree, n *accessibility.Node) string {
	var parts []string
	size := 0
	var visit func(id accessibility.NodeID, depth int)
	visit = func(id accessibility.NodeID, depth int) {
		c := t.byID[id]
		if c == nil || size > 80 || depth > 40 {
			return
		}
		if valueString(c.Role) == "StaticText" {
			if s := strings.TrimSpace(valueString(c.Name)); s != "" {
				parts = append(parts, s)
				size += len(s)
			}
			return
		}
		for _, cc := range c.ChildIDs {
			visit(cc, depth+1)
		}
	}
	for _, c := range n.ChildIDs {
		visit(c, 0)
	}
	text := strings.Join(parts, " ")
	if len(text) > 80 {
		text = text[:77] + "..."
	}
	return text
}

// readRemoteFrames adds the cross-site iframes of the page to its view. Each
// is read through a context attached to it, which the session keeps for the
// actions on its elements; an iframe inside another cross-site iframe is not
// reached.
func (s *session) readRemoteFrames(ctx context.Context, v *pageView) {
	tctx, cancel := context.WithTimeout(s.ctx, s.t.cfg.StepTimeout)
	infos, err := chromedp.Targets(tctx)
	cancel()
	if err != nil {
		return
	}
	live := map[target.ID]bool{}
	for _, info := range infos {
		if info.Type != "iframe" {
			continue
		}
		// The iframe element of the page that holds this target, if the
		// page is its parent.
		var owner cdp.BackendNodeID
		err := s.do(ctx, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
			res, err := cdp.Call(ctx, t, dom.GetFrameOwner, dom.GetFrameOwnerParams{FrameID: cdp.FrameID(info.TargetID)})
			owner = res.BackendNodeID
			return chromedp.Void{}, err
		})
		if err != nil || owner == 0 {
			continue
		}
		s.attachFrame(info.TargetID)
		var sub pageView
		err = s.doIn(info.TargetID, ctx, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
			var err error
			sub, err = readPage(ctx, t)
			return chromedp.Void{}, err
		})
		if err != nil || sub.main == nil {
			continue
		}
		live[info.TargetID] = true
		sub.frame = info.TargetID
		if v.remote == nil {
			v.remote = map[cdp.BackendNodeID]*pageView{}
		}
		v.remote[owner] = &sub
	}
	s.detachFramesExcept(live)
}

// attachFrame makes the context attached to a cross-site iframe, once.
func (s *session) attachFrame(id target.ID) {
	s.mu.Lock()
	_, ok := s.frames[id]
	s.mu.Unlock()
	if ok {
		return
	}
	// Detaching on cancel leaves the iframe in the page.
	fctx, cancel := chromedp.NewContext(s.ctx, chromedp.WithTargetID(id), chromedp.WithDetachOnCancel())
	// Attach with the context itself: the attachment lives as long as the
	// context of the first action, and every later one has a deadline.
	if err := chromedp.Do(fctx, chromedp.Sleep(0)); err != nil {
		cancel()
		return
	}
	s.mu.Lock()
	s.frames[id] = frameContext{ctx: fctx, cancel: cancel}
	s.mu.Unlock()
}

// detachFramesExcept lets go of the iframes that are no longer in the page.
func (s *session) detachFramesExcept(live map[target.ID]bool) {
	s.mu.Lock()
	var gone []frameContext
	for id, f := range s.frames {
		if !live[id] {
			gone = append(gone, f)
			delete(s.frames, id)
		}
	}
	s.mu.Unlock()
	for _, f := range gone {
		f.cancel()
	}
}
