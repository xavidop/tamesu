package target

import (
	"context"
	"errors"
	"sync"
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

// TranscriptionStreamRequest opens a transcription that is fed as the agent
// speaks, instead of once its turn is over.
type TranscriptionStreamRequest struct {
	// Rate is the sample rate of the samples written.
	Rate     int
	Language string
	// Model overrides the configured STT model.
	Model string
}

// TranscriptionStream receives one agent turn's audio as it arrives. Write
// and Close are called from the target's receive loop, in order.
type TranscriptionStream interface {
	// Write sends the next samples, 16-bit mono PCM at the request's rate.
	Write(ctx context.Context, samples []int16) error
	// Close marks the end of the audio, waits for the transcript and releases
	// the stream. It is called exactly once, also after a failed Write.
	Close(ctx context.Context) (Transcript, error)
}

// StreamingSpeech is a Speech whose STT model can transcribe as it listens.
// The llm module implements it for the providers that stream (Deepgram,
// AssemblyAI, Soniox) and falls back to buffering plus Transcribe for the
// others, so a target always gets a stream from StartTranscription.
type StreamingSpeech interface {
	Speech
	OpenTranscription(ctx context.Context, req TranscriptionStreamRequest) (TranscriptionStream, error)
}

// StartTranscription opens a transcription on sp: a provider stream when sp
// is a StreamingSpeech, else a buffer that calls sp.Transcribe at Close. It
// is how a voice target transcribes an agent turn as the audio arrives. The
// stream's Write never blocks or fails: samples queue up and a goroutine
// forwards them, so a target may call it from its receive loop, under its
// own locks; a transport error surfaces at Close.
func StartTranscription(ctx context.Context, sp Speech, req TranscriptionStreamRequest) (TranscriptionStream, error) {
	ss, ok := sp.(StreamingSpeech)
	if !ok {
		return &bufferedTranscription{sp: sp, req: req}, nil
	}
	inner, err := ss.OpenTranscription(ctx, req)
	if err != nil {
		return nil, err
	}
	return newAsyncTranscription(inner), nil
}

// asyncTranscription forwards queued samples to a stream from its own
// goroutine.
type asyncTranscription struct {
	inner  TranscriptionStream
	mu     sync.Mutex
	queue  [][]int16
	closed bool
	err    error
	wake   chan struct{}
	done   chan struct{}
}

func newAsyncTranscription(inner TranscriptionStream) *asyncTranscription {
	a := &asyncTranscription{inner: inner, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go a.run()
	return a
}

func (a *asyncTranscription) run() {
	defer close(a.done)
	for {
		a.mu.Lock()
		queue, closed, failed := a.queue, a.closed, a.err != nil
		a.queue = nil
		a.mu.Unlock()
		for _, samples := range queue {
			if failed {
				break
			}
			if err := a.inner.Write(context.Background(), samples); err != nil {
				a.mu.Lock()
				a.err = err
				a.mu.Unlock()
				failed = true
			}
		}
		if closed && len(queue) == 0 {
			return
		}
		if len(queue) == 0 {
			<-a.wake
		}
	}
}

func (a *asyncTranscription) Write(_ context.Context, samples []int16) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	if a.closed {
		return errors.New("target: the transcription is closed")
	}
	a.queue = append(a.queue, append([]int16(nil), samples...))
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}

func (a *asyncTranscription) Close(ctx context.Context) (Transcript, error) {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
	select {
	case <-a.done:
	case <-ctx.Done():
		// The forwarder is stuck in a Write; the provider's Close releases it.
	}
	tr, err := a.inner.Close(ctx)
	a.mu.Lock()
	werr := a.err
	a.mu.Unlock()
	if err == nil && werr != nil {
		err = werr
	}
	return tr, err
}

// bufferedTranscription keeps the samples and transcribes them at Close.
type bufferedTranscription struct {
	sp      Speech
	req     TranscriptionStreamRequest
	samples []int16
}

func (b *bufferedTranscription) Write(_ context.Context, samples []int16) error {
	b.samples = append(b.samples, samples...)
	return nil
}

func (b *bufferedTranscription) Close(ctx context.Context) (Transcript, error) {
	if len(b.samples) == 0 {
		return Transcript{}, nil
	}
	return b.sp.Transcribe(ctx, TranscribeRequest{Audio: Audio{Rate: b.req.Rate, Samples: b.samples}, Language: b.req.Language, Model: b.req.Model})
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
