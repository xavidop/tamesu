package assert

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPState(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		switch r.URL.Path {
		case "/orders/42":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"42","status":"refunded","amount":30}`))
		case "/missing":
			http.Error(w, "no such order", http.StatusNotFound)
		case "/text":
			_, _ = w.Write([]byte("order 42 refunded"))
		}
	}))
	defer srv.Close()
	r := &RunResult{}
	str := func(s string) *string { return &s }
	code := func(c int) *int { return &c }

	a, err := HTTPState(HTTPCheck{URL: srv.URL + "/orders/42", JSONPath: "$.status", Equals: "refunded", Headers: map[string]string{"Authorization": "Bearer t"}, Client: srv.Client()})
	require.NoError(t, err)
	assert.Equal(t, "http GET "+srv.URL+"/orders/42", a.Name())
	res := a.Evaluate(context.Background(), r)
	assert.True(t, res.Pass, res.Reason)
	assert.Equal(t, "Bearer t", got.Header.Get("Authorization"))
	assert.Equal(t, http.MethodGet, got.Method)
	require.Len(t, res.Evidence, 2)
	assert.Contains(t, res.Evidence[0], "→ 200 OK")
	assert.Contains(t, res.Evidence[1], `"status":"refunded"`)

	a, _ = HTTPState(HTTPCheck{URL: srv.URL + "/orders/42", JSONPath: "$.amount", Equals: 31, Client: srv.Client()})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Equal(t, "$.amount = 30, want 31", res.Reason)

	a, _ = HTTPState(HTTPCheck{URL: srv.URL + "/orders/42", JSONPath: "$.shipped", Client: srv.Client()})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "$.shipped is absent")

	a, _ = HTTPState(HTTPCheck{URL: srv.URL + "/missing", Client: srv.Client()})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "returned 404 Not Found")

	a, _ = HTTPState(HTTPCheck{URL: srv.URL + "/missing", Status: code(404), Contains: str("no such order"), Client: srv.Client()})
	res = a.Evaluate(context.Background(), r)
	assert.True(t, res.Pass, "an expected 404 passes")

	a, _ = HTTPState(HTTPCheck{URL: srv.URL + "/text", Method: "post", Body: map[string]any{"id": 42}, Regex: str(`order \d+ refunded`), Client: srv.Client()})
	res = a.Evaluate(context.Background(), r)
	assert.True(t, res.Pass, res.Reason)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"id":42}`, body)

	a, _ = HTTPState(HTTPCheck{URL: srv.URL + "/text", Contains: str("cancelled"), Client: srv.Client()})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, `does not contain "cancelled"`)

	a, _ = HTTPState(HTTPCheck{URL: srv.URL + "/text", JSONPath: "$.x", Client: srv.Client()})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "not JSON")

	a, _ = HTTPState(HTTPCheck{URL: "http://127.0.0.1:1/down", Timeout: time.Second})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "GET http://127.0.0.1:1/down:")

	_, err = HTTPState(HTTPCheck{URL: srv.URL, JSONPath: "$["})
	assert.Error(t, err)
	_, err = HTTPState(HTTPCheck{URL: srv.URL, Regex: str("(")})
	assert.Error(t, err)
}

func TestCommandState(t *testing.T) {
	r := &RunResult{}
	str := func(s string) *string { return &s }
	code := func(c int) *int { return &c }

	a, err := CommandState(CommandCheck{Run: "echo order 42 refunded", Contains: str("refunded"), Regex: str(`order \d+`)})
	require.NoError(t, err)
	assert.Equal(t, `command "echo order 42 refunded"`, a.Name())
	res := a.Evaluate(context.Background(), r)
	assert.True(t, res.Pass, res.Reason)
	assert.Equal(t, []string{"exit 0", "order 42 refunded"}, res.Evidence)

	a, _ = CommandState(CommandCheck{Run: "echo nope; exit 3"})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Equal(t, "the command exited with 3, want 0", res.Reason)

	a, _ = CommandState(CommandCheck{Run: "echo nope; exit 3", Exit: code(3), Contains: str("yes")})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, `does not contain "yes"`)

	a, _ = CommandState(CommandCheck{Run: "echo $TAMESU_CHECK", Env: []string{"TAMESU_CHECK=set"}, Contains: str("set")})
	res = a.Evaluate(context.Background(), r)
	assert.True(t, res.Pass, res.Reason)

	a, _ = CommandState(CommandCheck{Run: "sleep 5", Timeout: 100 * time.Millisecond})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "did not finish within 100ms")

	_, err = CommandState(CommandCheck{Run: "  "})
	assert.Error(t, err)
	_, err = CommandState(CommandCheck{Run: "true", Regex: str("(")})
	assert.Error(t, err)
}
