package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// sioConn is a socket.io v4 client over a websocket (Engine.IO v4), as far
// as the Voiceflow runtime needs: connect to the default namespace, emit
// events, read events, answer pings. No dependency on a socket.io library.
type sioConn struct {
	conn   *websocket.Conn
	events chan sioEvent
	cancel context.CancelFunc
	mu     sync.Mutex
	sent   int
	recv   int
}

// sioEvent is one event the server emitted, or the error that ended the
// connection.
type sioEvent struct {
	Name string
	Data json.RawMessage
	Err  error
}

// sioDial opens the Engine.IO websocket at the endpoint (the socket.io
// path, such as https://host/socket.io or https://host/v4/interact/socket),
// completes the Engine.IO open and the socket.io connect of the default
// namespace.
func sioDial(ctx context.Context, client *http.Client, endpoint string, hdr http.Header, timeout time.Duration) (*sioConn, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	q := u.Query()
	q.Set("EIO", "4")
	q.Set("transport", "websocket")
	u.RawQuery = q.Encode()
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, _, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPClient: client, HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u.String(), err)
	}
	conn.SetReadLimit(16 << 20)
	// Engine.IO open: "0{...}".
	_, data, err := conn.Read(dctx)
	if err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "no open packet")
		return nil, fmt.Errorf("engine.io open: %w", err)
	}
	if len(data) == 0 || data[0] != '0' {
		_ = conn.Close(websocket.StatusProtocolError, "bad open packet")
		return nil, fmt.Errorf("engine.io open: unexpected packet %q", excerpt(data))
	}
	// socket.io connect to the default namespace: "40" → "40{sid}" or "44{error}".
	if err := conn.Write(dctx, websocket.MessageText, []byte("40")); err != nil {
		return nil, fmt.Errorf("socket.io connect: %w", err)
	}
	for {
		_, data, err = conn.Read(dctx)
		if err != nil {
			_ = conn.Close(websocket.StatusProtocolError, "no connect ack")
			return nil, fmt.Errorf("socket.io connect: %w", err)
		}
		switch {
		case len(data) >= 2 && data[0] == '4' && data[1] == '0':
			s := &sioConn{conn: conn, events: make(chan sioEvent, 256)}
			readCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
			s.cancel = stop
			go s.read(readCtx)
			return s, nil
		case len(data) >= 2 && data[0] == '4' && data[1] == '4':
			_ = conn.Close(websocket.StatusNormalClosure, "refused")
			return nil, fmt.Errorf("socket.io connect refused: %s", excerpt(data[2:]))
		case len(data) == 1 && data[0] == '2':
			_ = conn.Write(dctx, websocket.MessageText, []byte("3"))
		}
	}
}

// read decodes packets into events until the connection ends: "2" pings are
// answered, "42[...]" are events, "41" is the namespace disconnect.
func (s *sioConn) read(ctx context.Context) {
	defer close(s.events)
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			select {
			case s.events <- sioEvent{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		if len(data) == 0 {
			continue
		}
		switch data[0] {
		case '2':
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = s.conn.Write(wctx, websocket.MessageText, []byte("3"))
			cancel()
		case '4':
			if len(data) < 2 {
				continue
			}
			switch data[1] {
			case '2', '3':
				// An event (or an ack): optional ack id digits, then a JSON array.
				payload := data[2:]
				i := 0
				for i < len(payload) && payload[i] >= '0' && payload[i] <= '9' {
					i++
				}
				var arr []json.RawMessage
				if json.Unmarshal(payload[i:], &arr) != nil || len(arr) == 0 {
					continue
				}
				var name string
				if json.Unmarshal(arr[0], &name) != nil {
					continue
				}
				ev := sioEvent{Name: name}
				if len(arr) > 1 {
					ev.Data = arr[1]
				}
				s.mu.Lock()
				s.recv++
				s.mu.Unlock()
				select {
				case s.events <- ev:
				case <-ctx.Done():
					return
				}
			case '1':
				select {
				case s.events <- sioEvent{Err: errors.New("the server disconnected the namespace")}:
				case <-ctx.Done():
				}
				return
			}
		}
	}
}

// emit sends one event with a JSON payload.
func (s *sioConn) emit(ctx context.Context, name string, payload any) error {
	arr := []any{name}
	if payload != nil {
		arr = append(arr, payload)
	}
	b, err := json.Marshal(arr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.sent++
	s.mu.Unlock()
	return s.conn.Write(ctx, websocket.MessageText, append([]byte("42"), b...))
}

func (s *sioConn) close() {
	if s == nil {
		return
	}
	_ = s.conn.Close(websocket.StatusNormalClosure, "done")
	s.cancel()
}
