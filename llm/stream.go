package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"

	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/llm/plugins/speechstream"
)

var _ model.StreamingSpeech = (*Client)(nil)

// streamersOf collects the plugins that transcribe a stream, by the provider
// prefix of their models.
func streamersOf(plugins []api.Plugin) map[string]speechstream.Transcriber {
	out := map[string]speechstream.Transcriber{}
	for _, p := range plugins {
		if s, ok := p.(speechstream.Transcriber); ok {
			out[providerOfPlugin(p.Name())] = s
		}
	}
	return out
}

// providerOfPlugin maps a plugin name to the provider prefix of its models:
// the two differ where Genkit's own plugin already owns the name.
func providerOfPlugin(name string) string {
	switch name {
	case "openai-speech":
		return "openai"
	case "googlespeech":
		return "google"
	}
	return name
}

// OpenTranscription starts transcribing an agent turn as its audio arrives.
// With a provider that streams (Deepgram, AssemblyAI, Soniox) the samples go
// out as they are written and Close only waits for the last words; with any
// other STT model the samples are kept and transcribed at Close through the
// transcribe flow, as Transcribe does. A stream that fails mid-turn falls
// back to that too, with the audio it had received, so a turn is never lost
// to a dropped connection. The usage and cost of a streamed turn are
// recorded like a batch call's.
func (c *Client) OpenTranscription(ctx context.Context, req model.TranscriptionStreamRequest) (model.TranscriptionStream, error) {
	if c.overBudget() {
		return nil, ErrBudgetExceeded
	}
	modelRef := req.Model
	if modelRef == "" {
		modelRef = c.cfg.Speech.STT.Model
	}
	if modelRef == "" {
		return nil, ErrNoSpeechModel
	}
	if req.Rate <= 0 {
		return nil, fmt.Errorf("llm: transcription stream: the sample rate is %d", req.Rate)
	}
	lang := req.Language
	if lang == "" {
		lang = c.cfg.Speech.STT.Language
	}
	t := &clientTranscription{c: c, model: modelRef, lang: lang, rate: req.Rate}
	if s := c.streamers[providerOf(modelRef)]; s != nil {
		_, name, _ := strings.Cut(modelRef, "/")
		inner, err := s.OpenTranscription(ctx, speechstream.Options{Model: name, Rate: req.Rate, Language: lang})
		switch {
		case err == nil:
			t.inner = inner
		case errors.Is(err, speechstream.ErrUnsupported):
			// The model has no streaming path: buffer and transcribe at Close.
		case ctx.Err() != nil:
			return nil, err
		default:
			c.logger.Warn("llm: the transcription stream did not open; the turn's audio is transcribed once it is over", "model", modelRef, "err", err)
			c.countFallback()
		}
	}
	return t, nil
}

// clientTranscription is one open transcription: the provider's stream when
// there is one, and always the buffered samples for the fallback.
type clientTranscription struct {
	c           *Client
	model, lang string
	rate        int
	inner       speechstream.Transcription
	buf         []int16
	err         error
}

func (t *clientTranscription) Write(ctx context.Context, samples []int16) error {
	t.buf = append(t.buf, samples...)
	if t.inner == nil || t.err != nil {
		return nil
	}
	if err := t.inner.Write(ctx, samples); err != nil {
		// Close falls back to the buffer; nothing for the caller to do.
		t.err = err
	}
	return nil
}

func (t *clientTranscription) Close(ctx context.Context) (model.Transcript, error) {
	if len(t.buf) == 0 {
		// Nothing was heard: release the stream, bill nothing.
		if t.inner != nil {
			_, _ = t.inner.Close(ctx)
		}
		return model.Transcript{}, nil
	}
	if t.inner != nil {
		res, err := t.inner.Close(ctx)
		if err == nil {
			err = t.err
		}
		if err == nil {
			usage := &ai.GenerationUsage{InputAudioFiles: 1, OutputCharacters: len(res.Text), Custom: map[string]float64{"audio_seconds": res.AudioSeconds}}
			if res.Confidence > 0 {
				usage.Custom["confidence"] = res.Confidence
			}
			if err := t.c.record(t.model, &ai.ModelResponse{Usage: usage}, nil); err != nil {
				return model.Transcript{}, err
			}
			return model.Transcript{Text: strings.TrimSpace(res.Text), Confidence: res.Confidence}, nil
		}
		if ctx.Err() != nil {
			return model.Transcript{}, err
		}
		t.c.logger.Warn("llm: the transcription stream failed; the turn's audio is transcribed once instead", "model", t.model, "err", err)
		t.c.countFallback()
	}
	return t.c.Transcribe(ctx, model.TranscribeRequest{Audio: model.Audio{Rate: t.rate, Samples: t.buf}, Language: t.lang, Model: t.model})
}
