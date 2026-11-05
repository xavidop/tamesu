// Package openaispeech is a Genkit model plugin for OpenAI's speech models,
// which Genkit's own OpenAI plugin leaves out: text to speech with
// openai/gpt-4o-mini-tts, openai/tts-1 and openai/tts-1-hd (the request's
// text comes back as an audio media part, a data URL of 16-bit PCM at 24 kHz)
// and speech to text with openai/gpt-4o-transcribe, openai/gpt-4o-mini-transcribe
// and openai/whisper-1 (an audio media part in a user message comes back as
// the transcript). It is an ordinary Genkit plugin, usable in any Genkit Go
// app next to the chat plugin, since its models keep the openai/ prefix while
// the plugin itself is named openai-speech:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&openai.OpenAI{APIKey: key}, &openaispeech.OpenAISpeech{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("openai/gpt-4o-mini-tts"),
//	    ai.WithPrompt("Hello there"), ai.WithConfig(openaispeech.Config{Voice: "nova"}))
//
// Speech goes to POST /v1/audio/speech, transcription to POST
// /v1/audio/transcriptions; the audio duration is reported in the usage's
// Custom map (audio_seconds).
package openaispeech

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
)

// Provider is the prefix of the models: the same openai/ as the chat models.
const Provider = "openai"

// PluginName is the plugin's name in the Genkit registry, distinct from the
// chat plugin's "openai" so both can be initialised together.
const PluginName = "openai-speech"

// DefaultBaseURL is OpenAI's API.
const DefaultBaseURL = "https://api.openai.com"

// DefaultTTSModels are the text-to-speech models defined when Models is empty.
var DefaultTTSModels = []string{"gpt-4o-mini-tts", "tts-1", "tts-1-hd"}

// DefaultSTTModels are the speech-to-text models defined when Models is empty.
var DefaultSTTModels = []string{"gpt-4o-transcribe", "gpt-4o-mini-transcribe", "whisper-1"}

// DefaultVoice is used when a request names none.
const DefaultVoice = "alloy"

// Voices are the built-in voices of the TTS models.
var Voices = []string{"alloy", "ash", "ballad", "coral", "echo", "fable", "onyx", "nova", "sage", "shimmer", "verse", "marin", "cedar"}

// PCMRate is the sample rate of the pcm response format.
const PCMRate = 24000

// OpenAISpeech is the plugin.
type OpenAISpeech struct {
	// APIKey authenticates; OPENAI_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies, Azure OpenAI
	// gateways that speak the same API).
	BaseURL string
	// HTTPClient overrides the client (default: 60 s timeout).
	HTTPClient *http.Client
	// Models to define, without the provider prefix. Names containing
	// "transcribe" or starting with "whisper" are speech to text, the rest
	// text to speech.
	Models []string
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Voice is one of Voices (default alloy). Text to speech.
	Voice string `json:"voice,omitempty"`
	// Instructions steer the delivery (tone, pace, accent); gpt-4o-mini-tts
	// only. Text to speech.
	Instructions string `json:"instructions,omitempty"`
	// Speed is 0.25..4.0 around 1. Text to speech.
	Speed *float64 `json:"speed,omitempty"`
	// ResponseFormat is pcm by default; mp3, opus, aac, flac and wav are
	// passed through, but only pcm and wav are decoded by tamesu. Text to
	// speech.
	ResponseFormat string `json:"response_format,omitempty"`
	// Language is an ISO 639-1 code (a locale is cut to its language); it
	// improves accuracy and latency. Speech to text.
	Language string `json:"language,omitempty"`
	// Prompt gives the transcriber context: names, terms, the previous turn.
	// Speech to text.
	Prompt string `json:"prompt,omitempty"`
}

// Name implements api.Plugin.
func (o *OpenAISpeech) Name() string { return PluginName }

// Init implements api.Plugin.
func (o *OpenAISpeech) Init(context.Context) []api.Action {
	if o.APIKey == "" {
		o.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := o.Models
	if len(models) == 0 {
		models = append(append([]string(nil), DefaultTTSModels...), DefaultSTTModels...)
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, o.model(m))
	}
	return actions
}

// IsSTT reports whether a model name (without the prefix) transcribes.
func IsSTT(model string) bool {
	return strings.Contains(model, "transcribe") || strings.HasPrefix(model, "whisper")
}

func (o *OpenAISpeech) model(name string) api.Action {
	if IsSTT(name) {
		supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
		return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "OpenAI " + name, Supports: supports},
			func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
				return o.transcribe(ctx, name, req, cfg)
			})
	}
	supports := &ai.ModelSupports{Media: false, Multiturn: false, Output: []string{"media"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "OpenAI " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return o.synthesize(ctx, name, req, cfg)
		})
}

func (o *OpenAISpeech) synthesize(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if o.APIKey == "" {
		return nil, errors.New("openai: no API key (set OPENAI_API_KEY or OpenAISpeech.APIKey)")
	}
	text := promptText(req)
	if text == "" {
		return nil, errors.New("openai: the request has no text to speak")
	}
	voice := cfg.Voice
	if voice == "" {
		voice = DefaultVoice
	}
	format := cfg.ResponseFormat
	if format == "" {
		format = "pcm"
	}
	body := map[string]any{"model": model, "input": text, "voice": voice, "response_format": format}
	if cfg.Instructions != "" {
		body["instructions"] = cfg.Instructions
	}
	if cfg.Speed != nil {
		body["speed"] = *cfg.Speed
	}
	payload, _ := json.Marshal(body)
	httpReq, err := o.request(ctx, "/v1/audio/speech", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	data, err := o.do(httpReq, 64<<20)
	if err != nil {
		return nil, err
	}
	mime, rate := mimeFor(format)
	usage := &ai.GenerationUsage{InputCharacters: len(text), OutputAudioFiles: 1, Custom: map[string]float64{}}
	if rate > 0 {
		usage.Custom["audio_seconds"] = float64(len(data)) / 2 / float64(rate)
	} else if format == "wav" {
		usage.Custom["audio_seconds"] = wavSeconds(data)
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewMediaPart(mime, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))),
		Usage:        usage,
	}, nil
}

// mimeFor maps a response format to a content type and PCM rate (0 when the
// format is not raw PCM).
func mimeFor(format string) (string, int) {
	switch format {
	case "pcm":
		return fmt.Sprintf("audio/L16;rate=%d", PCMRate), PCMRate
	case "mp3":
		return "audio/mpeg", 0
	case "opus":
		return "audio/ogg", 0
	case "aac":
		return "audio/aac", 0
	case "flac":
		return "audio/flac", 0
	case "wav":
		return "audio/wav", 0
	}
	return "application/octet-stream", 0
}

// transcription is the part of the transcription response tamesu reads: the
// text, whisper-1's verbose duration and the newer models' usage.
type transcription struct {
	Text     string  `json:"text"`
	Duration float64 `json:"duration"`
	Usage    struct {
		Type    string  `json:"type"`
		Seconds float64 `json:"seconds"`
	} `json:"usage"`
}

func (o *OpenAISpeech) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if o.APIKey == "" {
		return nil, errors.New("openai: no API key (set OPENAI_API_KEY or OpenAISpeech.APIKey)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	part, err := w.CreateFormFile("file", "audio"+extensionFor(mime))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	_ = w.WriteField("model", model)
	// Only whisper-1 has verbose_json (with the duration); the gpt-4o
	// transcribers answer json with a usage block.
	format := "json"
	if strings.HasPrefix(model, "whisper") {
		format = "verbose_json"
	}
	_ = w.WriteField("response_format", format)
	if lang, _, _ := strings.Cut(cfg.Language, "-"); lang != "" {
		_ = w.WriteField("language", strings.ToLower(lang))
	}
	if cfg.Prompt != "" {
		_ = w.WriteField("prompt", cfg.Prompt)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	httpReq, err := o.request(ctx, "/v1/audio/transcriptions", w.FormDataContentType(), &form)
	if err != nil {
		return nil, err
	}
	body, err := o.do(httpReq, 8<<20)
	if err != nil {
		return nil, err
	}
	var out transcription
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("openai: decoding the response: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	seconds := out.Duration
	if seconds == 0 {
		seconds = out.Usage.Seconds
	}
	if seconds == 0 && isWAV(mime) {
		seconds = wavSeconds(data)
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": seconds},
		},
	}, nil
}

func (o *OpenAISpeech) request(ctx context.Context, path, contentType string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", contentType)
	return req, nil
}

func (o *OpenAISpeech) do(req *http.Request, limit int64) ([]byte, error) {
	res, err := o.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("openai: %s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func isWAV(mime string) bool {
	mt, _, _ := strings.Cut(strings.ToLower(mime), ";")
	return mt == "audio/wav" || mt == "audio/x-wav" || mt == "audio/wave"
}

// wavSeconds reads a WAV header for the duration of its data chunk; 0 when
// the bytes are not a WAV it understands.
func wavSeconds(data []byte) float64 {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0
	}
	var bytesPerSecond uint32
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		switch id {
		case "fmt ":
			if pos+16 > len(data) {
				return 0
			}
			bytesPerSecond = binary.LittleEndian.Uint32(data[pos+8 : pos+12])
		case "data":
			if bytesPerSecond == 0 {
				return 0
			}
			if pos+size > len(data) {
				size = len(data) - pos
			}
			return float64(size) / float64(bytesPerSecond)
		}
		pos += size + size%2
	}
	return 0
}

// extensionFor names the uploaded file for its type; OpenAI reads the format
// from the extension.
func extensionFor(mime string) string {
	mt, _, _ := strings.Cut(strings.ToLower(mime), ";")
	switch mt {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/webm", "video/webm":
		return ".webm"
	}
	return ".wav"
}

func promptText(req *ai.ModelRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == ai.RoleSystem {
			continue
		}
		for _, p := range m.Content {
			if p.IsText() {
				b.WriteString(p.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// audioPart finds the audio in the request: the last media part, as a data
// URL (data:<mime>;base64,<data>) or raw base64 with the part's content type.
func audioPart(req *ai.ModelRequest) (mime string, data []byte, err error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, p := range req.Messages[i].Content {
			if !p.IsMedia() {
				continue
			}
			return decodePart(p)
		}
	}
	return "", nil, errors.New("openai: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("openai: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("openai: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("openai: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("openai: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
