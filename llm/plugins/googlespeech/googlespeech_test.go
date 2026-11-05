package googlespeech

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One Genkit instance per process.
var (
	g       *genkit.Genkit
	handler atomic.Pointer[http.HandlerFunc]
)

func setup(t *testing.T) *genkit.Genkit {
	t.Helper()
	if g != nil {
		return g
	}
	// The server outlives the first test: genkit.Init runs once per process.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*handler.Load())(w, r)
	}))
	g = genkit.Init(context.Background(), genkit.WithPlugins(&GoogleSpeech{
		APIKey: "gk-test", Project: "proj-1", Location: "eu",
		TTSBaseURL: srv.URL, STTBaseURL: srv.URL, HTTPClient: srv.Client(),
	}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

func TestSynthesize(t *testing.T) {
	g := setup(t)
	audio := wav(24000, 48000) // one second at 24 kHz, 16-bit, with a LIST chunk before data
	var got *http.Request
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"audioContent": base64.StdEncoding.EncodeToString(audio)})
	})
	speaking := 1.2
	pitch := -2.0
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("google/chirp3-hd"),
		ai.WithPrompt("Hello there"), ai.WithConfig(Config{Voice: "Kore", Language: "en-GB", SpeakingRate: &speaking, Pitch: &pitch}))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/text:synthesize", got.URL.Path)
	assert.Equal(t, "gk-test", got.Header.Get("x-goog-api-key"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.Equal(t, map[string]any{"text": "Hello there"}, body["input"])
	assert.Equal(t, map[string]any{"languageCode": "en-GB", "name": "en-GB-Chirp3-HD-Kore"}, body["voice"])
	assert.Equal(t, map[string]any{"audioEncoding": "LINEAR16", "sampleRateHertz": 24000.0, "speakingRate": 1.2, "pitch": -2.0}, body["audioConfig"])

	var media *ai.Part
	for _, p := range resp.Message.Content {
		if p.IsMedia() {
			media = p
		}
	}
	require.NotNil(t, media, "the answer is an audio media part")
	assert.Equal(t, "audio/L16;rate=24000", media.ContentType)
	assert.True(t, strings.HasPrefix(media.Text, "data:audio/L16;rate=24000;base64,"))
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(media.Text, "data:audio/L16;rate=24000;base64,"))
	require.NoError(t, err)
	assert.Len(t, data, 48000, "the WAV header and the LIST chunk are stripped")
	assert.Equal(t, 11, resp.Usage.InputCharacters)
	assert.Equal(t, 1, resp.Usage.OutputAudioFiles)
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)

	// Defaults: en-US Aoede, 24 kHz, no optional fields; a full voice name
	// is used as is and names its own language.
	body = nil
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/neural2"), ai.WithPrompt("hi"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"languageCode": "en-US", "name": "en-US-Neural2-Aoede"}, body["voice"])
	assert.Equal(t, map[string]any{"audioEncoding": "LINEAR16", "sampleRateHertz": 24000.0}, body["audioConfig"])

	audio = wav(16000, 8000) // a quarter second at 16 kHz
	body = nil
	resp, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/wavenet"), ai.WithPrompt("hi"),
		ai.WithConfig(Config{Voice: "de-DE-Wavenet-B", Language: "en-US", SampleRate: 16000}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"languageCode": "de-DE", "name": "de-DE-Wavenet-B"}, body["voice"])
	assert.Equal(t, 16000.0, body["audioConfig"].(map[string]any)["sampleRateHertz"])
	media = resp.Message.Content[0]
	assert.Equal(t, "audio/L16;rate=16000", media.ContentType)
	assert.InDelta(t, 0.25, resp.Usage.Custom["audio_seconds"], 0.001)
}

func TestRecognize(t *testing.T) {
	g := setup(t)
	audio := wav(16000, 32000) // one second
	var got *http.Request
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"alternatives":[{"transcript":"Hello, I need a refund. ","confidence":0.9},{"transcript":"hello","confidence":0.1}]},
			{"alternatives":[]},
			{"alternatives":[{"transcript":"My order is late.","confidence":0.7}]}],
			"metadata":{"totalBilledDuration":"12.5s"}}`))
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("google/chirp_3"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-GB"}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund. My order is late.", resp.Text(), "top alternatives joined with a space")
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	assert.Equal(t, len("Hello, I need a refund. My order is late."), resp.Usage.OutputCharacters)
	assert.InDelta(t, 0.8, resp.Usage.Custom["confidence"], 1e-9, "mean of the top alternatives' confidences")
	assert.InDelta(t, 12.5, resp.Usage.Custom["audio_seconds"], 1e-9, "totalBilledDuration wins over the WAV header")

	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v2/projects/proj-1/locations/eu/recognizers/_:recognize", got.URL.Path)
	assert.Equal(t, "gk-test", got.Header.Get("x-goog-api-key"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	config := body["config"].(map[string]any)
	assert.Equal(t, "chirp_3", config["model"])
	assert.Equal(t, []any{"en-GB"}, config["languageCodes"])
	assert.Equal(t, map[string]any{}, config["autoDecodingConfig"])
	_, explicit := config["explicitDecodingConfig"]
	assert.False(t, explicit)
	assert.Equal(t, map[string]any{"enableAutomaticPunctuation": true}, config["features"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(audio), body["content"])

	// No language → auto; no confidence or billed duration → the WAV header
	// gives the seconds and confidence is left out.
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"results":[{"alternatives":[{"transcript":" Our return window is 30 days. "}]}]}`))
	})
	resp, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/long"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))))
	require.NoError(t, err)
	assert.Equal(t, []any{"auto"}, body["config"].(map[string]any)["languageCodes"])
	assert.Equal(t, "long", body["config"].(map[string]any)["model"])
	assert.Equal(t, "Our return window is 30 days.", resp.Text())
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)
	_, hasConfidence := resp.Usage.Custom["confidence"]
	assert.False(t, hasConfidence)

	// Header-less PCM is described to the API instead of auto-detected.
	resp, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/chirp_2"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/L16;rate=8000", "data:audio/L16;rate=8000;base64,AAAA"))))
	require.NoError(t, err)
	config = body["config"].(map[string]any)
	_, auto := config["autoDecodingConfig"]
	assert.False(t, auto)
	assert.Equal(t, map[string]any{"encoding": "LINEAR16", "sampleRateHertz": 8000.0, "audioChannelCount": 1.0}, config["explicitDecodingConfig"])
	assert.Equal(t, 0.0, resp.Usage.Custom["audio_seconds"], "no billed duration and not a WAV")
}

func TestErrors(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":403,"message":"API key not valid. Please pass a valid API key."}}`, http.StatusForbidden)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("google/studio"), ai.WithPrompt("hola"))
	assert.ErrorContains(t, err, "403")
	assert.ErrorContains(t, err, "API key not valid")
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/short"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	assert.ErrorContains(t, err, "403")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/standard"), ai.WithPrompt("   "))
	assert.ErrorContains(t, err, "no text to speak")
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/telephony"), ai.WithPrompt("no audio here"))
	assert.ErrorContains(t, err, "no audio media part")
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/chirp"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/a.wav"))))
	assert.ErrorContains(t, err, "data URL")

	// A response without audio is an error, not an empty media part.
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("google/chirp3-hd"), ai.WithPrompt("hola"))
	assert.ErrorContains(t, err, "no audioContent")

	// The project and the key are checked before any request is made.
	req := &ai.ModelRequest{Messages: []*ai.Message{ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))}}
	p := &GoogleSpeech{APIKey: "gk-test", HTTPClient: &http.Client{}}
	p.Init(context.Background())
	_, err = p.recognize(context.Background(), "chirp_3", req, Config{})
	assert.EqualError(t, err, "google: no project (set GOOGLE_CLOUD_PROJECT or GoogleSpeech.Project)")
	p = &GoogleSpeech{Project: "proj-1", HTTPClient: &http.Client{}}
	t.Setenv("GOOGLE_CLOUD_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	p.Init(context.Background())
	_, err = p.recognize(context.Background(), "chirp_3", req, Config{})
	assert.EqualError(t, err, "google: no API key (set GOOGLE_CLOUD_API_KEY or GoogleSpeech.APIKey)")
	_, err = p.synthesize(context.Background(), "chirp3-hd", &ai.ModelRequest{Messages: []*ai.Message{ai.NewUserMessage(ai.NewTextPart("hi"))}}, Config{})
	assert.EqualError(t, err, "google: no API key (set GOOGLE_CLOUD_API_KEY or GoogleSpeech.APIKey)")
}

func TestInitDefaults(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "fallback-key")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	p := &GoogleSpeech{}
	actions := p.Init(context.Background())
	assert.Equal(t, "fallback-key", p.APIKey, "GOOGLE_API_KEY is the second fallback")
	assert.Equal(t, "env-project", p.Project)
	assert.Equal(t, "global", p.Location)
	assert.Equal(t, DefaultTTSBaseURL, p.TTSBaseURL)
	assert.Equal(t, "https://speech.googleapis.com", p.STTBaseURL)
	assert.Len(t, actions, len(DefaultTTSModels)+len(DefaultSTTModels))
	assert.Equal(t, PluginName, p.Name())

	t.Setenv("GOOGLE_CLOUD_API_KEY", "cloud-key")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us")
	p = &GoogleSpeech{Models: []string{"chirp_3"}}
	actions = p.Init(context.Background())
	assert.Equal(t, "cloud-key", p.APIKey, "GOOGLE_CLOUD_API_KEY comes first")
	assert.Equal(t, "us", p.Location)
	assert.Equal(t, "https://us-speech.googleapis.com", p.STTBaseURL, "the regional endpoint follows the location")
	assert.Len(t, actions, 1)

	p = &GoogleSpeech{Location: "asia-southeast1", STTBaseURL: "http://proxy"}
	p.Init(context.Background())
	assert.Equal(t, "http://proxy", p.STTBaseURL, "an explicit endpoint is kept")
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "https://speech.googleapis.com", DefaultSTTBaseURL("global"))
	assert.Equal(t, "https://speech.googleapis.com", DefaultSTTBaseURL(""))
	assert.Equal(t, "https://eu-speech.googleapis.com", DefaultSTTBaseURL("eu"))
	assert.Equal(t, "https://us-central1-speech.googleapis.com", DefaultSTTBaseURL("us-central1"))

	for _, m := range DefaultTTSModels {
		assert.True(t, IsTTS(m), m)
	}
	for _, m := range DefaultSTTModels {
		assert.False(t, IsTTS(m), m)
	}
	assert.False(t, IsTTS("chirp3-hd-custom"))

	name, lang := VoiceName("chirp3-hd", "", "")
	assert.Equal(t, "en-US-Chirp3-HD-Aoede", name)
	assert.Equal(t, "en-US", lang)
	name, lang = VoiceName("neural2", "F", "es-ES")
	assert.Equal(t, "es-ES-Neural2-F", name)
	assert.Equal(t, "es-ES", lang)
	name, lang = VoiceName("studio", "Puck", "")
	assert.Equal(t, "en-US-Studio-Puck", name)
	assert.Equal(t, "en-US", lang)
	name, lang = VoiceName("wavenet", "", "fr-FR")
	assert.Equal(t, "fr-FR-Wavenet-Aoede", name)
	assert.Equal(t, "fr-FR", lang)
	name, lang = VoiceName("standard", "en-US-Neural2-F", "de-DE")
	assert.Equal(t, "en-US-Neural2-F", name, "a full voice name is used as is")
	assert.Equal(t, "en-US", lang, "and names its own language, whatever the Config says")
	name, lang = VoiceName("chirp3-hd", "cmn-CN-Chirp3-HD-Leda", "")
	assert.Equal(t, "cmn-CN-Chirp3-HD-Leda", name)
	assert.Equal(t, "cmn-CN", lang)
	name, lang = VoiceName("unknown", "Kore", "")
	assert.Equal(t, "en-US-Chirp3-HD-Kore", name, "an unknown family falls back to Chirp 3 HD")
	assert.Equal(t, "en-US", lang)

	// WAV stripping walks the chunks: the LIST chunk before data and an odd
	// sized chunk's pad byte are skipped, and a truncated data chunk is cut
	// to what is there.
	pcm, rate := wavPCM(wav(8000, 8000))
	assert.Len(t, pcm, 8000)
	assert.Equal(t, 8000, rate)
	assert.Equal(t, []byte{0, 1, 2, 3}, pcm[:4], "the samples start right after the data chunk header")
	truncated := wav(8000, 8000)[:len(wav(8000, 8000))-100]
	pcm, rate = wavPCM(truncated)
	assert.Len(t, pcm, 7900)
	assert.Equal(t, 8000, rate)
	raw := []byte("not a wav at all")
	pcm, rate = wavPCM(raw)
	assert.Equal(t, raw, pcm, "bytes that are not a WAV come back unchanged")
	assert.Equal(t, 0, rate)
	pcm, rate = wavPCM(nil)
	assert.Nil(t, pcm)
	assert.Equal(t, 0, rate)

	assert.InDelta(t, 0.5, wavSeconds(wav(8000, 8000)), 1e-9, "8000 bytes of 16-bit mono at 8 kHz")
	assert.Equal(t, 0.0, wavSeconds([]byte("not a wav at all")))
	assert.Equal(t, 0.0, wavSeconds(nil))

	assert.Equal(t, 12.5, billedSeconds("12.5s"))
	assert.Equal(t, 3.0, billedSeconds("3s"))
	assert.Equal(t, 0.0, billedSeconds(""))
	assert.Equal(t, 0.0, billedSeconds("twelve"))

	assert.Equal(t, 16000, pcmRate("audio/L16;rate=16000"))
	assert.Equal(t, 8000, pcmRate("audio/l16; rate=8000; channels=1"))
	assert.Equal(t, 0, pcmRate("audio/L16"))
	assert.Equal(t, 0, pcmRate("audio/wav"))

	assert.True(t, isWAV("audio/wav"))
	assert.True(t, isWAV("audio/x-wav; codec=1"))
	assert.False(t, isWAV("audio/L16;rate=16000"))

	mime, data, err := decodePart(ai.NewMediaPart("", "data:audio/ogg;codecs=opus;base64,"+base64.StdEncoding.EncodeToString([]byte("x"))))
	require.NoError(t, err)
	assert.Equal(t, "audio/ogg;codecs=opus", mime, "the media type keeps its parameters, only base64 goes")
	assert.Equal(t, []byte("x"), data)
	mime, _, err = decodePart(ai.NewMediaPart("", "data:audio/L16;rate=8000;base64,AAAA"))
	require.NoError(t, err)
	assert.Equal(t, 8000, pcmRate(mime), "a PCM data URL keeps its rate")
	_, _, err = decodePart(ai.NewMediaPart("", "data:audio/wav,notbase64"))
	assert.Error(t, err)
	mime, data, err = decodePart(ai.NewMediaPart("audio/mpeg", base64.StdEncoding.EncodeToString([]byte("y"))))
	require.NoError(t, err)
	assert.Equal(t, "audio/mpeg", mime, "raw base64 keeps the part's content type")
	assert.Equal(t, []byte("y"), data)
}

// wav builds a 16-bit mono WAV over dataBytes bytes of a counting pattern,
// with an odd sized LIST chunk between fmt and data so the chunk walk is
// exercised (a 44-byte header assumption would fail).
func wav(rate, dataBytes int) []byte {
	const listSize = 13 // odd: a pad byte follows
	b := make([]byte, 12+8+16+8+listSize+1+8+dataBytes)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "LIST")
	binary.LittleEndian.PutUint32(b[40:], listSize)
	copy(b[44:], "INFOISFTtamesu")
	pos := 44 + listSize + 1
	copy(b[pos:], "data")
	binary.LittleEndian.PutUint32(b[pos+4:], uint32(dataBytes))
	for i := range dataBytes {
		b[pos+8+i] = byte(i % 251)
	}
	return b
}
