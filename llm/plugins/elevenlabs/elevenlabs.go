// Package elevenlabs is a Genkit model plugin for ElevenLabs speech: text to
// speech with the Eleven models (elevenlabs/eleven_flash_v2_5 and friends
// take the request's text and answer with an audio media part, a data URL of
// 16-bit PCM, audio/L16;rate=24000 by default) and speech to text with Scribe
// (elevenlabs/scribe_v1 takes an audio media part in a user message and
// answers with the transcript). It is an ordinary Genkit plugin, usable in
// any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&elevenlabs.ElevenLabs{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("elevenlabs/eleven_flash_v2_5"),
//	    ai.WithPrompt("Hello there"), ai.WithConfig(elevenlabs.Config{Voice: "Rachel"}))
//	resp, err = genkit.Generate(ctx, g, ai.WithModelName("elevenlabs/scribe_v1"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// The voice is a voice ID, one of the premade names (Rachel, Adam, ...) or any
// name in the account's voice library, looked up through GET /v1/voices once.
// Transcription goes to POST /v1/speech-to-text; the audio duration (from the
// last word's end) and the mean word confidence are reported in the usage's
// Custom map (audio_seconds, confidence).
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
)

// Provider is the plugin name and the prefix of its models.
const Provider = "elevenlabs"

// DefaultBaseURL is the ElevenLabs API.
const DefaultBaseURL = "https://api.elevenlabs.io"

// DefaultModels are the text-to-speech models defined when Models is empty.
var DefaultModels = []string{"eleven_flash_v2_5", "eleven_turbo_v2_5", "eleven_multilingual_v2", "eleven_v3"}

// DefaultSTTModels are the speech-to-text models defined when Models is empty.
var DefaultSTTModels = []string{"scribe_v1", "scribe_v2"}

// DefaultVoice is used when a request names none.
const DefaultVoice = "Rachel"

// PremadeVoices maps the well-known premade voice names to their IDs, so the
// common ones need no library lookup.
var PremadeVoices = map[string]string{
	"rachel": "21m00Tcm4TlvDq8ikWAM", "adam": "pNInz6obpgDQGcFmaJgB", "bella": "EXAVITQu4vr4xnSDxMaL",
	"antoni": "ErXwobaYiN019PkySvjV", "elli": "MF3mGyEYCl7XYWbV9V6O", "josh": "TxGEqnHWrfWFTfGW9XjX",
	"arnold": "VR6AewLTigWG4xSOukaG", "domi": "AZnzlk1XvdvUeBnXmlld", "sam": "yoZ06aMxZJJ28mfd3POQ",
}

// ElevenLabs is the plugin.
type ElevenLabs struct {
	// APIKey authenticates; ELEVENLABS_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies).
	BaseURL string
	// HTTPClient overrides the client (default: 60 s timeout).
	HTTPClient *http.Client
	// Models to define, without the provider prefix. Names starting with
	// "scribe" are speech to text, the rest text to speech.
	Models []string

	mu     sync.Mutex
	voices map[string]string // lowercase name → id, from the library
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Voice is a voice ID or name (default Rachel).
	Voice string `json:"voice,omitempty"`
	// OutputFormat is an ElevenLabs output format; pcm_24000 by default. Only
	// the pcm_* formats are decoded by tamesu.
	OutputFormat string `json:"output_format,omitempty"`
	// Language is an ISO 639-1 code, for the speech models that take one and
	// for Scribe (a locale is cut to its language; empty lets Scribe detect).
	Language string `json:"language,omitempty"`
	// Diarize labels speakers. Speech to text.
	Diarize bool `json:"diarize,omitempty"`
	// TagAudioEvents marks laughter, applause and the like in the transcript.
	// Speech to text.
	TagAudioEvents bool `json:"tag_audio_events,omitempty"`
	// Stability and Similarity are voice settings, 0..1.
	Stability  *float64 `json:"stability,omitempty"`
	Similarity *float64 `json:"similarity,omitempty"`
	// Speed is 0.7..1.2.
	Speed *float64 `json:"speed,omitempty"`
}

// Name implements api.Plugin.
func (e *ElevenLabs) Name() string { return Provider }

// Init implements api.Plugin.
func (e *ElevenLabs) Init(context.Context) []api.Action {
	if e.APIKey == "" {
		e.APIKey = os.Getenv("ELEVENLABS_API_KEY")
	}
	if e.BaseURL == "" {
		e.BaseURL = DefaultBaseURL
	}
	if e.HTTPClient == nil {
		e.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := e.Models
	if len(models) == 0 {
		models = append(append([]string(nil), DefaultModels...), DefaultSTTModels...)
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, e.model(m))
	}
	return actions
}

// IsSTT reports whether a model name (without the prefix) transcribes.
func IsSTT(model string) bool { return strings.HasPrefix(model, "scribe") }

func (e *ElevenLabs) model(name string) api.Action {
	if IsSTT(name) {
		supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
		return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "ElevenLabs " + name, Supports: supports},
			func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
				return e.transcribe(ctx, name, req, cfg)
			})
	}
	supports := &ai.ModelSupports{Media: false, Multiturn: false, Output: []string{"media"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "ElevenLabs " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return e.synthesize(ctx, name, req, cfg)
		})
}

var voiceIDRe = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

func (e *ElevenLabs) synthesize(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if e.APIKey == "" {
		return nil, errors.New("elevenlabs: no API key (set ELEVENLABS_API_KEY or ElevenLabs.APIKey)")
	}
	text := promptText(req)
	if text == "" {
		return nil, errors.New("elevenlabs: the request has no text to speak")
	}
	voiceID, err := e.voiceID(ctx, cfg.Voice)
	if err != nil {
		return nil, err
	}
	format := cfg.OutputFormat
	if format == "" {
		format = "pcm_24000"
	}
	body := map[string]any{"text": text, "model_id": model}
	if cfg.Language != "" {
		body["language_code"] = cfg.Language
	}
	settings := map[string]any{}
	if cfg.Stability != nil {
		settings["stability"] = *cfg.Stability
	}
	if cfg.Similarity != nil {
		settings["similarity_boost"] = *cfg.Similarity
	}
	if cfg.Speed != nil {
		settings["speed"] = *cfg.Speed
	}
	if len(settings) > 0 {
		body["voice_settings"] = settings
	}
	payload, _ := json.Marshal(body)
	u := strings.TrimRight(e.BaseURL, "/") + "/v1/text-to-speech/" + url.PathEscape(voiceID) + "?output_format=" + url.QueryEscape(format)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("xi-api-key", e.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := e.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("elevenlabs: %s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	mime, rate := mimeFor(format)
	usage := &ai.GenerationUsage{InputCharacters: len(text), OutputAudioFiles: 1, Custom: map[string]float64{}}
	if rate > 0 {
		usage.Custom["audio_seconds"] = float64(len(data)) / 2 / float64(rate)
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewMediaPart(mime, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))),
		Usage:        usage,
	}, nil
}

// mimeFor maps an output format to a content type and PCM rate (0 when the
// format is not raw PCM).
func mimeFor(format string) (string, int) {
	switch {
	case strings.HasPrefix(format, "pcm_"):
		var rate int
		_, _ = fmt.Sscanf(format, "pcm_%d", &rate)
		return fmt.Sprintf("audio/L16;rate=%d", rate), rate
	case strings.HasPrefix(format, "mp3_"):
		return "audio/mpeg", 0
	case strings.HasPrefix(format, "ulaw_"):
		return "audio/basic", 0
	case strings.HasPrefix(format, "opus_"):
		return "audio/ogg", 0
	}
	return "application/octet-stream", 0
}

// voiceID resolves a voice ID, premade name or library name.
func (e *ElevenLabs) voiceID(ctx context.Context, voice string) (string, error) {
	if voice == "" {
		voice = DefaultVoice
	}
	if voiceIDRe.MatchString(voice) {
		return voice, nil
	}
	if id, ok := PremadeVoices[strings.ToLower(voice)]; ok {
		return id, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.voices == nil {
		if err := e.loadVoices(ctx); err != nil {
			return "", err
		}
	}
	if id, ok := e.voices[strings.ToLower(voice)]; ok {
		return id, nil
	}
	return "", fmt.Errorf("elevenlabs: unknown voice %q (not an ID, a premade name or a name in the voice library)", voice)
}

func (e *ElevenLabs) loadVoices(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.BaseURL, "/")+"/v1/voices", nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("xi-api-key", e.APIKey)
	res, err := e.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("elevenlabs: listing voices: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("elevenlabs: listing voices: %s", res.Status)
	}
	var out struct {
		Voices []struct {
			VoiceID string `json:"voice_id"`
			Name    string `json:"name"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return fmt.Errorf("elevenlabs: listing voices: %w", err)
	}
	e.voices = map[string]string{}
	for _, v := range out.Voices {
		e.voices[strings.ToLower(v.Name)] = v.VoiceID
	}
	return nil
}

// transcript is the part of the speech-to-text response tamesu reads.
type transcript struct {
	Text  string `json:"text"`
	Words []struct {
		Text    string  `json:"text"`
		Type    string  `json:"type"`
		End     float64 `json:"end"`
		Logprob float64 `json:"logprob"`
	} `json:"words"`
}

func (e *ElevenLabs) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if e.APIKey == "" {
		return nil, errors.New("elevenlabs: no API key (set ELEVENLABS_API_KEY or ElevenLabs.APIKey)")
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
	_ = w.WriteField("model_id", model)
	if lang, _, _ := strings.Cut(cfg.Language, "-"); lang != "" {
		_ = w.WriteField("language_code", strings.ToLower(lang))
	}
	_ = w.WriteField("tag_audio_events", strconv.FormatBool(cfg.TagAudioEvents))
	if cfg.Diarize {
		_ = w.WriteField("diarize", "true")
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.BaseURL, "/")+"/v1/speech-to-text", &form)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("xi-api-key", e.APIKey)
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	res, err := e.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("elevenlabs: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var out transcript
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("elevenlabs: decoding the response: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	// Scribe times every word and gives each a log probability; the end of
	// the last one is the speech's duration, the mean probability its
	// confidence.
	seconds, confidence, words := 0.0, 0.0, 0
	for _, wd := range out.Words {
		if wd.End > seconds {
			seconds = wd.End
		}
		if wd.Type == "word" {
			confidence += math.Exp(wd.Logprob)
			words++
		}
	}
	if words > 0 {
		confidence /= float64(words)
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": seconds, "confidence": confidence},
		},
	}, nil
}

// extensionFor names the uploaded file for its type.
func extensionFor(mime string) string {
	mt, _, _ := strings.Cut(strings.ToLower(mime), ";")
	switch mt {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/webm", "video/webm":
		return ".webm"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return ".m4a"
	}
	return ".wav"
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
	return "", nil, errors.New("elevenlabs: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("elevenlabs: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("elevenlabs: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("elevenlabs: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("elevenlabs: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
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
