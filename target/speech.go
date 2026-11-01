package target

import (
	"context"
	"time"
)

// Audio is mono 16-bit PCM at a sample rate: what the speech layer and the
// voice targets exchange.
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
// user and speech to text for the agent. The llm module implements it; the
// runner hands it to voice targets through RunContext.Speech.
type Speech interface {
	Synthesize(ctx context.Context, req SynthesizeRequest) (Audio, error)
	Transcribe(ctx context.Context, req TranscribeRequest) (Transcript, error)
}

// Voice is how the simulated caller sounds and behaves in audio mode: the
// persona's voice and language plus its behaviors (design.md §5 persona).
type Voice struct {
	// Hint is the persona's voice, such as en-GB-female or a provider voice.
	Hint     string
	Language string
	// InterruptProbability is the chance, per agent turn, that the caller
	// starts talking before the agent finished.
	InterruptProbability float64
	// SilenceMin and SilenceMax bound the pause before each user turn.
	SilenceMin, SilenceMax time.Duration
	// BackgroundNoise is a preset (cafe, street, office, white) or a WAV path
	// mixed under the caller's audio at NoiseSNRdB (default 20).
	BackgroundNoise string
	NoiseSNRdB      float64
	// LineQuality simulates a phone line on the caller's audio: pstn,
	// mobile_poor, voip_good.
	LineQuality string
	// Seed drives the behaviour RNG so a run is repeatable.
	Seed int64
	// VAD names the voice activity detector for the agent's audio: energy
	// (default) or silero.
	VAD string
	// TranscriptSource picks how the agent's words are captured: agent (its
	// own transcription stream), stt (tamesu transcribes its audio) or auto
	// (agent when it publishes one, else stt).
	TranscriptSource string
}
