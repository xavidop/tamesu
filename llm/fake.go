//go:build llmfake

package llm

import (
	"github.com/firebase/genkit/go/core/api"

	"github.com/xavidop/tamesu/llm/llmfake"
)

// A build with -tags llmfake registers the fake model provider, so the whole
// pipeline runs without network or keys. A default build never contains it.
// One fake plugin per process, so tests can swap its script.
var fakeInstance = llmfake.New()

func init() {
	fakePlugin = func() api.Plugin { return fakeInstance }
}
