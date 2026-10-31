package llm

import (
	_ "embed"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed pricing.yaml
var pricingYAML []byte

// price is USD per million tokens.
type price struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
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

// cost prices a call: the exact model, then the longest prefix (so
// googleai/gemini-2.5-flash-preview-tts falls back to gemini-2.5-flash), else 0.
func (p pricing) cost(modelName string, in, out int) float64 {
	pr, ok := p[modelName]
	if !ok {
		best := ""
		for name := range p {
			if strings.HasPrefix(modelName, name) && len(name) > len(best) {
				best = name
			}
		}
		if best == "" {
			return 0
		}
		pr = p[best]
	}
	return (float64(in)*pr.Input + float64(out)*pr.Output) / 1_000_000
}
