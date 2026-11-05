// Package azure is a Genkit model plugin for Azure AI Speech: text to speech
// with the neural voices (azure/tts takes the request's text and answers with
// an audio media part, a data URL of 16-bit PCM) and speech to text with the
// fast transcription API (azure/stt takes an audio media part and answers
// with the transcript). Azure has no model ids for speech, the voice is the
// model, so the model names are just the two directions. It is an ordinary
// Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&azure.Azure{Key: key, Region: "westeurope"}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("azure/tts"),
//	    ai.WithPrompt("Hello there"), ai.WithConfig(azure.Config{Voice: "en-US-AvaMultilingualNeural"}))
//
// Speech goes as SSML to POST https://<region>.tts.speech.microsoft.com/cognitiveservices/v1
// and comes back as raw PCM; transcription goes as multipart to POST
// https://<region>.api.cognitive.microsoft.com/speechtotext/transcriptions:transcribe?api-version=2024-11-15.
// Every request carries the Ocp-Apim-Subscription-Key header. The usage
// Custom map carries audio_seconds for both directions and confidence (the
// mean phrase confidence) for transcription.
package azure

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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
const Provider = "azure"

// DefaultVoice is the neural voice used when a request names none.
const DefaultVoice = "en-US-AvaMultilingualNeural"

// DefaultSampleRate is the PCM rate asked of the TTS model.
const DefaultSampleRate = 24000

// DefaultTTSModels are the text-to-speech models defined when Models is empty.
var DefaultTTSModels = []string{"tts"}

// DefaultSTTModels are the speech-to-text models defined when Models is empty.
var DefaultSTTModels = []string{"stt"}

// Azure is the plugin.
type Azure struct {
	// Key is the Speech resource's subscription key; AZURE_SPEECH_KEY when empty.
	Key string
	// Region is the resource's region (westeurope, eastus, ...);
	// AZURE_SPEECH_REGION when empty. The endpoints are derived from it
	// unless set explicitly.
	Region string
	// TTSEndpoint is the full text-to-speech URL. When empty it is derived
	// from Region: https://<region>.tts.speech.microsoft.com/cognitiveservices/v1.
	TTSEndpoint string
	// STTEndpoint is the full fast-transcription URL. When empty it is
	// derived from Region:
	// https://<region>.api.cognitive.microsoft.com/speechtotext/transcriptions:transcribe?api-version=2024-11-15.
	STTEndpoint string
	// HTTPClient overrides the client (default: 60 s timeout).
	HTTPClient *http.Client
	// Models to define, without the provider prefix. Names starting with
	// "stt" are speech to text, the rest text to speech.
	Models []string
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Voice is a neural voice name such as en-US-AvaMultilingualNeural
	// (DefaultVoice when empty). Text to speech.
	Voice string `json:"voice,omitempty"`
	// Language is a BCP-47 tag. For speech it is the SSML xml:lang, derived
	// from the voice name when empty (en-US-AvaMultilingualNeural → en-US).
	// For transcription it is the locale Azure recognises; a bare language
	// code is widened to its usual locale (en → en-US) and an empty value
	// lets Azure identify the language.
	Language string `json:"language,omitempty"`
	// SampleRate of the PCM returned, in Hz: 8000, 16000, 22050, 24000,
	// 44100 or 48000 (DefaultSampleRate). Text to speech.
	SampleRate int `json:"sample_rate,omitempty"`
	// Style is a speaking style the voice supports (cheerful, sad, ...),
	// applied with mstts:express-as. Text to speech.
	Style string `json:"style,omitempty"`
	// Rate is a prosody rate: a relative change (+10%) or a multiplier (1.1).
	// Text to speech.
	Rate string `json:"rate,omitempty"`
	// Diarize asks the transcription to label speakers (up to two).
	Diarize bool `json:"diarize,omitempty"`
}

// Name implements api.Plugin.
func (a *Azure) Name() string { return Provider }

// Init implements api.Plugin.
func (a *Azure) Init(context.Context) []api.Action {
	if a.Key == "" {
		a.Key = os.Getenv("AZURE_SPEECH_KEY")
	}
	if a.Region == "" {
		a.Region = os.Getenv("AZURE_SPEECH_REGION")
	}
	if a.TTSEndpoint == "" && a.Region != "" {
		a.TTSEndpoint = ttsEndpoint(a.Region)
	}
	if a.STTEndpoint == "" && a.Region != "" {
		a.STTEndpoint = sttEndpoint(a.Region)
	}
	if a.HTTPClient == nil {
		a.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := a.Models
	if len(models) == 0 {
		models = append(append([]string(nil), DefaultTTSModels...), DefaultSTTModels...)
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, a.model(m))
	}
	return actions
}

// IsSTT reports whether a model name (without the prefix) transcribes.
func IsSTT(model string) bool { return strings.HasPrefix(model, "stt") }

func ttsEndpoint(region string) string {
	return "https://" + region + ".tts.speech.microsoft.com/cognitiveservices/v1"
}

func sttEndpoint(region string) string {
	return "https://" + region + ".api.cognitive.microsoft.com/speechtotext/transcriptions:transcribe?api-version=2024-11-15"
}

func (a *Azure) model(name string) api.Action {
	if IsSTT(name) {
		supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
		return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Azure AI Speech " + name, Supports: supports},
			func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
				return a.transcribe(ctx, req, cfg)
			})
	}
	supports := &ai.ModelSupports{Media: false, Multiturn: false, Output: []string{"media"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Azure AI Speech " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return a.synthesize(ctx, req, cfg)
		})
}

func (a *Azure) check(endpoint string) error {
	if a.Key == "" {
		return errors.New("azure: no key (set AZURE_SPEECH_KEY or Azure.Key)")
	}
	if endpoint == "" {
		return errors.New("azure: no region (set AZURE_SPEECH_REGION or Azure.Region, or the endpoint fields)")
	}
	return nil
}

func (a *Azure) synthesize(ctx context.Context, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if err := a.check(a.TTSEndpoint); err != nil {
		return nil, err
	}
	text := promptText(req)
	if text == "" {
		return nil, errors.New("azure: the request has no text to speak")
	}
	voice := cfg.Voice
	if voice == "" {
		voice = DefaultVoice
	}
	rate := cfg.SampleRate
	if rate <= 0 {
		rate = DefaultSampleRate
	}
	format, err := outputFormat(rate)
	if err != nil {
		return nil, err
	}
	lang := cfg.Language
	if lang == "" {
		lang = voiceLanguage(voice)
	}
	body := ssml(text, voice, lang, cfg.Style, cfg.Rate)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TTSEndpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Ocp-Apim-Subscription-Key", a.Key)
	httpReq.Header.Set("Content-Type", "application/ssml+xml")
	httpReq.Header.Set("X-Microsoft-OutputFormat", format)
	httpReq.Header.Set("User-Agent", "tamesu")
	data, err := a.do(httpReq, 64<<20)
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

// outputFormat maps a PCM rate to Azure's X-Microsoft-OutputFormat value.
func outputFormat(rate int) (string, error) {
	switch rate {
	case 8000:
		return "raw-8khz-16bit-mono-pcm", nil
	case 16000:
		return "raw-16khz-16bit-mono-pcm", nil
	case 22050:
		return "raw-22050hz-16bit-mono-pcm", nil
	case 24000:
		return "raw-24khz-16bit-mono-pcm", nil
	case 44100:
		return "raw-44100hz-16bit-mono-pcm", nil
	case 48000:
		return "raw-48khz-16bit-mono-pcm", nil
	}
	return "", fmt.Errorf("azure: unsupported sample rate %d (8000, 16000, 22050, 24000, 44100 or 48000)", rate)
}

// voiceLanguage derives the SSML language from a voice name: the first two
// dash-separated segments (en-US-AvaMultilingualNeural → en-US), en-US when
// the name has no such shape.
func voiceLanguage(voice string) string {
	parts := strings.SplitN(voice, "-", 3)
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "-" + parts[1]
	}
	return "en-US"
}

// ssml builds the synthesis document: the text (escaped) inside the voice,
// wrapped in express-as and prosody when a style or rate is asked.
func ssml(text, voice, lang, style, rate string) string {
	var b strings.Builder
	b.WriteString("<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis'")
	if style != "" {
		b.WriteString(" xmlns:mstts='https://www.w3.org/2001/mstts'")
	}
	b.WriteString(" xml:lang='" + html.EscapeString(lang) + "'>")
	b.WriteString("<voice name='" + html.EscapeString(voice) + "'>")
	if style != "" {
		b.WriteString("<mstts:express-as style='" + html.EscapeString(style) + "'>")
	}
	if rate != "" {
		b.WriteString("<prosody rate='" + html.EscapeString(rate) + "'>")
	}
	b.WriteString(html.EscapeString(text))
	if rate != "" {
		b.WriteString("</prosody>")
	}
	if style != "" {
		b.WriteString("</mstts:express-as>")
	}
	b.WriteString("</voice></speak>")
	return b.String()
}

// transcription is the part of the fast transcription response tamesu reads.
type transcription struct {
	DurationMilliseconds float64 `json:"durationMilliseconds"`
	CombinedPhrases      []struct {
		Text string `json:"text"`
	} `json:"combinedPhrases"`
	Phrases []struct {
		Confidence float64 `json:"confidence"`
	} `json:"phrases"`
}

func (a *Azure) transcribe(ctx context.Context, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if err := a.check(a.STTEndpoint); err != nil {
		return nil, err
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	definition := map[string]any{}
	if locale := sttLocale(cfg.Language); locale != "" {
		definition["locales"] = []string{locale}
	}
	if cfg.Diarize {
		definition["diarization"] = map[string]any{"maxSpeakers": 2, "enabled": true}
	}
	def, _ := json.Marshal(definition)
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	part, err := w.CreateFormFile("audio", "audio"+extensionFor(mime))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	_ = w.WriteField("definition", string(def))
	if err := w.Close(); err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.STTEndpoint, &form)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Ocp-Apim-Subscription-Key", a.Key)
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	body, err := a.do(httpReq, 8<<20)
	if err != nil {
		return nil, err
	}
	var out transcription
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("azure: decoding the response: %w", err)
	}
	texts := make([]string, 0, len(out.CombinedPhrases))
	for _, p := range out.CombinedPhrases {
		if t := strings.TrimSpace(p.Text); t != "" {
			texts = append(texts, t)
		}
	}
	text := strings.Join(texts, " ")
	var confidence float64
	if len(out.Phrases) > 0 {
		for _, p := range out.Phrases {
			confidence += p.Confidence
		}
		confidence /= float64(len(out.Phrases))
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": out.DurationMilliseconds / 1000, "confidence": confidence},
		},
	}, nil
}

// locales widens a bare language code to the locale Azure's fast
// transcription expects.
var locales = map[string]string{
	"en": "en-US", "es": "es-ES", "fr": "fr-FR", "de": "de-DE", "it": "it-IT",
	"pt": "pt-BR", "nl": "nl-NL", "ja": "ja-JP", "zh": "zh-CN", "ko": "ko-KR",
	"pl": "pl-PL", "ru": "ru-RU", "sv": "sv-SE", "tr": "tr-TR",
}

// sttLocale maps a Config language to a transcription locale: a full locale
// passes through, a known 2-letter code is widened, an empty value stays
// empty (Azure identifies the language).
func sttLocale(lang string) string {
	lang = strings.TrimSpace(lang)
	if len(lang) == 2 {
		if l, ok := locales[strings.ToLower(lang)]; ok {
			return l
		}
	}
	return lang
}

func (a *Azure) do(req *http.Request, limit int64) ([]byte, error) {
	res, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("azure: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("azure: %s: %s", res.Status, strings.TrimSpace(string(data)))
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
	return "", nil, errors.New("azure: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("azure: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("azure: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("azure: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("azure: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
