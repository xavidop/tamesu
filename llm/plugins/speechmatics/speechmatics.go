// Package speechmatics is a Genkit model plugin for Speechmatics batch speech
// to text: models named speechmatics/<model> (enhanced and standard by
// default; the model is Speechmatics' operating_point) take an audio media
// part in a user message and answer with the transcript as text. It is an
// ordinary Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&speechmatics.Speechmatics{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("speechmatics/enhanced"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// A request creates a transcription job with the audio and its configuration
// (POST /v2/jobs, multipart), polls the job (GET /v2/jobs/{id}) until it is
// done, reads the transcript (GET /v2/jobs/{id}/transcript?format=json-v2)
// and deletes the job (DELETE /v2/jobs/{id}), which removes the audio and the
// transcript from Speechmatics. The response's audio duration and mean word
// confidence are reported in the usage's Custom map (audio_seconds,
// confidence).
package speechmatics

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
const Provider = "speechmatics"

// DefaultBaseURL is Speechmatics' batch API.
const DefaultBaseURL = "https://asr.api.speechmatics.com"

// DefaultModels are defined when Models is empty; they are Speechmatics'
// operating_point values.
var DefaultModels = []string{"enhanced", "standard"}

// DefaultPollInterval is how often a job's status is read.
const DefaultPollInterval = time.Second

// Speechmatics is the plugin.
type Speechmatics struct {
	// APIKey authenticates; SPEECHMATICS_API_KEY when empty.
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
	// Language is the audio's language as a BCP-47 tag or a Speechmatics code
	// (en, en-GB, es); it becomes the transcription's language, and an
	// English region (en-GB, en-US, en-AU) its output_locale. Empty means en;
	// auto asks Speechmatics to identify the language.
	Language string `json:"language,omitempty"`
	// Diarize asks for speaker diarization (diarization: speaker).
	Diarize bool `json:"diarize,omitempty"`
}

// Name implements api.Plugin.
func (s *Speechmatics) Name() string { return Provider }

// Init implements api.Plugin.
func (s *Speechmatics) Init(context.Context) []api.Action {
	if s.APIKey == "" {
		s.APIKey = os.Getenv("SPEECHMATICS_API_KEY")
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

func (s *Speechmatics) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Speechmatics " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return s.transcribe(ctx, name, req, cfg)
		})
}

// job is the part of a job's status tamesu reads.
type job struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Duration float64 `json:"duration"`
	Errors   []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// result is one item of a json-v2 transcript: a word or a punctuation mark.
type result struct {
	Type         string  `json:"type"`
	StartTime    float64 `json:"start_time"`
	EndTime      float64 `json:"end_time"`
	Alternatives []struct {
		Content    string  `json:"content"`
		Confidence float64 `json:"confidence"`
	} `json:"alternatives"`
}

// transcript is a json-v2 transcript.
type transcript struct {
	Job struct {
		Duration float64 `json:"duration"`
	} `json:"job"`
	Results []result `json:"results"`
}

func (s *Speechmatics) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if s.APIKey == "" {
		return nil, errors.New("speechmatics: no API key (set SPEECHMATICS_API_KEY or Speechmatics.APIKey)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	id, err := s.create(ctx, model, mime, data, cfg)
	if err != nil {
		return nil, err
	}
	// The job is deleted whatever happens, which removes the audio and the
	// transcript from Speechmatics.
	path := "/v2/jobs/" + url.PathEscape(id)
	defer func() { _ = s.call(context.WithoutCancel(ctx), http.MethodDelete, path, "", nil, nil) }()
	var status struct {
		Job job `json:"job"`
	}
	status.Job.Status = "running"
	for status.Job.Status != "done" {
		switch status.Job.Status {
		case "rejected", "deleted", "expired":
			msg := "no error message"
			if len(status.Job.Errors) > 0 {
				msg = strings.TrimSpace(status.Job.Errors[0].Message)
			}
			return nil, fmt.Errorf("speechmatics: job %s: %s", status.Job.Status, msg)
		}
		timer := time.NewTimer(s.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := s.call(ctx, http.MethodGet, path, "", nil, &status); err != nil {
			return nil, err
		}
	}
	var out transcript
	if err := s.call(ctx, http.MethodGet, path+"/transcript?format=json-v2", "", nil, &out); err != nil {
		return nil, err
	}
	text, confidence, end := assemble(out.Results)
	seconds := out.Job.Duration
	if seconds <= 0 {
		seconds = status.Job.Duration
	}
	if seconds <= 0 {
		seconds = end
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

// create submits the audio and its configuration as a job and returns the
// job's id.
func (s *Speechmatics) create(ctx context.Context, model, mime string, data []byte, cfg Config) (string, error) {
	code, locale := languageCode(cfg.Language)
	tc := map[string]any{"language": code, "operating_point": model}
	if locale != "" {
		tc["output_locale"] = locale
	}
	if cfg.Diarize {
		tc["diarization"] = "speaker"
	}
	config, _ := json.Marshal(map[string]any{"type": "transcription", "transcription_config": tc})
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	part, err := w.CreateFormFile("data_file", "audio"+extensionFor(mime))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.WriteField("config", string(config)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := s.call(ctx, http.MethodPost, "/v2/jobs", w.FormDataContentType(), &form, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("speechmatics: the job has no id")
	}
	return out.ID, nil
}

// call makes one API request and decodes a JSON response into out (nil for
// none).
func (s *Speechmatics) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
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
		return fmt.Errorf("speechmatics: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("speechmatics: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("speechmatics: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("speechmatics: decoding the response of %s %s: %w", method, path, err)
	}
	return nil
}

// assemble joins a json-v2 transcript's items into text: words separated by
// single spaces, a punctuation mark appended to the token before it. It also
// returns the mean confidence of the words' first alternatives and the
// latest end time, a fallback for the audio's duration.
func assemble(results []result) (text string, confidence, end float64) {
	var b strings.Builder
	words := 0
	for _, r := range results {
		if len(r.Alternatives) == 0 {
			continue
		}
		content := r.Alternatives[0].Content
		if r.Type == "punctuation" {
			b.WriteString(content)
		} else {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(content)
			confidence += r.Alternatives[0].Confidence
			words++
		}
		if r.EndTime > end {
			end = r.EndTime
		}
	}
	if words > 0 {
		confidence /= float64(words)
	}
	return strings.TrimSpace(b.String()), confidence, end
}

// languageCode maps a BCP-47 tag to Speechmatics' language code and
// output_locale: the code is the lower-cased language (en-GB becomes en; empty
// means en; auto passes through, asking for language identification) and the
// locale is the full tag when the language is English and a region is given
// (en-GB, en-US, en-AU), the only locales Speechmatics distinguishes;
// otherwise it is empty.
func languageCode(tag string) (code, locale string) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "en", ""
	}
	lang, region, _ := strings.Cut(tag, "-")
	code = strings.ToLower(lang)
	if code == "en" && region != "" {
		locale = code + "-" + strings.ToUpper(region)
	}
	return code, locale
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
	return "", nil, errors.New("speechmatics: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("speechmatics: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("speechmatics: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("speechmatics: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("speechmatics: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
