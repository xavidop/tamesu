// Package cartesia is a Genkit model plugin for Cartesia speech: text to
// speech with the Sonic models (cartesia/sonic-3.6 and friends take the
// request's text and answer with an audio media part, a data URL of 16-bit
// PCM) and speech to text with Ink (cartesia/ink-whisper takes an audio media
// part and answers with the transcript). It is an ordinary Genkit plugin,
// usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&cartesia.Cartesia{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("cartesia/sonic-3.6"),
//	    ai.WithPrompt("Hello there"), ai.WithConfig(cartesia.Config{Voice: "<voice id>"}))
//
// Speech goes to POST /tts/bytes as raw PCM; transcription to POST /stt
// (multipart). Every request carries the Cartesia-Version header.
package cartesia

import (
	"bytes"
	"context"
	"encoding/base64"
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

// Provider is the plugin name and the prefix of its models.
const Provider = "cartesia"

// DefaultBaseURL is Cartesia's API.
const DefaultBaseURL = "https://api.cartesia.ai"

// DefaultVersion is the Cartesia-Version header sent with every request.
const DefaultVersion = "2025-04-16"

// DefaultVoice is the voice used when a request names none: a voice from
// Cartesia's public library.
const DefaultVoice = "db6b0ed5-d5d3-463d-ae85-518a07d3c2b4"

// DefaultSampleRate is the PCM rate asked of the TTS models.
const DefaultSampleRate = 24000

// DefaultTTSModels are the text-to-speech models defined when Models is empty.
var DefaultTTSModels = []string{"sonic-3.6", "sonic-3.5", "sonic-3", "sonic-latest"}

// DefaultSTTModels are the speech-to-text models defined when Models is empty.
var DefaultSTTModels = []string{"ink-whisper"}

// Cartesia is the plugin.
type Cartesia struct {
	// APIKey authenticates; CARTESIA_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies).
	BaseURL string
	// Version is the Cartesia-Version header (DefaultVersion when empty).
	Version string
	// HTTPClient overrides the client (default: 60 s timeout).
	HTTPClient *http.Client
	// Models to define, without the provider prefix. Names starting with
	// "ink" are speech to text, the rest text to speech.
	Models []string
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Voice is a Cartesia voice ID (DefaultVoice when empty). Text to speech.
	Voice string `json:"voice,omitempty"`
	// SampleRate of the PCM returned, in Hz (DefaultSampleRate). Text to speech.
	SampleRate int `json:"sample_rate,omitempty"`
	// Language is an ISO 639-1 code (en) or a locale (en-GB) for speech, an
	// ISO 639-1 code for transcription; a locale is cut to its language there.
	Language string `json:"language,omitempty"`
	// Speed is a speaking rate multiplier around 1 (0.6..1.5). Text to speech.
	Speed *float64 `json:"speed,omitempty"`
	// Volume is a loudness multiplier around 1. Text to speech.
	Volume *float64 `json:"volume,omitempty"`
}

// Name implements api.Plugin.
func (c *Cartesia) Name() string { return Provider }

// Init implements api.Plugin.
func (c *Cartesia) Init(context.Context) []api.Action {
	if c.APIKey == "" {
		c.APIKey = os.Getenv("CARTESIA_API_KEY")
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Version == "" {
		c.Version = DefaultVersion
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := c.Models
	if len(models) == 0 {
		models = append(append([]string(nil), DefaultTTSModels...), DefaultSTTModels...)
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, c.model(m))
	}
	return actions
}

// IsSTT reports whether a model name (without the prefix) transcribes.
func IsSTT(model string) bool { return strings.HasPrefix(model, "ink") }

func (c *Cartesia) model(name string) api.Action {
	if IsSTT(name) {
		supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
		return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Cartesia " + name, Supports: supports},
			func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
				return c.transcribe(ctx, name, req, cfg)
			})
	}
	supports := &ai.ModelSupports{Media: false, Multiturn: false, Output: []string{"media"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Cartesia " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return c.synthesize(ctx, name, req, cfg)
		})
}

func (c *Cartesia) synthesize(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if c.APIKey == "" {
		return nil, errors.New("cartesia: no API key (set CARTESIA_API_KEY or Cartesia.APIKey)")
	}
	text := promptText(req)
	if text == "" {
		return nil, errors.New("cartesia: the request has no text to speak")
	}
	voice := cfg.Voice
	if voice == "" {
		voice = DefaultVoice
	}
	rate := cfg.SampleRate
	if rate <= 0 {
		rate = DefaultSampleRate
	}
	body := map[string]any{
		"model_id":      model,
		"transcript":    text,
		"voice":         map[string]any{"mode": "id", "id": voice},
		"output_format": map[string]any{"container": "raw", "encoding": "pcm_s16le", "sample_rate": rate},
	}
	if cfg.Language != "" {
		body["language"] = cfg.Language
	}
	gen := map[string]any{}
	if cfg.Speed != nil {
		gen["speed"] = *cfg.Speed
	}
	if cfg.Volume != nil {
		gen["volume"] = *cfg.Volume
	}
	if len(gen) > 0 {
		body["generation_config"] = gen
	}
	payload, _ := json.Marshal(body)
	httpReq, err := c.request(ctx, "/tts/bytes", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	data, err := c.do(httpReq, 64<<20)
	if err != nil {
		return nil, err
	}
	mime := fmt.Sprintf("audio/L16;rate=%d", rate)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewMediaPart(mime, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))),
		Usage: &ai.GenerationUsage{
			InputCharacters:  len(text),
			OutputAudioFiles: 1,
			Custom:           map[string]float64{"audio_seconds": float64(len(data)) / 2 / float64(rate)},
		},
	}, nil
}

// transcript is the part of the batch transcription response tamesu reads.
type transcript struct {
	Text     string  `json:"text"`
	Language string  `json:"language"`
	Duration float64 `json:"duration"`
}

func (c *Cartesia) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if c.APIKey == "" {
		return nil, errors.New("cartesia: no API key (set CARTESIA_API_KEY or Cartesia.APIKey)")
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
	if lang, _, _ := strings.Cut(cfg.Language, "-"); lang != "" {
		_ = w.WriteField("language", strings.ToLower(lang))
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	httpReq, err := c.request(ctx, "/stt", w.FormDataContentType(), &form)
	if err != nil {
		return nil, err
	}
	body, err := c.do(httpReq, 8<<20)
	if err != nil {
		return nil, err
	}
	var out transcript
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("cartesia: decoding the response: %w", err)
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(out.Text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(out.Text),
			Custom:           map[string]float64{"audio_seconds": out.Duration},
		},
	}, nil
}

func (c *Cartesia) request(ctx context.Context, path, contentType string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Cartesia-Version", c.Version)
	req.Header.Set("Content-Type", contentType)
	return req, nil
}

func (c *Cartesia) do(req *http.Request, limit int64) ([]byte, error) {
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cartesia: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("cartesia: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cartesia: %s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func extensionFor(mime string) string {
	switch {
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "mpeg") || strings.Contains(mime, "mp3"):
		return ".mp3"
	case strings.Contains(mime, "ogg"):
		return ".ogg"
	case strings.Contains(mime, "flac"):
		return ".flac"
	case strings.Contains(mime, "webm"):
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
			if p.IsMedia() {
				return decodePart(p)
			}
		}
	}
	return "", nil, errors.New("cartesia: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("cartesia: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("cartesia: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("cartesia: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("cartesia: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
