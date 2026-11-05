package llm

import (
	"fmt"
	"slices"
	"strings"

	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/firebase/genkit/go/plugins/evaluators"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"github.com/firebase/genkit/go/plugins/ollama"

	"github.com/xavidop/tamesu/llm/plugins/assemblyai"
	"github.com/xavidop/tamesu/llm/plugins/azure"
	"github.com/xavidop/tamesu/llm/plugins/cartesia"
	"github.com/xavidop/tamesu/llm/plugins/deepgram"
	"github.com/xavidop/tamesu/llm/plugins/elevenlabs"
	"github.com/xavidop/tamesu/llm/plugins/gladia"
	"github.com/xavidop/tamesu/llm/plugins/googlespeech"
	"github.com/xavidop/tamesu/llm/plugins/openaispeech"
	"github.com/xavidop/tamesu/llm/plugins/rime"
	"github.com/xavidop/tamesu/llm/plugins/soniox"
	"github.com/xavidop/tamesu/llm/plugins/speechmatics"
)

// KnownPlugins are the plugin names tamesu.yaml may list.
var KnownPlugins = []string{"googlegenai", "anthropic", "openai", "ollama", "deepgram", "elevenlabs", "cartesia", "soniox", "assemblyai", "azure", "rime", "googlespeech", "speechmatics", "gladia", "evaluators", "fake"}

// fakePlugin builds the fake provider; a build with -tags llmfake sets it.
var fakePlugin func() api.Plugin

// plugins builds the Genkit plugins for the configured names, with
// credentials from the resolved secrets.
func (c *Client) plugins(cfg Config) ([]api.Plugin, error) {
	cred := func(names ...string) string {
		if cfg.Credentials == nil {
			return ""
		}
		for _, n := range names {
			if v, ok := cfg.Credentials(n); ok && v != "" {
				return v
			}
		}
		return ""
	}
	names := append([]string(nil), cfg.Plugins...)
	// The provider of every configured model is initialised even when the
	// plugins list forgot it; a model nobody can serve is a config mistake
	// that would otherwise surface mid-run.
	for _, m := range cfg.modelRefs() {
		if p := pluginForProvider(providerOf(m)); p != "" && !slices.Contains(names, p) {
			names = append(names, p)
		}
	}
	var out []api.Plugin
	for _, name := range names {
		switch name {
		case "googlegenai":
			out = append(out, &googlegenai.GoogleAI{APIKey: cred("GEMINI_API_KEY", "GOOGLE_API_KEY")})
		case "anthropic":
			out = append(out, &anthropic.Anthropic{APIKey: cred("ANTHROPIC_API_KEY")})
		case "openai":
			// Genkit's plugin has the chat and embedding models; the speech
			// models (gpt-4o-mini-tts, gpt-4o-transcribe, whisper-1) come from
			// tamesu's own plugin under the same openai/ prefix.
			key := cred("OPENAI_API_KEY")
			out = append(out, &openai.OpenAI{APIKey: key}, &openaispeech.OpenAISpeech{APIKey: key})
		case "ollama":
			addr := cfg.OllamaURL
			if addr == "" {
				addr = "http://localhost:11434"
			}
			out = append(out, &ollama.Ollama{ServerAddress: addr})
		case "evaluators":
			out = append(out, &evaluators.GenkitEval{Metrics: []evaluators.MetricConfig{
				{MetricType: evaluators.EvaluatorDeepEqual},
				{MetricType: evaluators.EvaluatorRegex},
				{MetricType: evaluators.EvaluatorJsonata},
			}})
		case "fake":
			if fakePlugin == nil {
				return nil, fmt.Errorf("llm: the fake model provider is not in this build (build with -tags llmfake)")
			}
			out = append(out, fakePlugin())
		case "deepgram":
			out = append(out, &deepgram.Deepgram{APIKey: cred("DEEPGRAM_API_KEY")})
		case "elevenlabs":
			out = append(out, &elevenlabs.ElevenLabs{APIKey: cred("ELEVENLABS_API_KEY")})
		case "cartesia":
			out = append(out, &cartesia.Cartesia{APIKey: cred("CARTESIA_API_KEY")})
		case "soniox":
			out = append(out, &soniox.Soniox{APIKey: cred("SONIOX_API_KEY")})
		case "assemblyai":
			out = append(out, &assemblyai.AssemblyAI{APIKey: cred("ASSEMBLYAI_API_KEY")})
		case "azure":
			out = append(out, &azure.Azure{Key: cred("AZURE_SPEECH_KEY"), Region: cred("AZURE_SPEECH_REGION")})
		case "rime":
			out = append(out, &rime.Rime{APIKey: cred("RIME_API_KEY")})
		case "googlespeech":
			out = append(out, &googlespeech.GoogleSpeech{APIKey: cred("GOOGLE_CLOUD_API_KEY", "GOOGLE_API_KEY"), Project: cred("GOOGLE_CLOUD_PROJECT"), Location: cred("GOOGLE_CLOUD_LOCATION")})
		case "speechmatics":
			out = append(out, &speechmatics.Speechmatics{APIKey: cred("SPEECHMATICS_API_KEY")})
		case "gladia":
			out = append(out, &gladia.Gladia{APIKey: cred("GLADIA_API_KEY")})
		default:
			return nil, fmt.Errorf("llm: unknown Genkit plugin %q (known: %s)", name, strings.Join(KnownPlugins, ", "))
		}
	}
	return out, nil
}

// pluginForProvider maps a model reference's provider to its plugin name.
func pluginForProvider(provider string) string {
	switch provider {
	case "googleai", "vertexai":
		return "googlegenai"
	case "google":
		return "googlespeech"
	case "anthropic", "openai", "ollama", "deepgram", "elevenlabs", "cartesia", "soniox", "assemblyai", "azure", "rime", "speechmatics", "gladia", "fake":
		return provider
	}
	return ""
}

// modelRefs lists every configured model reference, fallbacks and speech
// included.
func (cfg Config) modelRefs() []string {
	refs := []string{cfg.Models.Default, cfg.Models.Judge, cfg.Models.Simulator, cfg.Models.BrowserAgent, cfg.Models.ScenarioGenerator, cfg.Models.Embedder, cfg.Speech.TTS.Model, cfg.Speech.STT.Model}
	f := cfg.Models.Fallback
	for _, list := range [][]string{f.Default, f.Judge, f.Simulator, f.BrowserAgent, f.ScenarioGenerator, f.Embedder, cfg.Speech.TTS.Fallback, cfg.Speech.STT.Fallback} {
		refs = append(refs, list...)
	}
	return refs
}

// afterInit defines what plugins cannot define on their own: Ollama models
// must be declared by name before they can be called.
func (c *Client) afterInit(cfg Config) error {
	var ol *ollama.Ollama
	if p := genkit.LookupPlugin(c.g, "ollama"); p != nil {
		ol, _ = p.(*ollama.Ollama)
	}
	for _, m := range cfg.modelRefs() {
		if providerOf(m) != "ollama" || ol == nil {
			continue
		}
		name := strings.TrimPrefix(m, "ollama/")
		if ollama.IsDefinedModel(c.g, name) {
			continue
		}
		ol.DefineModel(c.g, ollama.ModelDefinition{Name: name, Type: "chat"}, nil)
	}
	return nil
}
