// Package rime is a Genkit model plugin for Rime text to speech: models named
// rime/<modelId> (arcana, mistv2, mist) take the request's text and answer
// with an audio media part, a data URL of 16-bit PCM (audio/L16;rate=24000 by
// default). It is an ordinary Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&rime.Rime{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("rime/arcana"),
//	    ai.WithPrompt("Hello there"), ai.WithConfig(rime.Config{Voice: "luna", Language: "en"}))
//
// The voice is a Rime speaker name (luna, celeste, ...); there is no lookup,
// the name is sent as is. The plugin makes one call per request, POST
// /v1/rime-tts with Accept: audio/pcm, and the usage Custom map carries
// audio_seconds, the length of the returned audio.
package rime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
)

// Provider is the plugin name and the prefix of its models.
const Provider = "rime"

// DefaultBaseURL is the Rime API.
const DefaultBaseURL = "https://users.rime.ai"

// DefaultModels are defined when Models is empty (Rime modelId values).
var DefaultModels = []string{"arcana", "mistv2", "mist"}

// DefaultVoice is the speaker used when a request names none.
const DefaultVoice = "luna"

// DefaultSampleRate is the PCM rate asked for when Config.SampleRate is zero.
const DefaultSampleRate = 24000

// Rime is the plugin.
type Rime struct {
	// APIKey authenticates; RIME_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies).
	BaseURL string
	// HTTPClient overrides the client (default: 60 s timeout).
	HTTPClient *http.Client
	// Models to define, without the provider prefix.
	Models []string
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Voice is a Rime speaker name (default luna).
	Voice string `json:"voice,omitempty"`
	// Language is a BCP-47 tag (en, es-ES) or a Rime three-letter code (eng,
	// spa); BCP-47 tags are mapped to Rime's codes. English by default.
	Language string `json:"language,omitempty"`
	// SampleRate is the PCM rate in Hz (default 24000).
	SampleRate int `json:"sample_rate,omitempty"`
	// Speed is Rime's speedAlpha: values above 1 are slower, below 1 faster.
	Speed *float64 `json:"speed,omitempty"`
	// ReduceLatency asks Rime to trade some quality for a faster answer.
	ReduceLatency bool `json:"reduce_latency,omitempty"`
}

// Name implements api.Plugin.
func (r *Rime) Name() string { return Provider }

// Init implements api.Plugin.
func (r *Rime) Init(context.Context) []api.Action {
	if r.APIKey == "" {
		r.APIKey = os.Getenv("RIME_API_KEY")
	}
	if r.BaseURL == "" {
		r.BaseURL = DefaultBaseURL
	}
	if r.HTTPClient == nil {
		r.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := r.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, r.model(m))
	}
	return actions
}

func (r *Rime) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: false, Multiturn: false, Output: []string{"media"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Rime " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return r.synthesize(ctx, name, req, cfg)
		})
}

func (r *Rime) synthesize(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if r.APIKey == "" {
		return nil, errors.New("rime: no API key (set RIME_API_KEY or Rime.APIKey)")
	}
	text := promptText(req)
	if text == "" {
		return nil, errors.New("rime: the request has no text to speak")
	}
	speaker := cfg.Voice
	if speaker == "" {
		speaker = DefaultVoice
	}
	rate := cfg.SampleRate
	if rate <= 0 {
		rate = DefaultSampleRate
	}
	body := map[string]any{
		"speaker":      speaker,
		"text":         text,
		"modelId":      model,
		"lang":         langCode(cfg.Language),
		"samplingRate": rate,
	}
	if cfg.Speed != nil {
		body["speedAlpha"] = *cfg.Speed
	}
	if cfg.ReduceLatency {
		body["reduceLatency"] = true
	}
	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.BaseURL, "/")+"/v1/rime-tts", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+r.APIKey)
	httpReq.Header.Set("Accept", "audio/pcm")
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := r.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rime: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	pcm, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("rime: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("rime: %s: %s", res.Status, strings.TrimSpace(string(pcm)))
	}
	mime := fmt.Sprintf("audio/L16;rate=%d", rate)
	usage := &ai.GenerationUsage{
		InputCharacters:  len(text),
		OutputAudioFiles: 1,
		Custom:           map[string]float64{"audio_seconds": float64(len(pcm)) / 2 / float64(rate)},
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewMediaPart(mime, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(pcm))),
		Usage:        usage,
	}, nil
}

// langCodes maps ISO 639-1 codes to the codes Rime takes.
var langCodes = map[string]string{
	"en": "eng", "es": "spa", "fr": "fra", "de": "ger", "it": "ita", "pt": "por",
	"nl": "nld", "ja": "jpn", "ko": "kor", "zh": "cmn", "hi": "hin", "ar": "ara",
	"pl": "pol", "ru": "rus", "sv": "swe", "tr": "tur",
}

// langCode maps a BCP-47 tag (en, es-ES) or a Rime three-letter code to the
// code Rime takes; three-letter codes pass through and empty means English.
func langCode(lang string) string {
	base, _, _ := strings.Cut(lang, "-")
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "" {
		return "eng"
	}
	if code, ok := langCodes[base]; ok {
		return code
	}
	return base
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
