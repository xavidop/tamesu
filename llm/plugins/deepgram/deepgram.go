// Package deepgram is a Genkit model plugin for Deepgram speech to text:
// models named deepgram/<model> (nova-3 by default) take an audio media part
// in a user message and answer with the transcript as text. It is an
// ordinary Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&deepgram.Deepgram{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("deepgram/nova-3"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// Requests go to the prerecorded endpoint (POST /v1/listen) with the audio as
// the body; the response's confidence and duration are reported in the
// usage's Custom map (confidence, audio_seconds).
package deepgram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
)

// Provider is the plugin name and the prefix of its models.
const Provider = "deepgram"

// DefaultBaseURL is Deepgram's API.
const DefaultBaseURL = "https://api.deepgram.com"

// DefaultModels are defined when Models is empty.
var DefaultModels = []string{"nova-3", "nova-2", "enhanced", "base"}

// Deepgram is the plugin.
type Deepgram struct {
	// APIKey authenticates; DEEPGRAM_API_KEY when empty.
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
	// Language is a BCP-47 tag such as en or es-ES; omitted means Deepgram's
	// default for the model (or detection where supported).
	Language string `json:"language,omitempty"`
	// SmartFormat applies punctuation and formatting (default true).
	SmartFormat *bool `json:"smart_format,omitempty"`
	// Keywords boost recognition of the listed terms (nova-2 and older).
	Keywords []string `json:"keywords,omitempty"`
	// Keyterms boost recognition of the listed terms (nova-3).
	Keyterms []string `json:"keyterms,omitempty"`
	// Diarize labels speakers.
	Diarize bool `json:"diarize,omitempty"`
}

// Name implements api.Plugin.
func (d *Deepgram) Name() string { return Provider }

// Init implements api.Plugin.
func (d *Deepgram) Init(context.Context) []api.Action {
	if d.APIKey == "" {
		d.APIKey = os.Getenv("DEEPGRAM_API_KEY")
	}
	if d.BaseURL == "" {
		d.BaseURL = DefaultBaseURL
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := d.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, d.model(m))
	}
	return actions
}

func (d *Deepgram) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Deepgram " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return d.transcribe(ctx, name, req, cfg)
		})
}

// response is the part of Deepgram's prerecorded response tamesu reads.
type response struct {
	Metadata struct {
		Duration float64 `json:"duration"`
	} `json:"metadata"`
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
			} `json:"alternatives"`
		} `json:"channels"`
	} `json:"results"`
}

func (d *Deepgram) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if d.APIKey == "" {
		return nil, errors.New("deepgram: no API key (set DEEPGRAM_API_KEY or Deepgram.APIKey)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	q := url.Values{"model": {model}}
	if cfg.Language != "" {
		q.Set("language", cfg.Language)
	}
	if cfg.SmartFormat == nil || *cfg.SmartFormat {
		q.Set("smart_format", "true")
	}
	for _, k := range cfg.Keywords {
		q.Add("keywords", k)
	}
	for _, k := range cfg.Keyterms {
		q.Add("keyterm", k)
	}
	if cfg.Diarize {
		q.Set("diarize", "true")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+"/v1/listen?"+q.Encode(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Token "+d.APIKey)
	httpReq.Header.Set("Content-Type", mime)
	res, err := d.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("deepgram: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var out response
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("deepgram: decoding the response: %w", err)
	}
	text, confidence := "", 0.0
	if len(out.Results.Channels) > 0 && len(out.Results.Channels[0].Alternatives) > 0 {
		alt := out.Results.Channels[0].Alternatives[0]
		text, confidence = alt.Transcript, alt.Confidence
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": out.Metadata.Duration, "confidence": confidence},
		},
	}, nil
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
	return "", nil, errors.New("deepgram: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("deepgram: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("deepgram: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("deepgram: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("deepgram: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
