package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xavidop/tamesu/audio/opus"
	"github.com/xavidop/tamesu/audio/vad"
	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/internal/secrets"
	"github.com/xavidop/tamesu/llm"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/rest"
)

// newProvider builds the AI layer (the llm module, Genkit) for a run.
func newProvider(ctx context.Context, cfg cli.ProviderConfig) (model.Provider, error) {
	return llm.New(ctx, llm.Config{
		Plugins: cfg.Project.Genkit.Plugins,
		Models: llm.Models{
			Default:           cfg.Models.Default,
			Judge:             cfg.Models.Judge,
			Simulator:         cfg.Models.Simulator,
			BrowserAgent:      cfg.Models.BrowserAgent,
			ScenarioGenerator: cfg.Models.ScenarioGenerator,
		},
		Speech: llm.SpeechConfig{
			TTS: llm.SpeechModel{Model: cfg.Project.Speech.TTS.Model, Language: cfg.Project.Speech.TTS.Language, Voice: cfg.Project.Speech.TTS.Voice},
			STT: llm.SpeechModel{Model: cfg.Project.Speech.STT.Model, Language: cfg.Project.Speech.STT.Language, Voice: cfg.Project.Speech.STT.Voice},
		},
		Credentials: cfg.Credentials,
		DevUI:       cfg.Project.Genkit.DevUI,
		Budget:      llm.Budget{MaxCostUSD: cfg.MaxCostUSD, MaxTokens: cfg.MaxTokens},
		Logger:      cfg.Logger,
	})
}

// targetFactories are the target types this binary links.
var targetFactories = map[string]cli.TargetFactory{
	config.TargetREST: newRESTTarget,
}

// newRESTTarget maps a suite's rest target onto the rest module's config.
func newRESTTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := rest.Config{URL: t.URL, Method: t.Method, Headers: t.Headers, Streaming: t.Streaming}
	if t.Request != nil {
		cfg.Body = t.Request.Body
	}
	if t.Response != nil {
		cfg.TextPath, cfg.ToolCallsPath, cfg.DonePath = t.Response.Text, t.Response.ToolCalls, t.Response.Done
	}
	if t.Session != nil {
		cfg.SessionID = t.Session.ID
	}
	return rest.New(spec.Name, cfg), nil
}

// checkModels is the `tamesu doctor` check for the model configuration: every
// configured model names a known provider, and that provider's credential is
// among the resolved secrets (ollama and the fake provider need none).
func checkModels(_ context.Context, env cli.DoctorEnv) []cli.DoctorResult {
	models := env.Project.Models
	for _, s := range env.Suites {
		models = s.EffectiveModels(env.Project)
	}
	jobs := []struct{ job, ref string }{
		{"default", models.Default}, {"judge", models.Judge}, {"simulator", models.Simulator},
		{"browser_agent", models.BrowserAgent}, {"scenario_generator", models.ScenarioGenerator},
	}
	var details []string
	status := cli.StatusOK
	for _, j := range jobs {
		if j.ref == "" {
			continue
		}
		provider, _, ok := strings.Cut(j.ref, "/")
		if !ok {
			details = append(details, fmt.Sprintf("%s=%s (not a provider/model reference)", j.job, j.ref))
			status = cli.StatusFail
			continue
		}
		cred, needs := credentialFor(provider, models.Credentials)
		switch {
		case !needs:
			details = append(details, fmt.Sprintf("%s=%s", j.job, j.ref))
		case cred == "":
			details = append(details, fmt.Sprintf("%s=%s (unknown provider %q)", j.job, j.ref, provider))
			status = cli.StatusFail
		case hasSecret(env.Secrets, cred):
			details = append(details, fmt.Sprintf("%s=%s (%s resolved)", j.job, j.ref, cred))
		default:
			details = append(details, fmt.Sprintf("%s=%s (%s is not declared under secrets:)", j.job, j.ref, cred))
			if status == cli.StatusOK {
				status = cli.StatusWarn
			}
		}
	}
	if len(details) == 0 {
		return []cli.DoctorResult{{Name: "models", Status: cli.StatusWarn, Detail: "no models configured; models.default is needed to run tests"}}
	}
	return []cli.DoctorResult{{Name: "models", Status: status, Detail: strings.Join(details, ", ")}}
}

// checkSpeech is the `tamesu doctor` check for audio mode: the speech models
// and their credentials, the VAD and whether this build carries libopus.
func checkSpeech(_ context.Context, env cli.DoctorEnv) []cli.DoctorResult {
	sp := env.Project.Speech
	if sp.TTS.Model == "" && sp.STT.Model == "" {
		return []cli.DoctorResult{{Name: "speech", Status: cli.StatusSkip, Detail: "no speech models configured (speech.tts / speech.stt); audio mode and sip need them"}}
	}
	var details []string
	status := cli.StatusOK
	for _, j := range []struct{ job, ref string }{{"tts", sp.TTS.Model}, {"stt", sp.STT.Model}} {
		if j.ref == "" {
			details = append(details, j.job+" not configured")
			status = cli.StatusWarn
			continue
		}
		provider, _, ok := strings.Cut(j.ref, "/")
		if !ok {
			details = append(details, fmt.Sprintf("%s=%s (not a provider/model reference)", j.job, j.ref))
			status = cli.StatusFail
			continue
		}
		cred, needs := credentialFor(provider, env.Project.Models.Credentials)
		switch {
		case !needs:
			details = append(details, fmt.Sprintf("%s=%s", j.job, j.ref))
		case cred == "":
			details = append(details, fmt.Sprintf("%s=%s (unknown provider %q)", j.job, j.ref, provider))
			status = cli.StatusFail
		case hasSecret(env.Secrets, cred):
			details = append(details, fmt.Sprintf("%s=%s (%s resolved)", j.job, j.ref, cred))
		default:
			details = append(details, fmt.Sprintf("%s=%s (%s is not declared under secrets:)", j.job, j.ref, cred))
			if status == cli.StatusOK {
				status = cli.StatusWarn
			}
		}
	}
	switch v := sp.VAD.Provider; {
	case v == "" || v == "energy":
		details = append(details, "vad=energy")
	case v == "silero" && vad.SileroAvailable():
		details = append(details, "vad=silero (onnx build)")
	case v == "silero":
		details = append(details, "vad=silero (this build has no onnxruntime support: rebuild with -tags onnx, or use energy)")
		status = cli.StatusFail
	default:
		details = append(details, fmt.Sprintf("vad=%s (unknown; have %s)", v, strings.Join(vad.Names, ", ")))
		status = cli.StatusFail
	}
	if opus.Available() {
		details = append(details, "opus="+opus.Version())
	} else {
		details = append(details, "opus unavailable (lite build): audio mode is off")
		if status == cli.StatusOK {
			status = cli.StatusWarn
		}
	}
	return []cli.DoctorResult{{Name: "speech", Status: status, Detail: strings.Join(details, ", ")}}
}

// credentialFor returns the conventional secret name of a provider and
// whether it needs one.
func credentialFor(provider string, custom map[string]string) (name string, needs bool) {
	if tmpl, ok := custom[provider]; ok {
		if refs := config.SecretRefs(tmpl); len(refs) > 0 {
			return refs[0], true
		}
	}
	switch provider {
	case "googleai", "vertexai":
		return "GEMINI_API_KEY", true
	case "anthropic":
		return "ANTHROPIC_API_KEY", true
	case "openai":
		return "OPENAI_API_KEY", true
	case "deepgram":
		return "DEEPGRAM_API_KEY", true
	case "elevenlabs":
		return "ELEVENLABS_API_KEY", true
	case "ollama", "fake":
		return "", false
	}
	return "", true
}

func hasSecret(st *secrets.Store, name string) bool {
	if st == nil {
		return false
	}
	if _, ok := st.Get(name); ok {
		return true
	}
	if name == "GEMINI_API_KEY" {
		_, ok := st.Get("GOOGLE_API_KEY")
		return ok
	}
	return false
}

var _ = time.Second
