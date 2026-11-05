// Package googlespeech is a Genkit model plugin for Google Cloud speech: text
// to speech with Cloud Text-to-Speech (google/chirp3-hd, google/neural2,
// google/studio, google/wavenet and google/standard take the request's text and
// answer with an audio media part, a data URL of 16-bit PCM) and speech to text
// with Cloud Speech-to-Text v2 (google/chirp_3, google/chirp_2, google/chirp,
// google/long, google/short and google/telephony take an audio media part and
// answer with the transcript). It is an ordinary Genkit plugin, usable in any
// Genkit Go app next to Genkit's googlegenai plugin: that plugin owns the
// googleai/ prefix, this one the google/ prefix, and it is registered under the
// name googlespeech so both coexist in one registry:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&googlespeech.GoogleSpeech{APIKey: key, Project: "my-project"}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("google/chirp3-hd"),
//	    ai.WithPrompt("Hello there"), ai.WithConfig(googlespeech.Config{Voice: "Kore", Language: "en-GB"}))
//
// Cloud Text-to-Speech has no model parameter: the voice name encodes the
// family (en-US-Chirp3-HD-Aoede, en-US-Neural2-F, en-US-Studio-O,
// en-US-Wavenet-D, en-US-Standard-C), so the tamesu model is the family and
// the Config's Voice is either a bare Chirp 3 HD speaker completed with the
// language and family, or a full voice name used as is. Speech goes to POST
// /v1/text:synthesize on texttospeech.googleapis.com as LINEAR16 (a WAV whose
// header is stripped); transcription to POST
// /v2/projects/{project}/locations/{location}/recognizers/_:recognize on
// speech.googleapis.com (or <location>-speech.googleapis.com) with the audio
// inline, which the API limits to about 10 MB and one minute of audio. Every
// request carries the x-goog-api-key header. The audio duration is reported in
// the usage's Custom map (audio_seconds), and the transcriber's confidence
// (confidence) when the API gives one.
package googlespeech

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
)

// Provider is the prefix of the models (google/<model>).
const Provider = "google"

// PluginName is the plugin's name in the Genkit registry, distinct from the
// googlegenai plugin's so both can be initialised together.
const PluginName = "googlespeech"

// DefaultTTSBaseURL is the Cloud Text-to-Speech API.
const DefaultTTSBaseURL = "https://texttospeech.googleapis.com"

// DefaultTTSModels are the text-to-speech voice families defined when Models
// is empty. Cloud Text-to-Speech has no model parameter; the family is the
// middle of the voice name (en-US-Chirp3-HD-Aoede, en-US-Neural2-F, ...).
var DefaultTTSModels = []string{"chirp3-hd", "neural2", "studio", "wavenet", "standard"}

// DefaultSTTModels are the Speech-to-Text v2 models defined when Models is
// empty.
var DefaultSTTModels = []string{"chirp_3", "chirp_2", "chirp", "long", "short", "telephony"}

// DefaultVoice is the Chirp 3 HD speaker used when a request names none.
const DefaultVoice = "Aoede"

// DefaultLanguage is the BCP-47 language of a bare voice when the request
// names none.
const DefaultLanguage = "en-US"

// DefaultSampleRate is the PCM rate asked of Cloud Text-to-Speech when the
// request names none.
const DefaultSampleRate = 24000

// Speakers are the Chirp 3 HD speakers a bare Voice can name.
var Speakers = []string{"Aoede", "Kore", "Leda", "Zephyr", "Charon", "Fenrir", "Orus", "Puck"}

// families maps a text-to-speech model to the token in the voice name.
var families = map[string]string{
	"chirp3-hd": "Chirp3-HD",
	"neural2":   "Neural2",
	"studio":    "Studio",
	"wavenet":   "Wavenet",
	"standard":  "Standard",
}

// GoogleSpeech is the plugin.
type GoogleSpeech struct {
	// APIKey authenticates; GOOGLE_CLOUD_API_KEY, then GOOGLE_API_KEY, when
	// empty.
	APIKey string
	// Project is the Google Cloud project whose recognizers transcribe;
	// GOOGLE_CLOUD_PROJECT when empty. Speech to text only.
	Project string
	// Location is the Speech-to-Text location (global, us, eu, ...);
	// GOOGLE_CLOUD_LOCATION, then global, when empty. Speech to text only.
	Location string
	// TTSBaseURL overrides the Cloud Text-to-Speech endpoint (tests, proxies).
	TTSBaseURL string
	// STTBaseURL overrides the Speech-to-Text endpoint (tests, proxies);
	// derived from Location by DefaultSTTBaseURL when empty.
	STTBaseURL string
	// HTTPClient overrides the client (default: 60 s timeout).
	HTTPClient *http.Client
	// Models to define, without the provider prefix. The text-to-speech
	// families (DefaultTTSModels) synthesise, every other name is a
	// Speech-to-Text v2 model.
	Models []string
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Voice is a bare Chirp 3 HD speaker (one of Speakers, default Aoede)
	// completed with Language and the model's family, or a full Cloud voice
	// name containing "-" (en-US-Neural2-F) used as is. Text to speech.
	Voice string `json:"voice,omitempty"`
	// Language is a BCP-47 code. Text to speech: the language of a bare voice
	// (default en-US). Speech to text: the language of the audio; empty lets
	// the Chirp models detect it (auto), which long, short and telephony do
	// not accept.
	Language string `json:"language,omitempty"`
	// SampleRate is the PCM rate in hertz of the synthesised audio (default
	// 24000). Text to speech.
	SampleRate int `json:"sample_rate,omitempty"`
	// SpeakingRate is 0.25..2.0 around 1. Text to speech.
	SpeakingRate *float64 `json:"speaking_rate,omitempty"`
	// Pitch is -20.0..20.0 semitones around 0; the Chirp 3 HD voices reject
	// it. Text to speech.
	Pitch *float64 `json:"pitch,omitempty"`
}

// Name implements api.Plugin.
func (p *GoogleSpeech) Name() string { return PluginName }

// Init implements api.Plugin.
func (p *GoogleSpeech) Init(context.Context) []api.Action {
	if p.APIKey == "" {
		p.APIKey = os.Getenv("GOOGLE_CLOUD_API_KEY")
	}
	if p.APIKey == "" {
		p.APIKey = os.Getenv("GOOGLE_API_KEY")
	}
	if p.Project == "" {
		p.Project = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if p.Location == "" {
		p.Location = os.Getenv("GOOGLE_CLOUD_LOCATION")
	}
	if p.Location == "" {
		p.Location = "global"
	}
	if p.TTSBaseURL == "" {
		p.TTSBaseURL = DefaultTTSBaseURL
	}
	if p.STTBaseURL == "" {
		p.STTBaseURL = DefaultSTTBaseURL(p.Location)
	}
	if p.HTTPClient == nil {
		p.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := p.Models
	if len(models) == 0 {
		models = append(append([]string(nil), DefaultTTSModels...), DefaultSTTModels...)
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, p.model(m))
	}
	return actions
}

// DefaultSTTBaseURL is the Speech-to-Text endpoint of a location:
// speech.googleapis.com for global, <location>-speech.googleapis.com
// otherwise.
func DefaultSTTBaseURL(location string) string {
	if location == "" || location == "global" {
		return "https://speech.googleapis.com"
	}
	return "https://" + location + "-speech.googleapis.com"
}

// IsTTS reports whether a model name (without the prefix) is a text-to-speech
// voice family; every other name transcribes.
func IsTTS(model string) bool {
	_, ok := families[model]
	return ok
}

// VoiceName completes a Config's voice for a text-to-speech model: a voice
// containing "-" is a full Cloud voice name, used as is, and its language is
// its first two segments (en-US-Neural2-F is en-US); otherwise the voice (a
// bare speaker, default Aoede) is joined with the language (default en-US)
// and the model's family token (en-US-Chirp3-HD-Aoede).
func VoiceName(model, voice, lang string) (name, languageCode string) {
	if strings.Contains(voice, "-") {
		parts := strings.SplitN(voice, "-", 3)
		return voice, strings.Join(parts[:min(2, len(parts))], "-")
	}
	token, ok := families[model]
	if !ok {
		token = families["chirp3-hd"]
	}
	if lang == "" {
		lang = DefaultLanguage
	}
	if voice == "" {
		voice = DefaultVoice
	}
	return lang + "-" + token + "-" + voice, lang
}

func (p *GoogleSpeech) model(name string) api.Action {
	if IsTTS(name) {
		supports := &ai.ModelSupports{Media: false, Multiturn: false, Output: []string{"media"}}
		return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Google " + name, Supports: supports},
			func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
				return p.synthesize(ctx, name, req, cfg)
			})
	}
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Google " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return p.recognize(ctx, name, req, cfg)
		})
}

func (p *GoogleSpeech) synthesize(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if p.APIKey == "" {
		return nil, errors.New("google: no API key (set GOOGLE_CLOUD_API_KEY or GoogleSpeech.APIKey)")
	}
	text := promptText(req)
	if text == "" {
		return nil, errors.New("google: the request has no text to speak")
	}
	name, languageCode := VoiceName(model, cfg.Voice, cfg.Language)
	rate := cfg.SampleRate
	if rate <= 0 {
		rate = DefaultSampleRate
	}
	audioConfig := map[string]any{"audioEncoding": "LINEAR16", "sampleRateHertz": rate}
	if cfg.SpeakingRate != nil {
		audioConfig["speakingRate"] = *cfg.SpeakingRate
	}
	if cfg.Pitch != nil {
		audioConfig["pitch"] = *cfg.Pitch
	}
	body := map[string]any{
		"input":       map[string]any{"text": text},
		"voice":       map[string]any{"languageCode": languageCode, "name": name},
		"audioConfig": audioConfig,
	}
	payload, _ := json.Marshal(body)
	httpReq, err := p.request(ctx, strings.TrimRight(p.TTSBaseURL, "/")+"/v1/text:synthesize", payload)
	if err != nil {
		return nil, err
	}
	data, err := p.do(httpReq, 64<<20)
	if err != nil {
		return nil, err
	}
	var out struct {
		AudioContent string `json:"audioContent"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("google: decoding the response: %w", err)
	}
	if out.AudioContent == "" {
		return nil, errors.New("google: the response has no audioContent")
	}
	wav, err := base64.StdEncoding.DecodeString(out.AudioContent)
	if err != nil {
		return nil, fmt.Errorf("google: decoding the audio: %w", err)
	}
	pcm, wavRate := wavPCM(wav)
	if wavRate > 0 {
		rate = wavRate
	}
	mime := fmt.Sprintf("audio/L16;rate=%d", rate)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewMediaPart(mime, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(pcm))),
		Usage: &ai.GenerationUsage{
			InputCharacters:  len(text),
			OutputAudioFiles: 1,
			Custom:           map[string]float64{"audio_seconds": float64(len(pcm)) / 2 / float64(rate)},
		},
	}, nil
}

// recognition is the part of the recognize response tamesu reads: the top
// alternative of every result and the billed duration.
type recognition struct {
	Results []struct {
		Alternatives []struct {
			Transcript string   `json:"transcript"`
			Confidence *float64 `json:"confidence"`
		} `json:"alternatives"`
	} `json:"results"`
	Metadata struct {
		TotalBilledDuration string `json:"totalBilledDuration"`
	} `json:"metadata"`
}

func (p *GoogleSpeech) recognize(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if p.APIKey == "" {
		return nil, errors.New("google: no API key (set GOOGLE_CLOUD_API_KEY or GoogleSpeech.APIKey)")
	}
	if p.Project == "" {
		return nil, errors.New("google: no project (set GOOGLE_CLOUD_PROJECT or GoogleSpeech.Project)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	languages := []string{"auto"}
	if cfg.Language != "" {
		languages = []string{cfg.Language}
	}
	config := map[string]any{
		"model":         model,
		"languageCodes": languages,
		"features":      map[string]any{"enableAutomaticPunctuation": true},
	}
	// Header-less PCM (audio/L16;rate=N, what tamesu's TTS plugins produce)
	// cannot be sniffed, so it is described; everything else is a container
	// the API detects.
	if rate := pcmRate(mime); rate > 0 {
		config["explicitDecodingConfig"] = map[string]any{"encoding": "LINEAR16", "sampleRateHertz": rate, "audioChannelCount": 1}
	} else {
		config["autoDecodingConfig"] = map[string]any{}
	}
	payload, _ := json.Marshal(map[string]any{"config": config, "content": base64.StdEncoding.EncodeToString(data)})
	location := p.Location
	if location == "" {
		location = "global"
	}
	url := fmt.Sprintf("%s/v2/projects/%s/locations/%s/recognizers/_:recognize", strings.TrimRight(p.STTBaseURL, "/"), p.Project, location)
	httpReq, err := p.request(ctx, url, payload)
	if err != nil {
		return nil, err
	}
	body, err := p.do(httpReq, 8<<20)
	if err != nil {
		return nil, err
	}
	var out recognition
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("google: decoding the response: %w", err)
	}
	var texts []string
	var confidence float64
	var scored int
	for _, r := range out.Results {
		if len(r.Alternatives) == 0 {
			continue
		}
		top := r.Alternatives[0]
		if t := strings.TrimSpace(top.Transcript); t != "" {
			texts = append(texts, t)
		}
		if top.Confidence != nil {
			confidence += *top.Confidence
			scored++
		}
	}
	text := strings.Join(texts, " ")
	usage := &ai.GenerationUsage{InputAudioFiles: 1, OutputCharacters: len(text), Custom: map[string]float64{}}
	seconds := billedSeconds(out.Metadata.TotalBilledDuration)
	if seconds == 0 && isWAV(mime) {
		seconds = wavSeconds(data)
	}
	usage.Custom["audio_seconds"] = seconds
	if scored > 0 {
		usage.Custom["confidence"] = confidence / float64(scored)
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage:        usage,
	}, nil
}

func (p *GoogleSpeech) request(ctx context.Context, url string, payload []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (p *GoogleSpeech) do(req *http.Request, limit int64) ([]byte, error) {
	res, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("google: %s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// billedSeconds parses a protobuf JSON duration ("12.5s"); 0 when empty or
// malformed.
func billedSeconds(d string) float64 {
	if d == "" {
		return 0
	}
	dur, err := time.ParseDuration(d)
	if err != nil {
		return 0
	}
	return dur.Seconds()
}

// pcmRate reads the rate of a header-less PCM content type (audio/L16;rate=N);
// 0 for anything else.
func pcmRate(mime string) int {
	mt, params, _ := strings.Cut(strings.ToLower(mime), ";")
	if strings.TrimSpace(mt) != "audio/l16" {
		return 0
	}
	for _, kv := range strings.Split(params, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		if k == "rate" {
			rate, _ := strconv.Atoi(strings.TrimSpace(v))
			return rate
		}
	}
	return 0
}

func isWAV(mime string) bool {
	mt, _, _ := strings.Cut(strings.ToLower(mime), ";")
	return mt == "audio/wav" || mt == "audio/x-wav" || mt == "audio/wave"
}

// wavPCM strips a WAV's RIFF header by walking its chunks to the data chunk,
// returning the samples and the fmt chunk's rate. Bytes that are not a WAV
// come back unchanged with rate 0.
func wavPCM(data []byte) ([]byte, int) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return data, 0
	}
	var rate int
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		switch id {
		case "fmt ":
			if pos+16 > len(data) {
				return data, 0
			}
			rate = int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		case "data":
			if pos+size > len(data) {
				size = len(data) - pos
			}
			return data[pos : pos+size], rate
		}
		pos += size + size%2
	}
	return data, 0
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
	return "", nil, errors.New("google: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("google: malformed data URL")
		}
		// The media type keeps its parameters (audio/L16;rate=8000 is
		// nothing without the rate); only the base64 marker goes.
		var kept []string
		isBase64 := false
		for _, seg := range strings.Split(meta, ";") {
			if seg = strings.TrimSpace(seg); seg == "base64" {
				isBase64 = true
			} else if seg != "" {
				kept = append(kept, seg)
			}
		}
		if len(kept) > 0 {
			mime = strings.Join(kept, ";")
		}
		if !isBase64 {
			return "", nil, errors.New("google: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("google: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("google: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}
