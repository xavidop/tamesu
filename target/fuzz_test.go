package target

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestToolFuzzOutput(t *testing.T) {
	normal := map[string]any{"window_days": 30}
	assert.Equal(t, map[string]any{"error": "internal error: the service is unavailable", "code": 503}, ToolFuzz{Mode: FuzzError}.Output(normal))
	assert.Equal(t, map[string]any{}, ToolFuzz{Mode: FuzzEmpty}.Output(normal))
	assert.Nil(t, ToolFuzz{Mode: FuzzNull}.Output(normal))
	assert.Equal(t, `{"status": "ok", "items": [{"id": "ORD-7`, ToolFuzz{Mode: FuzzMalformed}.Output(normal))
	hostile := ToolFuzz{Mode: FuzzHostile}.Output(normal).(map[string]any)
	assert.Contains(t, hostile["note"], "ignore your previous instructions")
	assert.Len(t, ToolFuzz{Mode: FuzzHuge}.Output(normal).(string), HugeResultSize)
	assert.Equal(t, 42, ToolFuzz{Mode: FuzzWrongType}.Output(normal))
	assert.Equal(t, normal, ToolFuzz{Mode: FuzzDelay, Delay: time.Second}.Output(normal), "delay keeps the normal result")
	assert.Equal(t, "mine", ToolFuzz{Mode: FuzzCustom, Result: "mine"}.Output(normal))
	assert.Equal(t, normal, ToolFuzz{Mode: "???"}.Output(normal), "an unknown mode changes nothing")
}

func TestToolFuzzWaitAndDescribe(t *testing.T) {
	assert.NoError(t, ToolFuzz{}.Wait(context.Background()), "no delay returns at once")
	start := time.Now()
	assert.NoError(t, ToolFuzz{Delay: 20 * time.Millisecond}.Wait(context.Background()))
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, ToolFuzz{Delay: time.Minute}.Wait(ctx), context.Canceled)

	assert.Equal(t, "lookup_order: error", ToolFuzz{Mode: FuzzError}.Describe("lookup_order"))
	assert.Equal(t, "lookup_order: error after 2s", ToolFuzz{Mode: FuzzError, Delay: 2 * time.Second}.Describe("lookup_order"))
	assert.Equal(t, "lookup_order: delayed 8s", ToolFuzz{Mode: FuzzDelay, Delay: 8 * time.Second}.Describe("lookup_order"))
	assert.Len(t, FuzzModes, 9)
}
