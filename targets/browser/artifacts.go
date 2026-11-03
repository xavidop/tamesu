package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
)

// saveScreenshot writes a PNG under the run directory's screenshots folder
// and returns its path relative to the run directory.
func (s *session) saveScreenshot(name string, png []byte) (string, error) {
	if s.run == nil || s.run.Dir == "" {
		return "", nil
	}
	dir := filepath.Join(s.run.Dir, "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	rel := filepath.Join("screenshots", slug(name)+".png")
	if err := os.WriteFile(filepath.Join(s.run.Dir, rel), png, 0o644); err != nil {
		return "", err
	}
	s.event("screenshot", rel)
	return rel, nil
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '/':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "screenshot"
	}
	return out
}

// har collects network traffic into an HTTP Archive (HAR 1.2).
type har struct {
	mu      sync.Mutex
	entries map[network.RequestID]*harEntry
	order   []network.RequestID
	start   time.Time
}

type harEntry struct {
	// Mocked marks an answer tamesu gave instead of the network.
	Mocked          bool        `json:"_mocked,omitempty"`
	StartedDateTime string      `json:"startedDateTime"`
	Time            float64     `json:"time"`
	Request         harRequest  `json:"request"`
	Response        harResponse `json:"response"`
	Cache           struct{}    `json:"cache"`
	Timings         harTimings  `json:"timings"`
	started         time.Time
	wall            time.Time
}

type harRequest struct {
	Method      string      `json:"method"`
	URL         string      `json:"url"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []any       `json:"cookies"`
	Headers     []harHeader `json:"headers"`
	QueryString []any       `json:"queryString"`
	HeadersSize int         `json:"headersSize"`
	BodySize    int         `json:"bodySize"`
}

type harResponse struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []any       `json:"cookies"`
	Headers     []harHeader `json:"headers"`
	Content     harContent  `json:"content"`
	RedirectURL string      `json:"redirectURL"`
	HeadersSize int         `json:"headersSize"`
	BodySize    int         `json:"bodySize"`
	Error       string      `json:"_error,omitempty"`
}

type harContent struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
}

type harHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harTimings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

func newHAR() *har {
	return &har{entries: map[network.RequestID]*harEntry{}, start: time.Now()}
}

func (h *har) request(e *network.EventRequestWillBeSent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if e.WallTime != 0 {
		now = time.Unix(0, int64(e.WallTime.Float64()*float64(time.Second)))
	}
	entry := &harEntry{
		StartedDateTime: now.UTC().Format(time.RFC3339Nano), started: now, wall: now,
		Request:  harRequest{Method: e.Request.Method, URL: e.Request.URL, HTTPVersion: "HTTP/1.1", Cookies: []any{}, QueryString: []any{}, HeadersSize: -1, BodySize: -1, Headers: headers(e.Request.Headers)},
		Response: harResponse{HTTPVersion: "HTTP/1.1", Cookies: []any{}, Headers: []harHeader{}, HeadersSize: -1, BodySize: -1},
		Timings:  harTimings{Receive: 0},
	}
	if _, seen := h.entries[e.RequestID]; !seen {
		h.order = append(h.order, e.RequestID)
	}
	h.entries[e.RequestID] = entry
}

// mocked marks the request as answered by a mock.
func (h *har) mocked(id network.RequestID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.entries[id]; ok {
		e.Mocked = true
	} else {
		h.entries[id] = &harEntry{Mocked: true, started: time.Now(), wall: time.Now(), StartedDateTime: time.Now().UTC().Format(time.RFC3339Nano),
			Request:  harRequest{HTTPVersion: "HTTP/1.1", Cookies: []any{}, QueryString: []any{}, HeadersSize: -1, BodySize: -1, Headers: []harHeader{}},
			Response: harResponse{HTTPVersion: "HTTP/1.1", Cookies: []any{}, Headers: []harHeader{}, HeadersSize: -1, BodySize: -1}}
		h.order = append(h.order, id)
	}
}

func (h *har) response(e *network.EventResponseReceived) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.entries[e.RequestID]
	if entry == nil || e.Response == nil {
		return
	}
	entry.Response.Status = int(e.Response.Status)
	entry.Response.StatusText = e.Response.StatusText
	entry.Response.Headers = headers(e.Response.Headers)
	entry.Response.Content.MimeType = e.Response.MimeType
	if e.Response.Protocol != "" {
		entry.Response.HTTPVersion = e.Response.Protocol
	}
	entry.Timings.Wait = float64(time.Since(entry.started).Microseconds()) / 1000
}

func (h *har) finished(e *network.EventLoadingFinished) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry := h.entries[e.RequestID]; entry != nil {
		entry.Response.BodySize = int(e.EncodedDataLength)
		entry.Response.Content.Size = int(e.EncodedDataLength)
		entry.Time = float64(time.Since(entry.started).Microseconds()) / 1000
	}
}

func (h *har) failed(e *network.EventLoadingFailed) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry := h.entries[e.RequestID]; entry != nil {
		entry.Response.Error = e.ErrorText
		entry.Time = float64(time.Since(entry.started).Microseconds()) / 1000
	}
}

func headers(m network.Headers) []harHeader {
	out := make([]harHeader, 0, len(m))
	for k, v := range m {
		out = append(out, harHeader{Name: k, Value: fmt.Sprint(v)})
	}
	return out
}

// document is the HAR as a JSON-ready value.
func (h *har) document() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := make([]*harEntry, 0, len(h.order))
	for _, id := range h.order {
		entries = append(entries, h.entries[id])
	}
	return map[string]any{"log": map[string]any{
		"version": "1.2",
		"creator": map[string]any{"name": "tamesu", "version": "browser"},
		"pages":   []map[string]any{{"startedDateTime": h.start.UTC().Format(time.RFC3339Nano), "id": "page_1", "title": "tamesu run", "pageTimings": map[string]any{}}},
		"entries": entries,
	}}
}
