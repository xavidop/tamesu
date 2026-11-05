package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveVoice(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		hint     string
		want     Voice
	}{
		{"gemini female with language", "googleai", "en-GB-female", Voice{Name: "Kore", Language: "en-GB", Gender: "female"}},
		{"gemini male two-letter language", "googleai", "es-male", Voice{Name: "Puck", Language: "es", Gender: "male"}},
		{"gemini named voice, any case", "googleai", "zephyr", Voice{Name: "Zephyr"}},
		{"gemini default", "googleai", "", Voice{Name: "Kore"}},
		{"elevenlabs gender", "elevenlabs", "female", Voice{Name: "Rachel", Gender: "female"}},
		{"elevenlabs id passes through", "elevenlabs", "21m00Tcm4TlvDq8ikWAM", Voice{Name: "21m00Tcm4TlvDq8ikWAM"}},
		{"openai male", "openai", "en-US-male", Voice{Name: "onyx", Language: "en-US", Gender: "male"}},
		{"fake keeps the gender word", "fake", "fr-FR-female", Voice{Name: "female", Language: "fr-FR", Gender: "female"}},
		{"neutral", "openai", "en-neutral", Voice{Name: "alloy", Language: "en"}},
		{"deepgram male", "deepgram", "en-US-male", Voice{Name: "apollo", Language: "en-US", Gender: "male"}},
		{"deepgram named aura voice passes through", "deepgram", "aura-2-celeste-es", Voice{Name: "aura-2-celeste-es"}},
		{"azure female", "azure", "es-ES-female", Voice{Name: "en-US-AvaMultilingualNeural", Language: "es-ES", Gender: "female"}},
		{"azure named voice passes through", "azure", "es-ES-ElviraNeural", Voice{Name: "es-ES-ElviraNeural"}},
		{"rime default", "rime", "", Voice{Name: "luna"}},
		{"rime male", "rime", "male", Voice{Name: "orion", Gender: "male"}},
		{"google female", "google", "en-GB-female", Voice{Name: "Aoede", Language: "en-GB", Gender: "female"}},
		{"google full voice passes through", "google", "en-US-Neural2-F", Voice{Name: "en-US-Neural2-F"}},
		{"unknown provider named voice", "acme", "Bob", Voice{Name: "Bob"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolveVoice(tt.provider, tt.hint))
		})
	}
}

func TestTTSConfigAndDecode(t *testing.T) {
	cfg := ttsConfig("googleai", "Kore", "en-GB")
	assert.Equal(t, []string{"AUDIO"}, cfg["responseModalities"])
	cfg = ttsConfig("elevenlabs", "Rachel", "es-ES")
	assert.Equal(t, "es", cfg["language"], "ElevenLabs takes the ISO 639-1 code")
	assert.Equal(t, "pcm_24000", cfg["output_format"])
	cfg = ttsConfig("openai", "nova", "en")
	assert.Equal(t, "pcm", cfg["response_format"])
	cfg = ttsConfig("deepgram", "apollo", "en")
	assert.Equal(t, "apollo", cfg["voice"])
	assert.Equal(t, 24000, cfg["sample_rate"], "Aura voices carry their own language; the hint's is not sent")
	cfg = ttsConfig("azure", "en-US-AvaMultilingualNeural", "es-ES")
	assert.Equal(t, "es-ES", cfg["language"], "Azure takes the locale as the SSML language")
	cfg = ttsConfig("rime", "luna", "es-ES")
	assert.Equal(t, "es-ES", cfg["language"], "the Rime plugin maps the tag to its own codes")
	assert.Equal(t, "en", sttConfig("openai", "en")["language"])
	assert.Equal(t, "en-GB", sttConfig("azure", "en-GB")["language"])
	assert.Equal(t, "en", sttConfig("assemblyai", "en")["language"])
	assert.Equal(t, "en", sttConfig("elevenlabs", "en")["language"])
	cfg = ttsConfig("google", "Aoede", "en-GB")
	assert.Equal(t, "en-GB", cfg["language"], "the Google plugin builds en-GB-Chirp3-HD-Aoede from it")
	assert.Equal(t, "en-US", sttConfig("google", "en-US")["language"])
	assert.Equal(t, "en-GB", sttConfig("speechmatics", "en-GB")["language"], "the plugin maps the tag to its codes")
	assert.Equal(t, "fr-FR", sttConfig("gladia", "fr-FR")["language"])
	_, has := sttConfig("googleai", "en")["language"]
	assert.False(t, has, "general models take the language in the prompt")

	pcm, err := decodeAudioPart("audio/L16;rate=16000", "data:audio/L16;rate=16000;base64,AAABAP//")
	assert.NoError(t, err)
	assert.Equal(t, 16000, pcm.Rate)
	assert.Equal(t, []int16{0, 1, -1}, pcm.Samples)
	_, err = decodeAudioPart("audio/mpeg", "data:audio/mpeg;base64,AAAA")
	assert.ErrorContains(t, err, "unsupported audio type")
	_, err = decodeAudioPart("audio/pcm", "data:audio/pcm;base64,AAAA")
	assert.ErrorContains(t, err, "no rate parameter")
	assert.Equal(t, []int16{1, -2}, bytesToPCM(pcmToBytes([]int16{1, -2})))
	assert.Equal(t, []int16{150}, downmix([]int16{100, 200}))
}

func TestSpeechPricing(t *testing.T) {
	p := loadPricing()
	assert.InDelta(t, 0.0077, p.costOf("deepgram/nova-3", 0, 0, 0, 60), 1e-9)
	assert.InDelta(t, 0.015, p.costOf("elevenlabs/eleven_flash_v2_5", 0, 0, 100, 0), 1e-9)
	assert.InDelta(t, 0.03, p.costOf("elevenlabs/eleven_multilingual_v2", 0, 0, 100, 0), 1e-9)
	assert.InDelta(t, 0.0067, p.costOf("elevenlabs/scribe_v1", 0, 0, 0, 60), 1e-9, "Scribe is per minute under a per-character provider")
	assert.InDelta(t, 0.003, p.costOf("deepgram/aura-2-thalia-en", 0, 0, 100, 0), 1e-9, "Aura is per character under a per-minute provider")
	assert.InDelta(t, 0.0015, p.costOf("deepgram/aura-asteria-en", 0, 0, 100, 0), 1e-9)
	assert.InDelta(t, 0.0015, p.costOf("openai/gpt-4o-mini-tts", 0, 0, 100, 0), 1e-9)
	assert.InDelta(t, 0.006, p.costOf("openai/gpt-4o-transcribe", 0, 0, 0, 60), 1e-9)
	assert.InDelta(t, 0.0015, p.costOf("azure/tts", 0, 0, 100, 0), 1e-9)
	assert.InDelta(t, 0.006, p.costOf("azure/stt", 0, 0, 0, 60), 1e-9)
	assert.InDelta(t, 0.0025, p.costOf("assemblyai/universal", 0, 0, 0, 60), 1e-9)
	assert.InDelta(t, 0.01, p.costOf("rime/arcana", 0, 0, 100, 0), 1e-9)
	assert.InDelta(t, 0.003, p.costOf("google/chirp3-hd", 0, 0, 100, 0), 1e-9)
	assert.InDelta(t, 0.016, p.costOf("google/chirp_3", 0, 0, 0, 60), 1e-9)
	assert.InDelta(t, 0.0083, p.costOf("speechmatics/enhanced", 0, 0, 0, 60), 1e-9)
	assert.InDelta(t, 0.005, p.costOf("speechmatics/standard", 0, 0, 0, 60), 1e-9)
	assert.InDelta(t, 0.0102, p.costOf("gladia/solaria-1", 0, 0, 0, 60), 1e-9)
	assert.Equal(t, 0.0, p.costOf("acme/unknown", 1000, 1000, 100, 60))
}
