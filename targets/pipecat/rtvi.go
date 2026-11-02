package pipecat

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// The RTVI messages the target speaks: JSON objects carried in a Pipecat
// MessageFrame, labelled rtvi-ai.
const rtviLabel = "rtvi-ai"

// rtviMessage is the envelope of an RTVI message.
type rtviMessage struct {
	ID    string         `json:"id,omitempty"`
	Label string         `json:"label"`
	Type  string         `json:"type"`
	Data  map[string]any `json:"data,omitempty"`
}

var rtviSeq atomic.Int64

// newRTVI builds a client message with a fresh id.
func newRTVI(typ string, data map[string]any) rtviMessage {
	return rtviMessage{ID: fmt.Sprintf("tamesu-%d", rtviSeq.Add(1)), Label: rtviLabel, Type: typ, Data: data}
}

// parseRTVI reads an RTVI message; ok is false for anything else.
func parseRTVI(data []byte) (rtviMessage, bool) {
	var m rtviMessage
	if err := json.Unmarshal(data, &m); err != nil || m.Type == "" {
		return rtviMessage{}, false
	}
	return m, true
}

// text returns data.text of a bot text message.
func (m rtviMessage) text() string {
	t, _ := m.Data["text"].(string)
	return t
}
