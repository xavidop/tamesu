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
	assert.Equal(t, 0.0, p.costOf("acme/unknown", 1000, 1000, 100, 60))
}
