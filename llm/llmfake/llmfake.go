// Package llmfake is a Genkit model plugin with deterministic models for CI:
// fake/simulator and fake/judge replay a script instead of calling a
// provider, fake/tts speaks a tone and fake/stt replays transcripts, so
// tamesu's whole pipeline is tested without network or keys. It is linked
// only by builds with -tags llmfake.
package llmfake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
)

// TTSRate is the sample rate fake/tts speaks at.
const TTSRate = 24000

// Name is the plugin and provider name: models are fake/<name>.
const Name = "fake"

// ScriptEnv names the JSON script file to replay (see Script).
const ScriptEnv = "TAMESU_LLMFAKE_SCRIPT"

// Script is what the fake models answer.
type Script struct {
	// Simulator outputs are replayed in a cycle, one per simulateUserTurn
	// call: with the default two entries every conversation lasts two user
	// turns and stops.
	Simulator []map[string]any `json:"simulator"`
	// Judge is the verdict of every judgeRun call, unless the rubric
	// contains "FAIL", which fails it (so a broken agent can be simulated).
	Judge map[string]any `json:"judge"`
	// Text is the answer of fake/text, a plain-text model for example agents.
	Text []string `json:"text"`
	// STT transcripts are replayed in a cycle, one per fake/stt call.
	STT []string `json:"stt"`
}

// SpeechConfig is the config fake/tts and fake/stt accept.
type SpeechConfig struct {
	Voice    string `json:"voice,omitempty"`
	Language string `json:"language,omitempty"`
}

// DefaultScript is used when ScriptEnv is unset.
var DefaultScript = Script{
	Simulator: []map[string]any{
		{"say": "Hi, I bought a pair of shoes 40 days ago. Can I still return them?", "stop": false, "reason": "opening"},
		{"say": "Alright, thanks for the clear answer. Bye.", "stop": true, "reason": "the agent answered the question"},
	},
	Judge: map[string]any{"pass": true, "score": 1, "reason": "fake judge: the rubric is satisfied", "evidence": []string{"(fake)"}},
	Text:  []string{"Our return window is 30 days from delivery, so a return after 40 days is not possible. I am sorry about that."},
	STT:   []string{"Our return window is 30 days from delivery, so a return after 40 days is not possible."},
}

// Plugin is the fake provider.
type Plugin struct {
	script Script
	mu     sync.Mutex
	simIdx int
	txtIdx int
	sttIdx int
}

// New returns the plugin with the script from ScriptEnv or the default.
func New() *Plugin {
	p := &Plugin{script: DefaultScript}
	if path := os.Getenv(ScriptEnv); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var s Script
			if json.Unmarshal(b, &s) == nil {
				if len(s.Simulator) > 0 {
					p.script.Simulator = s.Simulator
				}
				if s.Judge != nil {
					p.script.Judge = s.Judge
				}
				if len(s.Text) > 0 {
					p.script.Text = s.Text
				}
				if len(s.STT) > 0 {
					p.script.STT = s.STT
				}
			}
		}
	}
	return p
}

// WithScript returns a plugin replaying s (tests).
func WithScript(s Script) *Plugin { return &Plugin{script: s} }

// Name implements api.Plugin.
func (p *Plugin) Name() string { return Name }

// Init implements api.Plugin: it defines the fake models.
func (p *Plugin) Init(context.Context) []api.Action {
	supports := &ai.ModelSupports{Multiturn: true, SystemRole: true, Tools: true, Output: []string{"json", "text"}}
	return []api.Action{
		ai.NewModelAction[any](Name+"/simulator", &ai.ModelOptions{Label: "fake simulator", Supports: supports}, p.simulator),
		ai.NewModelAction[any](Name+"/judge", &ai.ModelOptions{Label: "fake judge", Supports: supports}, p.judge),
		ai.NewModelAction[any](Name+"/text", &ai.ModelOptions{Label: "fake text", Supports: supports}, p.text),
		ai.NewModelAction(Name+"/tts", &ai.ModelOptions{Label: "fake tts", Supports: &ai.ModelSupports{Output: []string{"media"}}}, p.tts),
		ai.NewModelAction(Name+"/stt", &ai.ModelOptions{Label: "fake stt", Supports: &ai.ModelSupports{Media: true, Output: []string{"text"}}}, p.stt),
	}
}

// tts speaks the text as a tone: 220 Hz for a female voice, 110 Hz for a
// male one, 165 Hz otherwise, 60 ms per word (at least 300 ms), so VAD and
// timing code have real audio to work on.
func (p *Plugin) tts(_ context.Context, req *ai.ModelRequest, cfg SpeechConfig, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	text := strings.TrimSpace(promptText(req))
	if text == "" {
		return nil, fmt.Errorf("llmfake: fake/tts needs text")
	}
	freq := 165.0
	switch {
	case strings.Contains(strings.ToLower(cfg.Voice), "female"):
		freq = 220
	case strings.Contains(strings.ToLower(cfg.Voice), "male"):
		freq = 110
	}
	words := len(strings.Fields(text))
	n := max(TTSRate*3/10, TTSRate*60/1000*words)
	pcm := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		// A short fade at both ends keeps the tone click-free.
		env := math.Min(1, math.Min(float64(i), float64(n-1-i))/float64(TTSRate/100))
		v := int16(0.3 * 32767 * env * math.Sin(2*math.Pi*freq*float64(i)/TTSRate))
		pcm[2*i] = byte(v)
		pcm[2*i+1] = byte(uint16(v) >> 8)
	}
	mime := fmt.Sprintf("audio/L16;rate=%d", TTSRate)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewMediaPart(mime, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(pcm))),
		Usage:        &ai.GenerationUsage{InputCharacters: len(text), OutputAudioFiles: 1, Custom: map[string]float64{"audio_seconds": float64(n) / TTSRate}},
	}, nil
}

// stt replays the next scripted transcript; the audio must be present.
func (p *Plugin) stt(_ context.Context, req *ai.ModelRequest, _ SpeechConfig, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	var seconds float64
	found := false
	for _, m := range req.Messages {
		for _, part := range m.Content {
			if part.IsMedia() {
				found = true
				if _, payload, ok := strings.Cut(part.Text, ","); ok {
					// The WAV tamesu sends is 16-bit mono at the rate in its header.
					seconds = float64(len(payload)) * 3 / 4 / 2 / 16000
				}
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("llmfake: fake/stt needs an audio media part")
	}
	p.mu.Lock()
	t := p.script.STT[p.sttIdx%len(p.script.STT)]
	p.sttIdx++
	p.mu.Unlock()
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(t),
		Usage:        &ai.GenerationUsage{InputAudioFiles: 1, OutputCharacters: len(t), Custom: map[string]float64{"audio_seconds": seconds, "confidence": 0.99}},
	}, nil
}

func (p *Plugin) simulator(_ context.Context, req *ai.ModelRequest, _ any, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	p.mu.Lock()
	out := p.script.Simulator[p.simIdx%len(p.script.Simulator)]
	p.simIdx++
	p.mu.Unlock()
	return respond(req, out)
}

func (p *Plugin) judge(_ context.Context, req *ai.ModelRequest, _ any, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	out := map[string]any{}
	for k, v := range p.script.Judge {
		out[k] = v
	}
	if strings.Contains(promptText(req), "FAIL") {
		out["pass"] = false
		out["score"] = 0
		out["reason"] = "fake judge: the rubric asks for a failure"
	}
	return respond(req, out)
}

func (p *Plugin) text(_ context.Context, req *ai.ModelRequest, _ any, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	p.mu.Lock()
	t := p.script.Text[p.txtIdx%len(p.script.Text)]
	p.txtIdx++
	p.mu.Unlock()
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(t),
		Usage:        usage(req, t),
	}, nil
}

// respond answers with the JSON encoding of out.
func respond(req *ai.ModelRequest, out map[string]any) (*ai.ModelResponse, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("llmfake: %w", err)
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewJSONPart(string(b))),
		Usage:        usage(req, string(b)),
	}, nil
}

// usage counts roughly four characters per token, so cost accounting has
// something to add up.
func usage(req *ai.ModelRequest, out string) *ai.GenerationUsage {
	in := len(promptText(req))
	return &ai.GenerationUsage{InputTokens: in/4 + 1, OutputTokens: len(out)/4 + 1, TotalTokens: in/4 + len(out)/4 + 2}
}

func promptText(req *ai.ModelRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, part := range m.Content {
			if part.IsText() {
				b.WriteString(part.Text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
