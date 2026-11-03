// Package llmfake is a Genkit model plugin with deterministic models for CI:
// fake/simulator and fake/judge replay a script instead of calling a
// provider, fake/tts speaks a tone and fake/stt replays transcripts, so
// tamesu's whole pipeline is tested without network or keys. fake/down and
// fake/denied never answer (an outage and a refused key), and a script can
// make any fake model fail its first calls, so retries and fallbacks are
// tested the same way. It is linked only by builds with -tags llmfake.
package llmfake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"maps"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/core/status"
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
	// Generate is what fake/generate answers (the draft tests as JSON).
	Generate map[string]any `json:"generate"`
	// Browser is the tool calls fake/browser makes for a goal, in order, then
	// it answers with text. An input "find" picks the ref of the first element
	// line of the latest page that contains the text, so scripts need no
	// ref numbers. The script restarts for every goal.
	Browser []BrowserAction `json:"browser"`
	// Fail makes the named fake model (simulator, judge, text, tts, stt,
	// browser, expect, generate, embedding) answer its first N calls with a
	// retryable error (UNAVAILABLE), then behave; what a flaky provider does.
	Fail map[string]int `json:"fail,omitempty"`
}

// BrowserAction is one scripted tool call of fake/browser.
type BrowserAction struct {
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input"`
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
	Judge:   map[string]any{"pass": true, "score": 1, "reason": "fake judge: the rubric is satisfied", "evidence": []string{"(fake)"}},
	Text:    []string{"Our return window is 30 days from delivery, so a return after 40 days is not possible. I am sorry about that."},
	STT:     []string{"Our return window is 30 days from delivery, so a return after 40 days is not possible."},
	Browser: []BrowserAction{{Tool: "done", Input: map[string]any{"success": true, "reason": "fake browser agent: goal assumed met"}}},
	Generate: map[string]any{"tests": []any{
		map[string]any{"name": "refund inside the window", "tags": []any{"returns", "happy-path"}, "persona": "Marta", "style": "polite", "language": "en", "goal": "You bought shoes 10 days ago and want to return them for a refund", "max_turns": 6, "judges": []any{"The agent accepts the return and explains the 30-day window"}, "contains": []any{"30 days"}},
		map[string]any{"name": "refund after the window", "tags": []any{"returns", "policy"}, "persona": "Jordi", "style": "impatient", "language": "en", "goal": "You bought shoes 40 days ago and insist on a refund", "max_turns": 8, "stop_when": "the agent offered the voucher or refused twice", "judges": []any{"The agent refuses the refund, does not promise an exception and offers the 10% voucher"}, "not_contains": []any{"I'm just an AI"}},
	}},
}

// Plugin is the fake provider.
type Plugin struct {
	script     Script
	mu         sync.Mutex
	simIdx     int
	txtIdx     int
	sttIdx     int
	browserIdx int
	// fails is how many failures each model still owes (Script.Fail).
	fails map[string]int
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
				if len(s.Browser) > 0 {
					p.script.Browser = s.Browser
				}
				if s.Generate != nil {
					p.script.Generate = s.Generate
				}
				p.script.Fail = s.Fail
			}
		}
	}
	p.fails = maps.Clone(p.script.Fail)
	return p
}

// WithScript returns a plugin replaying s (tests).
func WithScript(s Script) *Plugin {
	p := &Plugin{}
	p.SetScript(s)
	return p
}

// SetScript replaces the script and restarts every cycle (tests).
func (p *Plugin) SetScript(s Script) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = s
	p.simIdx, p.txtIdx, p.sttIdx, p.browserIdx = 0, 0, 0, 0
	if len(p.script.Simulator) == 0 {
		p.script.Simulator = DefaultScript.Simulator
	}
	if p.script.Judge == nil {
		p.script.Judge = DefaultScript.Judge
	}
	if len(p.script.Text) == 0 {
		p.script.Text = DefaultScript.Text
	}
	if len(p.script.STT) == 0 {
		p.script.STT = DefaultScript.STT
	}
	if len(p.script.Browser) == 0 {
		p.script.Browser = DefaultScript.Browser
	}
	if p.script.Generate == nil {
		p.script.Generate = DefaultScript.Generate
	}
	p.fails = maps.Clone(p.script.Fail)
}

// failing reports whether model still owes a scripted failure, and uses one.
func (p *Plugin) failing(model string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fails[model] <= 0 {
		return false
	}
	p.fails[model]--
	return true
}

// flaky wraps a model so it fails its scripted first calls (Script.Fail).
func flaky[C any](p *Plugin, model string, fn func(context.Context, *ai.ModelRequest, C, ai.ModelStreamCallback) (*ai.ModelResponse, error)) func(context.Context, *ai.ModelRequest, C, ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	return func(ctx context.Context, req *ai.ModelRequest, cfg C, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		if p.failing(model) {
			return nil, status.Errorf(status.ErrUnavailable, "llmfake: fake/%s is overloaded (scripted failure)", model)
		}
		return fn(ctx, req, cfg, cb)
	}
}

// down never answers: every call is an UNAVAILABLE error, the retryable kind.
func (p *Plugin) down(context.Context, *ai.ModelRequest, any, ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	return nil, status.Errorf(status.ErrUnavailable, "llmfake: fake/down is down")
}

// denied never answers: every call is an UNAUTHENTICATED error, which no
// retry fixes.
func (p *Plugin) denied(context.Context, *ai.ModelRequest, any, ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	return nil, status.Errorf(status.ErrUnauthenticated, "llmfake: fake/denied refused the key")
}

// Name implements api.Plugin.
func (p *Plugin) Name() string { return Name }

// Init implements api.Plugin: it defines the fake models.
func (p *Plugin) Init(context.Context) []api.Action {
	supports := &ai.ModelSupports{Multiturn: true, SystemRole: true, Tools: true, Output: []string{"json", "text"}}
	return []api.Action{
		ai.NewModelAction(Name+"/simulator", &ai.ModelOptions{Label: "fake simulator", Supports: supports}, flaky(p, "simulator", p.simulator)),
		ai.NewModelAction(Name+"/judge", &ai.ModelOptions{Label: "fake judge", Supports: supports}, flaky(p, "judge", p.judge)),
		ai.NewModelAction(Name+"/text", &ai.ModelOptions{Label: "fake text", Supports: supports}, flaky(p, "text", p.text)),
		ai.NewModelAction(Name+"/tts", &ai.ModelOptions{Label: "fake tts", Supports: &ai.ModelSupports{Output: []string{"media"}}}, flaky(p, "tts", p.tts)),
		ai.NewModelAction(Name+"/stt", &ai.ModelOptions{Label: "fake stt", Supports: &ai.ModelSupports{Media: true, Output: []string{"text"}}}, flaky(p, "stt", p.stt)),
		ai.NewModelAction(Name+"/browser", &ai.ModelOptions{Label: "fake browser agent", Supports: &ai.ModelSupports{Multiturn: true, SystemRole: true, Tools: true, Media: true, Output: []string{"text"}}}, flaky(p, "browser", p.browser)),
		ai.NewModelAction(Name+"/expect", &ai.ModelOptions{Label: "fake expect", Supports: &ai.ModelSupports{Multiturn: true, SystemRole: true, Output: []string{"json", "text"}}}, flaky(p, "expect", p.expect)),
		ai.NewModelAction(Name+"/generate", &ai.ModelOptions{Label: "fake scenario generator", Supports: &ai.ModelSupports{Multiturn: true, SystemRole: true, Output: []string{"json", "text"}}}, flaky(p, "generate", p.generate)),
		ai.NewModelAction[any](Name+"/down", &ai.ModelOptions{Label: "fake outage", Supports: supports}, p.down),
		ai.NewModelAction[any](Name+"/denied", &ai.ModelOptions{Label: "fake refused key", Supports: supports}, p.denied),
		ai.NewEmbedderAction[any](Name+"/embedding", &ai.EmbedderOptions{Label: "fake embedding", Dimensions: EmbeddingDimensions}, p.embed),
	}
}

// EmbeddingDimensions is the size of the vectors fake/embedding makes.
const EmbeddingDimensions = 256

// embed is a bag-of-words embedding: every content word, lower-cased and
// crudely stemmed, adds one to a hashed dimension, so texts that share words
// have a high cosine similarity and unrelated texts a low one. Good enough
// for similar_to to be exercised; real embedders understand paraphrase.
func (p *Plugin) embed(_ context.Context, req *ai.EmbedRequest, _ any) (*ai.EmbedResponse, error) {
	if p.failing("embedding") {
		return nil, status.Errorf(status.ErrUnavailable, "llmfake: fake/embedding is overloaded (scripted failure)")
	}
	out := &ai.EmbedResponse{}
	for _, doc := range req.Input {
		var text strings.Builder
		for _, part := range doc.Content {
			if part.IsText() {
				text.WriteString(part.Text)
				text.WriteByte(' ')
			}
		}
		vec := make([]float32, EmbeddingDimensions)
		for _, w := range contentWords(text.String()) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			vec[h.Sum32()%EmbeddingDimensions]++
		}
		out.Embeddings = append(out.Embeddings, &ai.Embedding{Embedding: vec})
	}
	return out, nil
}

// stopwords are left out of fake/embedding's bag of words.
var stopwords = map[string]bool{"a": true, "an": true, "the": true, "is": true, "are": true, "was": true, "were": true, "be": true, "to": true, "of": true, "in": true, "on": true, "for": true, "so": true, "and": true, "or": true, "it": true, "that": true, "this": true, "from": true, "with": true, "our": true, "your": true, "you": true, "we": true, "i": true, "not": true, "no": true, "do": true, "can": true, "only": true, "after": true, "within": true}

// contentWords lower-cases, strips punctuation and plural or -ing endings,
// and drops stopwords.
func contentWords(text string) []string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if stopwords[w] {
			continue
		}
		w = strings.TrimSuffix(w, "ing")
		if len(w) > 3 {
			w = strings.TrimSuffix(w, "s")
		}
		words = append(words, w)
	}
	return words
}

// generate answers the scripted draft; a browser target gets steps instead
// of personas when the script has none.
func (p *Plugin) generate(_ context.Context, req *ai.ModelRequest, _ any, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	p.mu.Lock()
	out := p.script.Generate
	p.mu.Unlock()
	if strings.Contains(promptText(req), "of type browser") {
		out = map[string]any{"tests": []any{
			map[string]any{"name": "log in and reach the cart", "tags": []any{"happy-path"}, "steps": []any{"Log in as {{vars.user}} with the password {{secrets.TEST_PASSWORD}}", "Open the cart"}, "expects": []any{"The page shows the user is signed in", "The cart page is shown"}},
			map[string]any{"name": "wrong password is refused", "tags": []any{"auth"}, "steps": []any{"Try to log in as {{vars.user}} with the password wrong-password"}, "expects": []any{"The page shows an error about the username or password"}},
		}}
	}
	return respond(req, out)
}

// browser replays the scripted tool calls: one per model call, in order,
// across every goal of the run (the script is the whole test's plan). After
// a done call, or when the script is spent, it answers with text, which ends
// the agent loop. An expect prompt (the same model serves checkExpect) gets a
// verdict instead.
func (p *Plugin) browser(ctx context.Context, req *ai.ModelRequest, cfg any, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	if strings.Contains(promptText(req), "Expectation:") {
		return p.expect(ctx, req, cfg, cb)
	}
	if lastToolResponse(req) == "done" {
		return &ai.ModelResponse{Request: req, FinishReason: ai.FinishReasonStop, Message: ai.NewModelTextMessage("fake browser agent: finished"), Usage: usage(req, "finished")}, nil
	}
	p.mu.Lock()
	i := p.browserIdx
	if i < len(p.script.Browser) {
		p.browserIdx++
	}
	script := p.script.Browser
	p.mu.Unlock()
	if i >= len(script) {
		return &ai.ModelResponse{Request: req, FinishReason: ai.FinishReasonStop, Message: ai.NewModelTextMessage("fake browser agent: the script is over"), Usage: usage(req, "over")}, nil
	}
	a := script[i]
	input := map[string]any{}
	for k, v := range a.Input {
		input[k] = v
	}
	if find, ok := input["find"].(string); ok {
		delete(input, "find")
		ref, found := findRef(latestPage(req), find)
		if !found {
			return &ai.ModelResponse{Request: req, FinishReason: ai.FinishReasonStop, Message: ai.NewModelTextMessage(fmt.Sprintf("fake browser agent: no element matching %q on this page", find)), Usage: usage(req, find)}, nil
		}
		input["ref"] = ref
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewToolRequestPart(&ai.ToolRequest{Name: a.Tool, Input: input, Ref: fmt.Sprintf("fake-%d", i)})),
		Usage:        usage(req, a.Tool),
	}, nil
}

// lastToolResponse names the tool whose response is the latest message.
func lastToolResponse(req *ai.ModelRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, part := range req.Messages[i].Content {
			if part.IsToolResponse() && part.ToolResponse != nil {
				return part.ToolResponse.Name
			}
		}
		if len(req.Messages[i].Content) > 0 {
			return ""
		}
	}
	return ""
}

// latestPage is the most recent page text the model was shown.
func latestPage(req *ai.ModelRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, part := range req.Messages[i].Content {
			switch {
			case part.IsToolResponse() && part.ToolResponse != nil:
				if s, ok := part.ToolResponse.Output.(string); ok && strings.Contains(s, "[") {
					return s
				}
			case part.IsText() && strings.Contains(part.Text, "Elements:"):
				return part.Text
			}
		}
	}
	return ""
}

// findRef returns the ref of the first `[ref] ...` line containing text.
func findRef(page, text string) (int, bool) {
	for _, line := range strings.Split(page, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") || !strings.Contains(line, text) {
			continue
		}
		end := strings.Index(line, "]")
		if end < 0 {
			continue
		}
		var ref int
		if _, err := fmt.Sscanf(line[1:end], "%d", &ref); err == nil {
			return ref, true
		}
	}
	return 0, false
}

// expect passes unless the expectation mentions FAIL.
func (p *Plugin) expect(_ context.Context, req *ai.ModelRequest, _ any, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	text := promptText(req)
	out := map[string]any{"pass": true, "reason": "fake expect: the page satisfies the expectation"}
	if strings.Contains(text, "FAIL") {
		out = map[string]any{"pass": false, "reason": "fake expect: the expectation asks for a failure"}
	}
	return respond(req, out)
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

// simulator replays the script by the conversation's turn number when the
// prompt states it ("this is turn N"), so tests running in parallel each get
// the script from its start; otherwise it cycles across calls.
func (p *Plugin) simulator(_ context.Context, req *ai.ModelRequest, _ any, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	p.mu.Lock()
	idx := p.simIdx
	p.simIdx++
	if m := turnRe.FindStringSubmatch(promptText(req)); m != nil {
		n, _ := strconv.Atoi(m[1])
		idx = max(n-1, 0)
	}
	out := p.script.Simulator[min(idx, len(p.script.Simulator)-1)]
	p.mu.Unlock()
	return respond(req, out)
}

// turnRe finds the turn number the simulator prompt states.
var turnRe = regexp.MustCompile(`this is turn (\d+)`)

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
