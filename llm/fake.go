//go:build llmfake

package llm

import (
	"github.com/firebase/genkit/go/core/api"

	"github.com/xavidop/tamesu/llm/llmfake"
)

// A build with -tags llmfake registers the fake model provider, so the whole
// pipeline runs without network or keys. A default build never contains it.
func init() {
	fakePlugin = func() api.Plugin { return llmfake.New() }
}
