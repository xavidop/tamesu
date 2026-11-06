// Package speechstream is the contract between a speech-to-text plugin that
// transcribes audio as it arrives and the code that feeds it: tamesu's speech
// layer, or any Go program that wants a provider's streaming transcription
// without the provider's SDK.
//
// Genkit models take a whole request and answer once, which suits a recorded
// utterance but not a live one: the caller would hold the audio until the
// speaker stops and only then pay the round trip. A plugin whose provider
// streams (Deepgram, AssemblyAI, Soniox) implements Transcriber next to its
// Genkit model actions. Callers write samples as they are heard and read the
// transcript at Close, so the provider has heard almost everything by the time
// the utterance ends.
//
//	t, err := plugin.OpenTranscription(ctx, speechstream.Options{Model: "nova-3", Rate: 16000, Language: "en"})
//	for frame := range frames {
//		if err := t.Write(ctx, frame); err != nil { ... }
//	}
//	result, err := t.Close(ctx)
//
// A plugin returns ErrUnsupported from OpenTranscription for a model that has
// no streaming path, before touching the network, so the caller can fall back
// to the model's Genkit action with the buffered audio. This module has no
// dependencies, so a plugin that implements it stays as light as it was.
package speechstream

import (
	"context"
	"errors"
)

// ErrUnsupported says the model has no streaming path; the caller should
// transcribe the buffered audio through the plugin's Genkit model instead.
var ErrUnsupported = errors.New("speechstream: this model does not transcribe a stream")

// Options open one transcription.
type Options struct {
	// Model is the model name without the provider prefix (nova-3,
	// universal, stt-rt-v3).
	Model string
	// Rate is the sample rate of the 16-bit mono PCM that Write receives.
	Rate int
	// Language is a BCP-47 tag (en, en-GB, es-ES). Empty leaves the
	// provider's default or its language detection in charge.
	Language string
}

// Result is the transcript of everything written before Close.
type Result struct {
	Text string
	// Confidence is 0 to 1 when the provider reports one, else 0.
	Confidence float64
	// AudioSeconds is the duration of the audio written, which is what the
	// provider bills.
	AudioSeconds float64
}

// Transcription is one open stream. Write and Close are called from one
// goroutine; an implementation receives the provider's messages on another.
type Transcription interface {
	// Write sends the next samples, 16-bit mono PCM at Options.Rate, in
	// order. An error means the stream is dead; Close still has to be called
	// to release it, and reports the error again.
	Write(ctx context.Context, samples []int16) error
	// Close tells the provider the audio is over, waits for the final
	// transcript and releases the connection, whatever happened before. The
	// ctx bounds the wait.
	Close(ctx context.Context) (Result, error)
}

// Transcriber is a speech-to-text plugin that can transcribe a stream.
type Transcriber interface {
	// OpenTranscription connects for one utterance, or returns
	// ErrUnsupported when opts.Model has no streaming path.
	OpenTranscription(ctx context.Context, opts Options) (Transcription, error)
}
