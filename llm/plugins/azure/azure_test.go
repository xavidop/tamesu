package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ttsPath = "/cognitiveservices/v1"
	sttPath = "/speechtotext/transcriptions:transcribe"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*handler.Load())(w, r)
	}))
	g = genkit.Init(context.Background(), genkit.WithPlugins(&Azure{
		Key:         "az-test",
		TTSEndpoint: srv.URL + ttsPath,
		STTEndpoint: srv.URL + sttPath + "?api-version=2024-11-15",
		HTTPClient:  srv.Client(),
	}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

func TestSynthesize(t *testing.T) {
	g := setup(t)
	pcm := make([]byte, 48000) // one second at 24 kHz
	var got *http.Request
	var body string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write(pcm)
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("azure/tts"),
		ai.WithPrompt("Tom & Jerry <3"), ai.WithConfig(Config{Voice: "es-ES-ElviraNeural", Language: "es-ES"}))
	require.NoError(t, err)
	part := resp.Message.Content[0]
	require.True(t, part.IsMedia())
	assert.Equal(t, "audio/L16;rate=24000", part.ContentType)
	data, err := base64.StdEncoding.DecodeString(part.Text[len("data:audio/L16;rate=24000;base64,"):])
	require.NoError(t, err)
	assert.Equal(t, pcm, data)
	assert.Equal(t, len("Tom & Jerry <3"), resp.Usage.InputCharacters)
	assert.Equal(t, 1, resp.Usage.OutputAudioFiles)
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)

	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, ttsPath, got.URL.Path)
	assert.Equal(t, "az-test", got.Header.Get("Ocp-Apim-Subscription-Key"))
	assert.Equal(t, "application/ssml+xml", got.Header.Get("Content-Type"))
	assert.Equal(t, "raw-24khz-16bit-mono-pcm", got.Header.Get("X-Microsoft-OutputFormat"))
	assert.Equal(t, "tamesu", got.Header.Get("User-Agent"))
	assert.Contains(t, body, "<voice name='es-ES-ElviraNeural'>")
	assert.Contains(t, body, "xml:lang='es-ES'")
	assert.Contains(t, body, "Tom &amp; Jerry &lt;3", "the text is escaped")
	assert.NotContains(t, body, "mstts", "no style, no mstts namespace")
	assert.NotContains(t, body, "<prosody")
}

func TestSynthesizeDefaultsStyleRate(t *testing.T) {
	g := setup(t)
	var got *http.Request
	var body string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write(make([]byte, 3200)) // 0.1 s at 16 kHz
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("azure/tts"),
		ai.WithPrompt("Hi"), ai.WithConfig(Config{SampleRate: 16000, Style: "cheerful", Rate: "+10%"}))
	require.NoError(t, err)
	assert.Equal(t, "audio/L16;rate=16000", resp.Message.Content[0].ContentType)
	assert.InDelta(t, 0.1, resp.Usage.Custom["audio_seconds"], 0.001)
	require.NotNil(t, got)
	assert.Equal(t, "raw-16khz-16bit-mono-pcm", got.Header.Get("X-Microsoft-OutputFormat"))
	assert.Contains(t, body, "<voice name='"+DefaultVoice+"'>", "the default voice when none is named")
	assert.Contains(t, body, "xml:lang='en-US'", "the language is derived from the voice")
	assert.Contains(t, body, "xmlns:mstts='https://www.w3.org/2001/mstts'")
	assert.Contains(t, body, "<mstts:express-as style='cheerful'><prosody rate='+10%'>Hi</prosody></mstts:express-as>")
}

func TestTranscribe(t *testing.T) {
	g := setup(t)
	audio := []byte("RIFF....WAVEfmt fake")
	var got *http.Request
	var definition map[string]any
	var file []byte
	var filename string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		require.NoError(t, r.ParseMultipartForm(1<<20))
		require.NoError(t, json.Unmarshal([]byte(r.FormValue("definition")), &definition))
		f, hdr, err := r.FormFile("audio")
		require.NoError(t, err)
		filename = hdr.Filename
		file, _ = io.ReadAll(f)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"durationMilliseconds": 2500,
			"combinedPhrases": [{"channel": 0, "text": "Hello, I need a refund."}],
			"phrases": [
			  {"channel":0,"offsetMilliseconds":0,"durationMilliseconds":1000,"text":"Hello,","words":[],"locale":"en-US","confidence":0.95},
			  {"channel":0,"offsetMilliseconds":1000,"durationMilliseconds":1500,"text":"I need a refund.","words":[],"locale":"en-US","confidence":0.99}
			]}`))
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("azure/stt"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en"}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])
	assert.InDelta(t, 0.97, resp.Usage.Custom["confidence"], 0.0001)
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	assert.Equal(t, len("Hello, I need a refund."), resp.Usage.OutputCharacters)

	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, sttPath, got.URL.Path)
	assert.Equal(t, "2024-11-15", got.URL.Query().Get("api-version"))
	assert.Equal(t, "az-test", got.Header.Get("Ocp-Apim-Subscription-Key"))
	assert.Equal(t, "application/json", got.Header.Get("Accept"))
	assert.Contains(t, got.Header.Get("Content-Type"), "multipart/form-data")
	assert.Equal(t, map[string]any{"locales": []any{"en-US"}}, definition, "a bare language code is widened")
	assert.Equal(t, "audio.wav", filename)
	assert.Equal(t, audio, file)
}

func TestTranscribeDiarizeAndNoLanguage(t *testing.T) {
	g := setup(t)
	var definition string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		definition = r.FormValue("definition")
		_, _ = w.Write([]byte(`{"durationMilliseconds": 0, "combinedPhrases": [], "phrases": []}`))
	})
	media := ai.NewUserMessage(ai.NewMediaPart("audio/mpeg", base64.StdEncoding.EncodeToString([]byte("mp3"))))

	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("azure/stt"),
		ai.WithMessages(media), ai.WithConfig(Config{Diarize: true, Language: "pt-PT"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"locales":["pt-PT"],"diarization":{"maxSpeakers":2,"enabled":true}}`, definition, "a full locale passes through")
	assert.Equal(t, "", resp.Text())
	assert.Equal(t, 0.0, resp.Usage.Custom["confidence"], "no phrases, no confidence")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("azure/stt"), ai.WithMessages(media))
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, definition, "no language: Azure identifies it")
}

func TestErrors(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"401","message":"Access denied due to invalid subscription key"}}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("azure/tts"), ai.WithPrompt("Hi"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "invalid subscription key")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("azure/stt"), ai.WithPrompt("no audio here"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no audio media part")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("azure/stt"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/a.wav"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data URL")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("azure/tts"), ai.WithMessages(ai.NewUserMessage(ai.NewTextPart("   "))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no text to speak")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("azure/tts"), ai.WithPrompt("Hi"), ai.WithConfig(Config{SampleRate: 11025}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported sample rate 11025")
}

func TestNoKeyNoRegion(t *testing.T) {
	t.Setenv("AZURE_SPEECH_KEY", "")
	t.Setenv("AZURE_SPEECH_REGION", "")
	a := &Azure{HTTPClient: http.DefaultClient}
	actions := a.Init(context.Background())
	assert.Len(t, actions, len(DefaultTTSModels)+len(DefaultSTTModels))
	assert.Empty(t, a.TTSEndpoint)
	assert.Empty(t, a.STTEndpoint)
	speak := &ai.ModelRequest{Messages: []*ai.Message{ai.NewUserTextMessage("hi")}}
	_, err := a.synthesize(context.Background(), speak, Config{})
	assert.ErrorContains(t, err, "no key")
	_, err = a.transcribe(context.Background(), speak, Config{})
	assert.ErrorContains(t, err, "no key")

	a = &Azure{Key: "k", HTTPClient: http.DefaultClient}
	a.Init(context.Background())
	_, err = a.synthesize(context.Background(), speak, Config{})
	assert.ErrorContains(t, err, "no region")
	_, err = a.transcribe(context.Background(), speak, Config{})
	assert.ErrorContains(t, err, "no region")

	assert.True(t, IsSTT("stt"))
	assert.False(t, IsSTT("tts"))
}

func TestEndpointsFromRegion(t *testing.T) {
	t.Setenv("AZURE_SPEECH_KEY", "env-key")
	t.Setenv("AZURE_SPEECH_REGION", "eastus")
	a := &Azure{}
	a.Init(context.Background())
	assert.Equal(t, "env-key", a.Key)
	assert.Equal(t, "https://eastus.tts.speech.microsoft.com/cognitiveservices/v1", a.TTSEndpoint)
	assert.Equal(t, "https://eastus.api.cognitive.microsoft.com/speechtotext/transcriptions:transcribe?api-version=2024-11-15", a.STTEndpoint)
	require.NotNil(t, a.HTTPClient)

	a = &Azure{Region: "westeurope", TTSEndpoint: "http://proxy/tts"}
	a.Init(context.Background())
	assert.Equal(t, "http://proxy/tts", a.TTSEndpoint, "an explicit endpoint wins")
	assert.Equal(t, "https://westeurope.api.cognitive.microsoft.com/speechtotext/transcriptions:transcribe?api-version=2024-11-15", a.STTEndpoint)
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "en-US", voiceLanguage("en-US-AvaMultilingualNeural"))
	assert.Equal(t, "es-MX", voiceLanguage("es-MX-DaliaNeural"))
	assert.Equal(t, "zh-CN", voiceLanguage("zh-CN-XiaoxiaoNeural"))
	assert.Equal(t, "en-US", voiceLanguage("custom"))
	assert.Equal(t, "en-US", voiceLanguage(""))

	assert.Equal(t, "en-US", sttLocale("en"))
	assert.Equal(t, "pt-BR", sttLocale("pt"))
	assert.Equal(t, "ja-JP", sttLocale("JA"))
	assert.Equal(t, "en-GB", sttLocale("en-GB"), "a full locale passes through")
	assert.Equal(t, "eu", sttLocale("eu"), "an unknown code passes through")
	assert.Equal(t, "", sttLocale(""))

	for rate, want := range map[int]string{
		8000: "raw-8khz-16bit-mono-pcm", 16000: "raw-16khz-16bit-mono-pcm", 22050: "raw-22050hz-16bit-mono-pcm",
		24000: "raw-24khz-16bit-mono-pcm", 44100: "raw-44100hz-16bit-mono-pcm", 48000: "raw-48khz-16bit-mono-pcm",
	} {
		got, err := outputFormat(rate)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := outputFormat(12000)
	assert.ErrorContains(t, err, "unsupported sample rate 12000")

	assert.Equal(t, "<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'><voice name='en-US-AvaMultilingualNeural'>Hello there</voice></speak>",
		ssml("Hello there", "en-US-AvaMultilingualNeural", "en-US", "", ""))
}
