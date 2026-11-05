package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/a2apb"

	"github.com/xavidop/tamesu/targets/a2a/internal/pbjson"
)

// Push is where the agent posts task updates when push notifications are on.
type Push struct {
	// Addr is the address to listen on; empty means a free port on 127.0.0.1.
	Addr string
	// URL is what the agent is told to call; empty means http://<addr>. Set
	// it when the agent reaches tamesu through a tunnel or another host.
	URL string
}

// pushListener receives the notifications of every session of a target. Each
// session subscribes with a token; the agent sends it back in the
// X-A2A-Notification-Token header, and the URL carries it too for agents that
// do not.
type pushListener struct {
	srv  *http.Server
	url  string
	mu   sync.Mutex
	subs map[string]chan json.RawMessage
}

func startPush(cfg Push) (*pushListener, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("push listener on %s: %w", addr, err)
	}
	host := l.Addr().String()
	if h, port, err := net.SplitHostPort(host); err == nil && (h == "" || h == "::" || h == "0.0.0.0") {
		host = net.JoinHostPort("127.0.0.1", port)
	}
	u := cfg.URL
	if u == "" {
		u = "http://" + host
	}
	p := &pushListener{url: strings.TrimRight(u, "/"), subs: map[string]chan json.RawMessage{}}
	p.srv = &http.Server{Handler: http.HandlerFunc(p.handle), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(l) }()
	return p, nil
}

// subscribe returns the channel a session reads its notifications from and
// the URL the agent must post to.
func (p *pushListener) subscribe(token string) (<-chan json.RawMessage, string) {
	ch := make(chan json.RawMessage, 32)
	p.mu.Lock()
	p.subs[token] = ch
	p.mu.Unlock()
	return ch, p.url + "/" + token
}

func (p *pushListener) unsubscribe(token string) {
	p.mu.Lock()
	delete(p.subs, token)
	p.mu.Unlock()
}

// handle accepts one notification: a task in the protocol's JSON, or in the
// HTTP+JSON binding's JSON, which is converted.
func (p *pushListener) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "push notifications are POSTed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := r.Header.Get("X-A2A-Notification-Token")
	if token == "" {
		token = strings.TrimPrefix(r.URL.Path, "/")
		if i := strings.IndexByte(token, '/'); i >= 0 {
			token = token[:i]
		}
	}
	p.mu.Lock()
	ch := p.subs[token]
	p.mu.Unlock()
	if ch == nil {
		http.Error(w, "unknown notification token", http.StatusNotFound)
		return
	}
	ev := body
	var probe struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(body, &probe) != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	if probe.Kind == "" {
		var t a2apb.Task
		if err := pbjson.Unmarshal(body, &t); err != nil || t.GetId() == "" {
			http.Error(w, "not a task", http.StatusBadRequest)
			return
		}
		if ev, err = pbjson.TaskJSON(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	select {
	case ch <- ev:
	default:
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *pushListener) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)
}
