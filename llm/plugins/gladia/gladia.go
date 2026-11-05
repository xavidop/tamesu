// Package gladia is a Genkit model plugin for Gladia pre-recorded speech to
// text: models named gladia/<model> (solaria-1 by default) take an audio
// media part in a user message and answer with the transcript as text. It is
// an ordinary Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&gladia.Gladia{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("gladia/solaria-1"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// Gladia's pre-recorded API takes no model parameter: every request runs the
// current Solaria model, whatever name the reference carries. The model name
// exists so that a tamesu reference reads gladia/solaria-1 like every other
// provider's.
//
// A request uploads the audio (POST /v2/upload), creates a transcription
// (POST /v2/pre-recorded) pointing at the upload, polls it
// (GET /v2/pre-recorded/{id}) until it is done and deletes it
// (DELETE /v2/pre-recorded/{id}) on a best-effort basis. The response's
// audio duration and the mean utterance confidence are reported in the
// usage's Custom map (audio_seconds, confidence; confidence is omitted when
// the result has no utterances).
package gladia

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
const Provider = "gladia"

// DefaultBaseURL is Gladia's API.
const DefaultBaseURL = "https://api.gladia.io"

// DefaultModels are defined when Models is empty. Gladia's pre-recorded API
// does not take a model parameter (it always runs the current Solaria
// model); the name exists so the reference reads gladia/solaria-1.
var DefaultModels = []string{"solaria-1"}

// DefaultPollInterval is how often a transcription's status is read.
const DefaultPollInterval = time.Second

// Gladia is the plugin.
type Gladia struct {
	// APIKey authenticates; GLADIA_API_KEY when empty.
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
	// Language is the audio's language, an ISO 639-1 code such as en or es
	// (a locale such as en-GB is cut to its language); it becomes the single
	// entry of language_config.languages. Empty sends no language_config, so
	// Gladia detects the language.
	Language string `json:"language,omitempty"`
	// Diarize asks for speaker diarization (diarization).
	Diarize bool `json:"diarize,omitempty"`
}

// Name implements api.Plugin.
func (g *Gladia) Name() string { return Provider }

// Init implements api.Plugin.
func (g *Gladia) Init(context.Context) []api.Action {
	if g.APIKey == "" {
		g.APIKey = os.Getenv("GLADIA_API_KEY")
	}
	if g.BaseURL == "" {
		g.BaseURL = DefaultBaseURL
	}
	if g.HTTPClient == nil {
		g.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if g.PollInterval <= 0 {
		g.PollInterval = DefaultPollInterval
	}
	models := g.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, g.model(m))
	}
	return actions
}

func (g *Gladia) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Gladia " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return g.transcribe(ctx, req, cfg)
		})
}

// upload is the part of an upload's answer tamesu reads.
type upload struct {
	AudioURL      string `json:"audio_url"`
	AudioMetadata struct {
		AudioDuration float64 `json:"audio_duration"`
	} `json:"audio_metadata"`
}

// transcription is the part of a transcription's status tamesu reads.
type transcription struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ErrorCode int    `json:"error_code"`
	Result    struct {
		Metadata struct {
			AudioDuration float64 `json:"audio_duration"`
		} `json:"metadata"`
		Transcription struct {
			FullTranscript string `json:"full_transcript"`
			Utterances     []struct {
				Text       string  `json:"text"`
				Start      float64 `json:"start"`
				End        float64 `json:"end"`
				Confidence float64 `json:"confidence"`
			} `json:"utterances"`
		} `json:"transcription"`
	} `json:"result"`
}

func (g *Gladia) transcribe(ctx context.Context, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if g.APIKey == "" {
		return nil, errors.New("gladia: no API key (set GLADIA_API_KEY or Gladia.APIKey)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	up, err := g.upload(ctx, mime, data)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"audio_url": up.AudioURL, "diarization": cfg.Diarize}
	if code := languageCode(cfg.Language); code != "" {
		body["language_config"] = map[string]any{"languages": []string{code}, "code_switching": false}
	}
	payload, _ := json.Marshal(body)
	var tr transcription
	if err := g.call(ctx, http.MethodPost, "/v2/pre-recorded", "application/json", bytes.NewReader(payload), &tr); err != nil {
		return nil, err
	}
	if tr.ID == "" {
		return nil, errors.New("gladia: the transcription has no id")
	}
	// The transcription is deleted whatever happens, best effort.
	path := "/v2/pre-recorded/" + url.PathEscape(tr.ID)
	defer func() { _ = g.call(context.WithoutCancel(ctx), http.MethodDelete, path, "", nil, nil) }()
	for tr.Status != "done" {
		if tr.Status == "error" {
			return nil, fmt.Errorf("gladia: transcription failed with error_code %d", tr.ErrorCode)
		}
		timer := time.NewTimer(g.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := g.call(ctx, http.MethodGet, path, "", nil, &tr); err != nil {
			return nil, err
		}
	}
	text := strings.TrimSpace(tr.Result.Transcription.FullTranscript)
	seconds := tr.Result.Metadata.AudioDuration
	if seconds == 0 {
		seconds = up.AudioMetadata.AudioDuration
	}
	custom := map[string]float64{"audio_seconds": seconds}
	if utterances := tr.Result.Transcription.Utterances; len(utterances) > 0 {
		confidence := 0.0
		for _, u := range utterances {
			confidence += u.Confidence
		}
		custom["confidence"] = confidence / float64(len(utterances))
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           custom,
		},
	}, nil
}

// upload sends the audio as a multipart form and returns where Gladia stores
// it, with the duration Gladia measured.
func (g *Gladia) upload(ctx context.Context, mime string, data []byte) (*upload, error) {
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	part, err := w.CreateFormFile("audio", "audio"+extensionFor(mime))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	var out upload
	if err := g.call(ctx, http.MethodPost, "/v2/upload", w.FormDataContentType(), &form, &out); err != nil {
		return nil, err
	}
	if out.AudioURL == "" {
		return nil, errors.New("gladia: the upload returned no audio_url")
	}
	return &out, nil
}

// call makes one API request and decodes a JSON response into out (nil for
// none).
func (g *Gladia) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("x-gladia-key", g.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := g.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("gladia: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("gladia: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("gladia: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("gladia: decoding the response of %s %s: %w", method, path, err)
	}
	return nil
}

// languageCode maps a BCP-47 tag to Gladia's language code: the part before
// the first "-", lower case (en-GB → en, es → es).
func languageCode(tag string) string {
	lang, _, _ := strings.Cut(strings.TrimSpace(tag), "-")
	return strings.ToLower(lang)
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
	return "", nil, errors.New("gladia: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("gladia: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("gladia: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("gladia: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("gladia: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
