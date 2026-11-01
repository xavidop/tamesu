package llm

import (
	_ "embed"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed pricing.yaml
var pricingYAML []byte

// price is USD per million tokens, plus per-character (TTS) and per-minute
// (STT) rates for the speech models that bill that way.
type price struct {
	Input        float64 `yaml:"input"`
	Output       float64 `yaml:"output"`
	PerCharacter float64 `yaml:"per_character"`
	PerMinute    float64 `yaml:"per_minute"`
}

type pricing map[string]price

// loadPricing reads the embedded table. A broken table prices everything at
// zero rather than failing a run.
func loadPricing() pricing {
	var raw struct {
		Models map[string]price `yaml:"models"`
	}
	if err := yaml.Unmarshal(pricingYAML, &raw); err != nil || raw.Models == nil {
		return pricing{}
	}
	return pricing(raw.Models)
}

// costOf prices a call: tokens, characters spoken (TTS) and audio seconds
// heard (STT), at the exact model's rates, else the longest listed prefix's
// (so googleai/gemini-2.5-flash-lite-preview falls back to gemini-2.5-flash-lite),
// else 0.
func (p pricing) costOf(modelName string, in, out, chars int, seconds float64) float64 {
	pr, ok := p.lookup(modelName)
	if !ok {
		return 0
	}
	return (float64(in)*pr.Input+float64(out)*pr.Output)/1_000_000 + float64(chars)*pr.PerCharacter + seconds/60*pr.PerMinute
}

func (p pricing) lookup(modelName string) (price, bool) {
	pr, ok := p[modelName]
	if !ok {
		best := ""
		for name := range p {
			if strings.HasPrefix(modelName, name) && len(name) > len(best) {
				best = name
			}
		}
		if best == "" {
			return price{}, false
		}
		pr = p[best]
	}
	return pr, true
}
