package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
)

func TestHookRunCommandAndHTTP(t *testing.T) {
	dir := t.TempDir()
	var got struct {
		method, auth, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile("/dev/null")
		_ = b
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		got.method, got.auth, got.body = r.Method, r.Header.Get("Authorization"), string(buf[:n])
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	scope := config.Scope{Vars: map[string]string{"who": "marta"}, Secrets: func(name string) (string, bool) { return "tok-" + name, true }}
	hr := hookRun{stage: "before_each", scope: scope, log: filepath.Join(dir, "hooks.log"),
		env: map[string]string{"TAMESU_TEST": "refund"},
		hooks: []config.Hook{
			{Command: "echo \"$TAMESU_TEST {{vars.who}}\" > " + filepath.Join(dir, "out.txt")},
			{HTTP: &config.HTTPTrigger{URL: srv.URL + "/reset", Method: "PUT", Body: map[string]any{"user": "{{vars.who}}"}, Headers: map[string]string{"Authorization": "Bearer {{secrets.API}}"}}},
		}}
	require.NoError(t, hr.run(context.Background()))
	b, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	require.NoError(t, err)
	assert.Equal(t, "refund marta\n", string(b), "the command sees the environment and the rendered template")
	assert.Equal(t, "PUT", got.method)
	assert.Equal(t, "Bearer tok-API", got.auth, "secrets render in headers")
	assert.JSONEq(t, `{"user":"marta"}`, got.body)
	logb, err := os.ReadFile(filepath.Join(dir, "hooks.log"))
	require.NoError(t, err)
	assert.Contains(t, string(logb), "== before_each[0] command echo")
	assert.Contains(t, string(logb), "201 Created")
	assert.NotContains(t, string(logb), "tok-API", "the rendered secret is not logged")
}

func TestHookRunFailures(t *testing.T) {
	dir := t.TempDir()
	hr := hookRun{stage: "before_all", log: filepath.Join(dir, "hooks.log"), hooks: []config.Hook{{Command: "echo boom >&2; exit 3"}}}
	err := hr.run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before_all hook 0 (command echo boom >&2; exit 3)")
	logb, _ := os.ReadFile(filepath.Join(dir, "hooks.log"))
	assert.Contains(t, string(logb), "boom")
	assert.Contains(t, string(logb), "-- failed")

	slow := hookRun{stage: "before_each", hooks: []config.Hook{{Command: "sleep 5", Timeout: config.Duration(200 * time.Millisecond)}}}
	err = slow.run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusForbidden) }))
	defer srv.Close()
	bad := hookRun{stage: "after_all", hooks: []config.Hook{{HTTP: &config.HTTPTrigger{URL: srv.URL}}}}
	err = bad.run(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "403"), err.Error())
}

func TestHooksOfOrder(t *testing.T) {
	p := config.Project{Hooks: &config.Hooks{BeforeEach: []config.Hook{{Command: "project"}}}}
	s := &config.Suite{Hooks: &config.Hooks{BeforeEach: []config.Hook{{Command: "suite"}}}}
	got := hooksOf(p, s, beforeEach)
	require.Len(t, got, 2)
	assert.Equal(t, "project", got[0].Command)
	assert.Equal(t, "suite", got[1].Command)
	assert.Empty(t, hooksOf(p, nil, afterAll))
}
