package metrics

import (
	"fmt"
	"sort"
)

// Built-in metric names. Latencies are milliseconds; ratios are 0 to 1.
const (
	TurnLatencyMs      = "turn_latency_ms"        // rest, livekit, sip: user turn sent → agent turn complete
	TimeToFirstTokenMs = "time_to_first_token_ms" // rest (sse/ws): user turn sent → first delta
	TimeToFirstAudioMs = "time_to_first_audio_ms" // livekit audio, sip: user speech end → agent speech start
	InterruptionStopMs = "interruption_stop_ms"   // livekit audio, sip: user talks over → agent audio stops
	TalkOverRatio      = "talk_over_ratio"        // livekit audio, sip: overlap / agent speech
	SilenceGapMaxMs    = "silence_gap_max_ms"     // livekit audio, sip: longest dead air while a reply was expected
	CallSetupMs        = "call_setup_ms"          // sip: dial (or trigger) → active
	RingMs             = "ring_ms"                // sip: ringing → answered
	GreetingMs         = "greeting_ms"            // sip: active → agent's first speech
	CallDurationMs     = "call_duration_ms"       // sip: active → hangup
	STTWordErrorRate   = "stt_word_error_rate"    // livekit audio, sip: agent's transcript of the caller vs. what tamesu said
	Turns              = "turns"                  // conversational: number of agent turns
	// HandoffTurn is the agent turn on which the agent handed the conversation to a person.
	HandoffTurn = "handoff_turn"
	GoalSteps   = "goal_steps"   // browser: agent steps per goal
	LLMCostUSD  = "llm_cost_usd" // all: tamesu's own model spend
	LLMTokens   = "llm_tokens"   // all: tamesu's own tokens
)

// Percentiles are the summaries published for per-turn metrics.
var Percentiles = []int{50, 95}

// Percentile names a percentile of a per-turn metric: turn_latency_ms_p95.
func Percentile(name string, p int) string { return fmt.Sprintf("%s_p%d", name, p) }

// Max names the maximum of a per-turn metric: turn_latency_ms_max.
func Max(name string) string { return name + "_max" }

// Summarize adds the p50, p95 and max of a per-turn series to out under the
// metric's name, plus the raw name as the mean. An empty series adds nothing.
func Summarize(out map[string]float64, name string, series []float64) {
	if len(series) == 0 {
		return
	}
	sorted := append([]float64(nil), series...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	out[name] = sum / float64(len(sorted))
	for _, p := range Percentiles {
		out[Percentile(name, p)] = percentile(sorted, p)
	}
	out[Max(name)] = sorted[len(sorted)-1]
}

// percentile uses the nearest-rank method on a sorted series.
func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
