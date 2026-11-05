package report

import (
	"bytes"
	"os"
	"testing"
	"time"

	tassert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/assert"
)

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestColorEnabled(t *testing.T) {
	var buf bytes.Buffer
	tassert.False(t, ColorEnabled("auto", &buf, nil), "a buffer is not a terminal")
	tassert.True(t, ColorEnabled("always", &buf, nil))
	tassert.False(t, ColorEnabled("never", &buf, envOf(map[string]string{"FORCE_COLOR": "1"})), "never wins over FORCE_COLOR")
	tassert.True(t, ColorEnabled("auto", &buf, envOf(map[string]string{"FORCE_COLOR": "1"})), "FORCE_COLOR colours a pipe")
	tassert.False(t, ColorEnabled("auto", &buf, envOf(map[string]string{"FORCE_COLOR": "1", "NO_COLOR": "1"})), "NO_COLOR wins over FORCE_COLOR")
	tassert.False(t, ColorEnabled("auto", &buf, envOf(map[string]string{"FORCE_COLOR": "0"})))

	// A regular file is not a character device either.
	f, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	tassert.False(t, ColorEnabled("auto", f, nil))
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer func() { _ = tty.Close() }()
		tassert.True(t, ColorEnabled("auto", tty, nil), "a terminal is coloured")
		tassert.False(t, ColorEnabled("auto", tty, envOf(map[string]string{"NO_COLOR": "1"})))
		tassert.False(t, ColorEnabled("auto", tty, envOf(map[string]string{"TERM": "dumb"})))
	}
}

func TestStyleWrapsAndStrips(t *testing.T) {
	off := NewStyle(false)
	tassert.Equal(t, "PASS", off.Pass("PASS"))
	tassert.False(t, off.Enabled())
	on := NewStyle(true)
	tassert.Equal(t, "\x1b[1m\x1b[32mPASS\x1b[0m", on.Pass("PASS"))
	tassert.Equal(t, "\x1b[1m\x1b[31mFAIL\x1b[0m", on.Fail("FAIL"))
	tassert.Equal(t, "\x1b[33mQUAR\x1b[0m", on.Warn("QUAR"))
	tassert.Equal(t, "\x1b[2mskip\x1b[0m", on.Dim("skip"))
	tassert.Equal(t, "", on.Fail(""), "nothing to colour")
	tassert.Equal(t, "PASS FAIL QUAR", Strip(on.Pass("PASS")+" "+on.Fail("FAIL")+" "+on.Warn("QUAR")))
}

func TestConsoleColours(t *testing.T) {
	var plain, coloured bytes.Buffer
	outcome := &TestOutcome{Suite: "s", Name: "t", Target: "api", Repeat: 1, Passed: false, Runs: []*RunOutcome{{Index: 1, Duration: time.Second,
		Assertions: []assert.Result{{Name: "contains", Pass: false, Reason: "missing 30 days"}}}}}
	NewConsole(&plain, false).TestFinished(outcome)
	NewConsole(&coloured, false).WithColor(true).TestFinished(outcome)
	tassert.NotContains(t, plain.String(), "\x1b[", "no colours by default")
	tassert.Contains(t, coloured.String(), "\x1b[1m\x1b[31mFAIL \x1b[0m")
	tassert.Equal(t, plain.String(), Strip(coloured.String()), "colours change nothing but the codes")
}

func TestTableKeepsColumnsAligned(t *testing.T) {
	render := func(st Style) string {
		var buf bytes.Buffer
		tb := st.NewTable(&buf, "NAME", "STATE", "NOTE")
		tb.Row("refund", st.Fail("failed"), "x")
		tb.Row("a much longer name", st.Pass("passed"), "y")
		require.NoError(t, tb.Flush())
		return buf.String()
	}
	plain, coloured := render(NewStyle(false)), render(NewStyle(true))
	tassert.Equal(t, "NAME                STATE   NOTE\nrefund              failed  x\na much longer name  passed  y\n", plain)
	tassert.Contains(t, coloured, "\x1b[1m\x1b[31mfailed\x1b[0m")
	tassert.Equal(t, plain, Strip(coloured), "the columns line up the same with colours")
}
