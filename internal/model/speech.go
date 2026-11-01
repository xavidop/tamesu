package model

import "context"

// Audio is mono 16-bit PCM at a sample rate: what the speech layer exchanges
// with the voice targets. It mirrors audio.PCM without the root module
// depending on the audio module.
type Audio struct {
	Rate    int     `json:"rate"`
	Samples []int16 `json:"-"`
}

// SynthesizeRequest asks for speech for one user turn.
type SynthesizeRequest struct {
	Text string `json:"text"`
	// Voice is a hint such as en-GB-female, or a provider voice name.
	Voice    string `json:"voice,omitempty"`
	Language string `json:"language,omitempty"`
	// Model overrides the configured TTS model.
	Model string `json:"model,omitempty"`
}

// TranscribeRequest asks for the text of an utterance.
type TranscribeRequest struct {
	Audio    Audio
	Language string
	// Model overrides the configured STT model.
	Model string
}

// Transcript is what the STT model heard.
type Transcript struct {
	Text string `json:"text"`
	// Confidence is 0..1 when the provider reports one.
	Confidence float64 `json:"confidence,omitempty"`
	TraceID    string  `json:"trace_id,omitempty"`
}

// Speech is the speech layer of audio mode: text to speech for the simulated
// user and speech to text for the agent. The llm module implements it.
type Speech interface {
	Synthesize(ctx context.Context, req SynthesizeRequest) (Audio, error)
	Transcribe(ctx context.Context, req TranscribeRequest) (Transcript, error)
}
