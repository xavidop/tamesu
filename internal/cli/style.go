package cli

import (
	"fmt"
	"io"

	"github.com/xavidop/tamesu/report"
)

// table starts an aligned table on w with a bold header row; cells may be
// coloured and still line up.
func table(w io.Writer, st report.Style, header ...string) *report.Table {
	return st.NewTable(w, header...)
}

// counts words passed, failed and errors, colouring what is not zero.
func counts(st report.Style, passed, failed, errored int) string {
	paint := func(n int, word string, f func(string) string) string {
		text := fmt.Sprintf("%d %s", n, word)
		if n > 0 {
			return f(text)
		}
		return text
	}
	return paint(passed, "passed", st.Pass) + ", " + paint(failed, "failed", st.Fail) + ", " + paint(errored, "errors", st.Fail)
}
