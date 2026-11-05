// Package assemblyai is a Genkit model plugin for AssemblyAI speech to text:
// models named assemblyai/<model> (universal, slam-1, nano and best by
// default; the model is AssemblyAI's speech_model) take an audio media part
// in a user message and answer with the transcript as text. It is an ordinary
// Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&assemblyai.AssemblyAI{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("assemblyai/universal"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// A request uploads the audio (POST /v2/upload), creates a transcript
// (POST /v2/transcript) pointing at the upload, polls it
// (GET /v2/transcript/{id}) until it completes and deletes it
// (DELETE /v2/transcript/{id}), which removes the transcript's content from
// AssemblyAI. The response's audio duration and confidence are reported in
// the usage's Custom map (audio_seconds, confidence).
package assemblyai

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
const Provider = "assemblyai"

// DefaultBaseURL is AssemblyAI's API.
const DefaultBaseURL = "https://api.assemblyai.com"

// DefaultModels are defined when Models is empty; they are AssemblyAI's
// speech_model values.
var DefaultModels = []string{"universal", "slam-1", "nano", "best"}

// DefaultPollInterval is how often a transcript's status is read.
const DefaultPollInterval = 400 * time.Millisecond

// AssemblyAI is the plugin.
type AssemblyAI struct {
	// APIKey authenticates; ASSEMBLYAI_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies).
	BaseURL string
	// HTTPClient overrides the client (default: 60 s timeout per call).
	HTTPClient *http.Client
	// Models to define, without the provider prefix.
	Models []string
	// PollInterval between status reads (DefaultPollInterval when zero).
	PollInterval time.Duration
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Language is the audio's language as a BCP-47 tag or a bare code (en,
	// en-GB, es-ES); it becomes AssemblyAI's language_code. Empty means
	// automatic language detection unless LanguageDetection is false.
	Language string `json:"language,omitempty"`
	// LanguageDetection toggles language_detection when Language is empty;
	// nil means on.
	LanguageDetection *bool `json:"language_detection,omitempty"`
	// Keyterms are domain terms to boost (keyterms_prompt; universal and
	// slam-1 honour them).
	Keyterms []string `json:"keyterms,omitempty"`
	// SpeakerLabels asks for speaker diarization (speaker_labels).
	SpeakerLabels bool `json:"speaker_labels,omitempty"`
}

// Name implements api.Plugin.
func (a *AssemblyAI) Name() string { return Provider }

// Init implements api.Plugin.
func (a *AssemblyAI) Init(context.Context) []api.Action {
	if a.APIKey == "" {
		a.APIKey = os.Getenv("ASSEMBLYAI_API_KEY")
	}
	if a.BaseURL == "" {
		a.BaseURL = DefaultBaseURL
	}
	if a.HTTPClient == nil {
		a.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if a.PollInterval <= 0 {
		a.PollInterval = DefaultPollInterval
	}
	models := a.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, a.model(m))
	}
	return actions
}

func (a *AssemblyAI) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "AssemblyAI " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return a.transcribe(ctx, name, req, cfg)
		})
}

// transcript is the part of a transcript's status tamesu reads.
type transcript struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	Text          string  `json:"text"`
	Confidence    float64 `json:"confidence"`
	AudioDuration float64 `json:"audio_duration"`
	LanguageCode  string  `json:"language_code"`
	Error         string  `json:"error"`
}

func (a *AssemblyAI) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if a.APIKey == "" {
		return nil, errors.New("assemblyai: no API key (set ASSEMBLYAI_API_KEY or AssemblyAI.APIKey)")
	}
	_, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	uploadURL, err := a.upload(ctx, data)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"audio_url": uploadURL, "speech_model": model}
	if cfg.Language != "" {
		body["language_code"] = languageCode(cfg.Language)
	} else if cfg.LanguageDetection == nil || *cfg.LanguageDetection {
		body["language_detection"] = true
	}
	if len(cfg.Keyterms) > 0 {
		body["keyterms_prompt"] = cfg.Keyterms
	}
	if cfg.SpeakerLabels {
		body["speaker_labels"] = true
	}
	payload, _ := json.Marshal(body)
	var tr transcript
	if err := a.call(ctx, http.MethodPost, "/v2/transcript", "application/json", bytes.NewReader(payload), &tr); err != nil {
		return nil, err
	}
	if tr.ID == "" {
		return nil, errors.New("assemblyai: the transcript has no id")
	}
	// The transcript is deleted whatever happens, which removes its content
	// from AssemblyAI.
	path := "/v2/transcript/" + url.PathEscape(tr.ID)
	defer func() { _ = a.call(context.WithoutCancel(ctx), http.MethodDelete, path, "", nil, nil) }()
	for tr.Status != "completed" {
		if tr.Status == "error" {
			return nil, fmt.Errorf("assemblyai: transcription failed: %s", strings.TrimSpace(tr.Error))
		}
		timer := time.NewTimer(a.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := a.call(ctx, http.MethodGet, path, "", nil, &tr); err != nil {
			return nil, err
		}
	}
	text := strings.TrimSpace(tr.Text)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": tr.AudioDuration, "confidence": tr.Confidence},
		},
	}, nil
}

// upload sends the raw audio and returns the URL AssemblyAI stores it at.
func (a *AssemblyAI) upload(ctx context.Context, data []byte) (string, error) {
	var out struct {
		UploadURL string `json:"upload_url"`
	}
	if err := a.call(ctx, http.MethodPost, "/v2/upload", "application/octet-stream", bytes.NewReader(data), &out); err != nil {
		return "", err
	}
	if out.UploadURL == "" {
		return "", errors.New("assemblyai: the upload returned no upload_url")
	}
	return out.UploadURL, nil
}

// call makes one API request and decodes a JSON response into out (nil for
// none).
func (a *AssemblyAI) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	// AssemblyAI takes the raw key, without a Bearer prefix.
	req.Header.Set("Authorization", a.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := a.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("assemblyai: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("assemblyai: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("assemblyai: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("assemblyai: decoding the response of %s %s: %w", method, path, err)
	}
	return nil
}

// languageCode maps a BCP-47 tag to AssemblyAI's language_code: lower case,
// with the English regional variants AssemblyAI distinguishes (en_uk, en_us,
// en_au); any other locale is cut to its language and a bare code passes
// through.
func languageCode(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	switch tag {
	case "en-gb":
		return "en_uk"
	case "en-us":
		return "en_us"
	case "en-au":
		return "en_au"
	}
	lang, _, _ := strings.Cut(tag, "-")
	return lang
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
	return "", nil, errors.New("assemblyai: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("assemblyai: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("assemblyai: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("assemblyai: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("assemblyai: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
