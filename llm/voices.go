package llm

import (
	"slices"
	"strings"

	"github.com/xavidop/tamesu/llm/plugins/cartesia"
)

// Voice is a resolved voice hint.
type Voice struct {
	// Name is what the provider wants: a Gemini prebuilt voice, an ElevenLabs
	// voice name or ID, a Cartesia voice ID, an OpenAI voice.
	Name string
	// Language is the BCP-47 tag the hint carried, if any (en-GB).
	Language string
	// Gender is female, male or "".
	Gender string
}

// GeminiVoices are the prebuilt voices of the Gemini TTS models.
var GeminiVoices = []string{
	"Zephyr", "Puck", "Charon", "Kore", "Fenrir", "Leda", "Orus", "Aoede", "Callirrhoe", "Autonoe",
	"Enceladus", "Iapetus", "Umbriel", "Algieba", "Despina", "Erinome", "Algenib", "Rasalgethi",
	"Laomedeia", "Achernar", "Alnilam", "Schedar", "Gacrux", "Pulcherrima", "Achird", "Zubenelgenubi",
	"Vindemiatrix", "Sadachbia", "Sadaltager", "Sulafat",
}

// defaultVoices maps a provider and gender to a voice.
var defaultVoices = map[string]map[string]string{
	"googleai":   {"female": "Kore", "male": "Puck", "": "Kore"},
	"vertexai":   {"female": "Kore", "male": "Puck", "": "Kore"},
	"elevenlabs": {"female": "Rachel", "male": "Adam", "": "Rachel"},
	"cartesia":   {"female": cartesia.DefaultVoice, "male": cartesia.DefaultVoice, "": cartesia.DefaultVoice},
	"openai":     {"female": "nova", "male": "onyx", "": "alloy"},
	"fake":       {"female": "female", "male": "male", "": "neutral"},
}

// ResolveVoice turns a persona's voice hint into a provider voice. A hint is
// either a provider voice name (passed through) or `<lang>-<gender>` such as
// en-GB-female, es-ES-male, fr-female or just female, which picks the
// provider's default voice of that gender.
func ResolveVoice(provider, hint string) Voice {
	hint = strings.TrimSpace(hint)
	v := Voice{}
	if hint == "" {
		v.Name = defaultVoices[provider][""]
		return v
	}
	parts := strings.Split(hint, "-")
	last := strings.ToLower(parts[len(parts)-1])
	if last == "female" || last == "male" || last == "neutral" {
		if last != "neutral" {
			v.Gender = last
		}
		v.Language = strings.Join(parts[:len(parts)-1], "-")
		if provider == "fake" {
			v.Name = last
		} else {
			v.Name = defaultVoices[provider][v.Gender]
		}
		if v.Name == "" {
			v.Name = defaultVoices[provider][""]
		}
		return v
	}
	// A provider voice name: keep it, with the canonical case for Gemini.
	v.Name = hint
	if provider == "googleai" || provider == "vertexai" {
		if i := slices.IndexFunc(GeminiVoices, func(n string) bool { return strings.EqualFold(n, hint) }); i >= 0 {
			v.Name = GeminiVoices[i]
		}
	}
	return v
}
