// Package soniox is a Genkit model plugin for Soniox speech to text: models
// named soniox/<model> (stt-async-v6 by default) take an audio media part in
// a user message and answer with the transcript as text. It is an ordinary
// Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&soniox.Soniox{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("soniox/stt-async-v6"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// A request uploads the audio (POST /v1/files), creates a transcription
// (POST /v1/transcriptions), polls it until it completes, reads the
// transcript (GET /v1/transcriptions/{id}/transcript) and deletes both the
// transcription and the file. The response's duration and mean token
// confidence are reported in the usage's Custom map (audio_seconds,
// confidence).
package soniox

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
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
)

// Provider is the plugin name and the prefix of its models.
const Provider = "soniox"

// DefaultBaseURL is Soniox's API.
const DefaultBaseURL = "https://api.soniox.com"

// DefaultModels are defined when Models is empty.
var DefaultModels = []string{"stt-async-v6", "stt-async-v5"}

// DefaultPollInterval is how often a transcription's status is read.
const DefaultPollInterval = 400 * time.Millisecond

// Soniox is the plugin.
type Soniox struct {
	// APIKey authenticates; SONIOX_API_KEY when empty.
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
	// Language is a hint, an ISO 639-1 code such as en or es (a locale is
	// cut to its language); it joins LanguageHints.
	Language string `json:"language,omitempty"`
	// LanguageHints are the languages expected in the audio; none means
	// automatic detection.
	LanguageHints []string `json:"language_hints,omitempty"`
	// Context helps with domain terms: free text passed as the context.
	Context string `json:"context,omitempty"`
	// LanguageIdentification labels each token with its language.
	LanguageIdentification bool `json:"language_identification,omitempty"`
}

// Name implements api.Plugin.
func (s *Soniox) Name() string { return Provider }

// Init implements api.Plugin.
func (s *Soniox) Init(context.Context) []api.Action {
	if s.APIKey == "" {
		s.APIKey = os.Getenv("SONIOX_API_KEY")
	}
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
	if s.HTTPClient == nil {
		s.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if s.PollInterval <= 0 {
		s.PollInterval = DefaultPollInterval
	}
	models := s.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, s.model(m))
	}
	return actions
}

func (s *Soniox) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Soniox " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return s.transcribe(ctx, name, req, cfg)
		})
}

// transcription is the part of a transcription's status tamesu reads.
type transcription struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	AudioDurationMS int    `json:"audio_duration_ms"`
	ErrorType       string `json:"error_type"`
	ErrorMessage    string `json:"error_message"`
}

// transcript is the finished text with its tokens.
type transcript struct {
	Text   string `json:"text"`
	Tokens []struct {
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
	} `json:"tokens"`
}

func (s *Soniox) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if s.APIKey == "" {
		return nil, errors.New("soniox: no API key (set SONIOX_API_KEY or Soniox.APIKey)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	fileID, err := s.upload(ctx, mime, data)
	if err != nil {
		return nil, err
	}
	// The file and the transcription are deleted whatever happens; the
	// transcription first, since a processing one keeps its file.
	defer func() { _ = s.delete(context.WithoutCancel(ctx), "/v1/files/"+url.PathEscape(fileID)) }()
	body := map[string]any{"file_id": fileID, "model": model}
	hints := append([]string(nil), cfg.LanguageHints...)
	if lang, _, _ := strings.Cut(cfg.Language, "-"); lang != "" {
		hints = append(hints, strings.ToLower(lang))
	}
	if len(hints) > 0 {
		body["language_hints"] = hints
	}
	if cfg.Context != "" {
		body["context"] = cfg.Context
	}
	if cfg.LanguageIdentification {
		body["enable_language_identification"] = true
	}
	payload, _ := json.Marshal(body)
	var tr transcription
	if err := s.call(ctx, http.MethodPost, "/v1/transcriptions", "application/json", bytes.NewReader(payload), &tr); err != nil {
		return nil, err
	}
	if tr.ID == "" {
		return nil, errors.New("soniox: the transcription has no id")
	}
	defer func() { _ = s.delete(context.WithoutCancel(ctx), "/v1/transcriptions/"+url.PathEscape(tr.ID)) }()
	for tr.Status != "completed" {
		if tr.Status == "error" {
			return nil, fmt.Errorf("soniox: transcription failed: %s %s", tr.ErrorType, strings.TrimSpace(tr.ErrorMessage))
		}
		timer := time.NewTimer(s.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := s.call(ctx, http.MethodGet, "/v1/transcriptions/"+url.PathEscape(tr.ID), "", nil, &tr); err != nil {
			return nil, err
		}
	}
	var out transcript
	if err := s.call(ctx, http.MethodGet, "/v1/transcriptions/"+url.PathEscape(tr.ID)+"/transcript", "", nil, &out); err != nil {
		return nil, err
	}
	text := out.Text
	confidence := 0.0
	if len(out.Tokens) > 0 {
		var b strings.Builder
		for _, t := range out.Tokens {
			b.WriteString(t.Text)
			confidence += t.Confidence
		}
		confidence /= float64(len(out.Tokens))
		if text == "" {
			text = b.String()
		}
	}
	text = strings.TrimSpace(text)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": float64(tr.AudioDurationMS) / 1000, "confidence": confidence},
		},
	}, nil
}

func (s *Soniox) upload(ctx context.Context, mime string, data []byte) (string, error) {
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	part, err := w.CreateFormFile("file", "audio"+extensionFor(mime))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := s.call(ctx, http.MethodPost, "/v1/files", w.FormDataContentType(), &form, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("soniox: the upload returned no file id")
	}
	return out.ID, nil
}

func (s *Soniox) delete(ctx context.Context, path string) error {
	return s.call(ctx, http.MethodDelete, path, "", nil, nil)
}

// call makes one API request and decodes a JSON response into out (nil for
// none).
func (s *Soniox) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := s.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("soniox: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("soniox: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("soniox: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("soniox: decoding the response of %s %s: %w", method, path, err)
	}
	return nil
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
	return "", nil, errors.New("soniox: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("soniox: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("soniox: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("soniox: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("soniox: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
