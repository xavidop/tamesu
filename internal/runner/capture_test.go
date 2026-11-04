package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

func TestCapturer(t *testing.T) {
	set := map[string]string{}
	c, err := newCapturer([]config.Capture{
		{Name: "order_id", Regex: `order (ORD-\d+)`},
		{Name: "status", JSONPath: "$.status"},
		{Name: "word", Regex: `refund`},
	}, func(name, value string) { set[name] = value }, nil)
	require.NoError(t, err)
	c.turn(target.AgentTurn{Text: "Hello there", Raw: map[string]any{"status": "ok"}})
	c.turn(target.AgentTurn{Text: "Your order ORD-42 is on its way, a refund is possible", Raw: []any{map[string]any{"status": "changed"}}})
	c.turn(target.AgentTurn{Text: "Also order ORD-99"})
	assert.Equal(t, map[string]string{"order_id": "ORD-42", "status": "ok", "word": "refund"}, c.snapshot(), "first match wins, group 1 or the whole match")
	assert.Equal(t, "ORD-42", set["order_id"], "values reach the scope as they are captured")
	assert.Nil(t, (*capturer)(nil).snapshot())
	_, err = newCapturer([]config.Capture{{Name: "bad", Regex: "("}}, nil, nil)
	assert.Error(t, err)
	n, err := newCapturer([]config.Capture{{Name: "n", JSONPath: "$.count"}}, nil, nil)
	require.NoError(t, err)
	n.turn(target.AgentTurn{Raw: map[string]any{"count": 3.0}})
	assert.Equal(t, "3", n.snapshot()["n"], "numbers read as integers when whole")
}

func TestCapturerExtract(t *testing.T) {
	var asked []string
	extract := func(description, text string) (string, bool) {
		asked = append(asked, description+" | "+text)
		if strings.Contains(text, "ORD-") {
			return " ORD-42 ", true
		}
		return "", false
	}
	c, err := newCapturer([]config.Capture{{Name: "order_id", Extract: "the order number the agent mentioned"}}, nil, extract)
	require.NoError(t, err)
	c.turn(target.AgentTurn{Text: ""})
	c.turn(target.AgentTurn{Text: "Hello, how can I help?"})
	c.turn(target.AgentTurn{Text: "Your order ORD-42 is on its way"})
	c.turn(target.AgentTurn{Text: "And ORD-99 too"})
	assert.Equal(t, map[string]string{"order_id": "ORD-42"}, c.snapshot(), "the first turn with the value wins, trimmed")
	assert.Equal(t, []string{"the order number the agent mentioned | Hello, how can I help?", "the order number the agent mentioned | Your order ORD-42 is on its way"}, asked, "empty turns are not sent; captured values are not asked again")

	none, err := newCapturer([]config.Capture{{Name: "x", Extract: "anything"}}, nil, nil)
	require.NoError(t, err)
	none.turn(target.AgentTurn{Text: "ORD-1"})
	assert.Nil(t, none.snapshot(), "no model, no extraction")
}
