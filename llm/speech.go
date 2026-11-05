package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"strconv"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/tracing"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/internal/model"
)

// Speech flow names.
const (
	FlowSynthesize = "synthesize"
	FlowTranscribe = "transcribe"
)

// SpeechModel is a speech model with its defaults (design.md §4 speech:).
type SpeechModel struct {
	// Model is a Genkit model reference: googleai/gemini-2.5-flash-preview-tts,
	// elevenlabs/eleven_flash_v2_5, cartesia/sonic-3.6, openai/gpt-4o-mini-tts,
	// deepgram/aura-2, azure/tts, rime/arcana for speech; deepgram/nova-3,
	// cartesia/ink-whisper, soniox/stt-async-v6, openai/gpt-4o-transcribe,
	// elevenlabs/scribe_v1, assemblyai/universal, azure/stt,
	// googleai/gemini-2.5-flash for transcription.
	Model    string
	Language string
	Voice    string
	// Fallback lists the models tried, in order, when Model fails.
	Fallback []string
}

// SpeechConfig configures text to speech and speech to text.
type SpeechConfig struct {
	TTS SpeechModel
	STT SpeechModel
}

// ErrNoSpeechModel is returned when a speech call has no model configured.
var ErrNoSpeechModel = errors.New("llm: no speech model configured (set speech.tts.model / speech.stt.model in tamesu.yaml)")

// synthInput is the synthesize flow's input.
type synthInput struct {
	Text     string `json:"text"`
	Voice    string `json:"voice,omitempty"`
	Language string `json:"language,omitempty"`
	Model    string `json:"model,omitempty"`
}

// synthOutput is the synthesize flow's output: PCM as base64 so traces stay
// JSON.
type synthOutput struct {
	Model   string  `json:"model"`
	Voice   string  `json:"voice"`
	Rate    int     `json:"rate"`
	Seconds float64 `json:"seconds"`
	PCM     []byte  `json:"pcm"`
	TraceID string  `json:"trace_id,omitempty"`
}

type sttInput struct {
	Rate     int    `json:"rate"`
	PCM      []byte `json:"pcm"`
	Language string `json:"language,omitempty"`
	Model    string `json:"model,omitempty"`
}

type sttOutput struct {
	Model      string  `json:"model"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence,omitempty"`
	TraceID    string  `json:"trace_id,omitempty"`
}

var _ model.Speech = (*Client)(nil)

// Synthesize speaks a user turn through the synthesize flow with the
// configured TTS model (or req.Model).
func (c *Client) Synthesize(ctx context.Context, req model.SynthesizeRequest) (model.Audio, error) {
	if c.overBudget() {
		return model.Audio{}, ErrBudgetExceeded
	}
	out, err := c.ttsFlow.Run(ctx, synthInput{Text: req.Text, Voice: req.Voice, Language: req.Language, Model: req.Model})
	if err != nil {
		return model.Audio{}, err
	}
	return model.Audio{Rate: out.Rate, Samples: bytesToPCM(out.PCM)}, nil
}

// Transcribe turns an utterance into text through the transcribe flow with
// the configured STT model (or req.Model).
func (c *Client) Transcribe(ctx context.Context, req model.TranscribeRequest) (model.Transcript, error) {
	if c.overBudget() {
		return model.Transcript{}, ErrBudgetExceeded
	}
	out, err := c.sttFlow.Run(ctx, sttInput{Rate: req.Audio.Rate, PCM: pcmToBytes(req.Audio.Samples), Language: req.Language, Model: req.Model})
	if err != nil {
		return model.Transcript{}, err
	}
	return model.Transcript{Text: out.Text, Confidence: out.Confidence, TraceID: out.TraceID}, nil
}

func (c *Client) synthesize(ctx context.Context, in synthInput) (synthOutput, error) {
	primary := in.Model
	if primary == "" {
		primary = c.cfg.Speech.TTS.Model
	}
	if primary == "" {
		return synthOutput{}, ErrNoSpeechModel
	}
	hint := in.Voice
	if hint == "" {
		hint = c.cfg.Speech.TTS.Voice
	}
	// The voice is resolved per model: a fallback on another provider speaks
	// with that provider's voice of the same description.
	return attempt(ctx, c, "tts", chain(primary, c.cfg.Speech.TTS.Fallback), func(ctx context.Context, modelName string) (synthOutput, error) {
		provider := providerOf(modelName)
		voice := ResolveVoice(provider, hint)
		lang := in.Language
		if lang == "" {
			lang = voice.Language
		}
		if lang == "" {
			lang = c.cfg.Speech.TTS.Language
		}
		resp, err := genkit.Generate(ctx, c.g, ai.WithModelName(modelName), ai.WithPrompt(in.Text), ai.WithConfig(ttsConfig(provider, voice.Name, lang)))
		budgetErr := c.record(modelName, resp, err)
		if err != nil {
			return synthOutput{}, fmt.Errorf("llm: %s with %s: %w", FlowSynthesize, modelName, err)
		}
		pcm, err := audioFromResponse(resp)
		if err != nil {
			return synthOutput{}, fmt.Errorf("llm: %s with %s: %w", FlowSynthesize, modelName, err)
		}
		out := synthOutput{Model: modelName, Voice: voice.Name, Rate: pcm.Rate, Seconds: pcm.Duration().Seconds(), PCM: pcmToBytes(pcm.Samples), TraceID: tracing.SpanTraceInfo(ctx).TraceID}
		return out, budgetErr
	})
}

// ttsConfig is the model config that selects a voice, per provider.
func ttsConfig(provider, voice, lang string) map[string]any {
	switch provider {
	case "googleai", "vertexai":
		return map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": voice}}},
		}
	case "elevenlabs":
		cfg := map[string]any{"voice": voice, "output_format": "pcm_24000"}
		if code, _, _ := strings.Cut(lang, "-"); code != "" {
			cfg["language"] = code
		}
		return cfg
	case "openai":
		return map[string]any{"voice": voice, "response_format": "pcm"}
	case "deepgram":
		return map[string]any{"voice": voice, "sample_rate": 24000}
	case "cartesia":
		cfg := map[string]any{"voice": voice}
		if lang != "" {
			cfg["language"] = lang
		}
		return cfg
	}
	cfg := map[string]any{"voice": voice}
	if lang != "" {
		cfg["language"] = lang
	}
	return cfg
}

func (c *Client) transcribe(ctx context.Context, in sttInput) (sttOutput, error) {
	primary := in.Model
	if primary == "" {
		primary = c.cfg.Speech.STT.Model
	}
	if primary == "" {
		return sttOutput{}, ErrNoSpeechModel
	}
	lang := in.Language
	if lang == "" {
		lang = c.cfg.Speech.STT.Language
	}
	if in.Rate <= 0 || len(in.PCM) == 0 {
		return sttOutput{}, fmt.Errorf("llm: %s: no audio to transcribe", FlowTranscribe)
	}
	var buf bytes.Buffer
	if err := wav.Write(&buf, in.Rate, bytesToPCM(in.PCM)); err != nil {
		return sttOutput{}, err
	}
	dataURL := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	msg := ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL), ai.NewTextPart(transcribePrompt(lang)))
	return attempt(ctx, c, "stt", chain(primary, c.cfg.Speech.STT.Fallback), func(ctx context.Context, modelName string) (sttOutput, error) {
		resp, err := genkit.Generate(ctx, c.g, ai.WithModelName(modelName), ai.WithMessages(msg), ai.WithConfig(sttConfig(providerOf(modelName), lang)))
		budgetErr := c.record(modelName, resp, err)
		if err != nil {
			return sttOutput{}, fmt.Errorf("llm: %s with %s: %w", FlowTranscribe, modelName, err)
		}
		out := sttOutput{Model: modelName, Text: strings.TrimSpace(resp.Text()), TraceID: tracing.SpanTraceInfo(ctx).TraceID}
		if resp.Usage != nil {
			out.Confidence = resp.Usage.Custom["confidence"]
		}
		return out, budgetErr
	})
}

// transcribePrompt is what a general model is told; dedicated STT models
// ignore the text part.
func transcribePrompt(lang string) string {
	p := "Transcribe the speech in this audio verbatim. Reply with the transcript only: no commentary, no quotes, no speaker labels. If there is no speech, reply with an empty string."
	if lang != "" {
		p += " The language is " + lang + "."
	}
	return p
}

func sttConfig(provider, lang string) map[string]any {
	cfg := map[string]any{}
	switch provider {
	case "deepgram", "cartesia", "soniox", "openai", "elevenlabs", "assemblyai", "azure", "google", "speechmatics", "gladia", "fake":
		if lang != "" {
			cfg["language"] = lang
		}
	}
	return cfg
}

// audioFromResponse decodes the response's audio media part (a data URL of
// PCM, audio/L16 or audio/pcm with a rate parameter, or a WAV) to PCM.
func audioFromResponse(resp *ai.ModelResponse) (audio.PCM, error) {
	if resp == nil || resp.Message == nil {
		return audio.PCM{}, errors.New("empty response")
	}
	for _, p := range resp.Message.Content {
		if !p.IsMedia() {
			continue
		}
		return decodeAudioPart(p.ContentType, p.Text)
	}
	return audio.PCM{}, fmt.Errorf("the model returned no audio (text: %s)", short(resp.Text()))
}

func decodeAudioPart(contentType, text string) (audio.PCM, error) {
	mediaType := contentType
	payload := text
	if strings.HasPrefix(text, "data:") {
		meta, data, ok := strings.Cut(text[len("data:"):], ",")
		if !ok {
			return audio.PCM{}, errors.New("malformed audio data URL")
		}
		if m := strings.TrimSuffix(meta, ";base64"); m != "" {
			mediaType = m
		}
		payload = data
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return audio.PCM{}, fmt.Errorf("decoding the audio: %w", err)
	}
	mt, params, _ := mime.ParseMediaType(mediaType)
	switch strings.ToLower(mt) {
	case "audio/l16", "audio/pcm", "audio/x-pcm", "audio/raw":
		rate, _ := strconv.Atoi(params["rate"])
		if rate == 0 {
			return audio.PCM{}, fmt.Errorf("audio type %q has no rate parameter", mediaType)
		}
		pcm := audio.PCM{Rate: rate, Samples: bytesToPCM(raw)}
		if ch, _ := strconv.Atoi(params["channels"]); ch == 2 {
			pcm.Samples = downmix(pcm.Samples)
		}
		return pcm, nil
	case "audio/wav", "audio/x-wav", "audio/wave":
		rate, channels, err := wav.Read(bytes.NewReader(raw))
		if err != nil {
			return audio.PCM{}, err
		}
		return audio.PCM{Rate: rate, Samples: channels[0]}, nil
	}
	return audio.PCM{}, fmt.Errorf("unsupported audio type %q (tamesu decodes PCM and WAV; ask the model for pcm output)", mediaType)
}

func downmix(stereo []int16) []int16 {
	out := make([]int16, len(stereo)/2)
	for i := range out {
		out[i] = int16((int(stereo[2*i]) + int(stereo[2*i+1])) / 2)
	}
	return out
}

// pcmToBytes and bytesToPCM are little-endian 16-bit.
func pcmToBytes(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		b[2*i] = byte(v)
		b[2*i+1] = byte(uint16(v) >> 8)
	}
	return b
}

func bytesToPCM(b []byte) []int16 {
	s := make([]int16, len(b)/2)
	for i := range s {
		s[i] = int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
	}
	return s
}
