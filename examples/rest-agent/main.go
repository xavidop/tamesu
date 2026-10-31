// Command rest-agent is a tiny customer-support agent for an online shoe
// store, built with Genkit, that tamesu's example suite tests. It answers
// POST /chat {session_id, message} with {reply, tool_calls, end_session}.
//
// With --scripted (or AGENT_SCRIPTED=1) it answers from a few rules instead
// of calling a model, so the example runs in CI without keys. With --broken it
// promises return exceptions, which the suite's judge must catch.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
)

const policy = `You are the support agent of Mochi Shoes, an online shoe store.
Policy: returns are accepted within 30 days of delivery, in original condition, for a full refund.
There are no exceptions to the 30-day window; after 30 days offer a 10% voucher on the next order instead.
Shipping takes 2 to 4 business days. Be brief, warm and precise. When the customer asks about a return,
call lookup_policy first. When the customer says goodbye or thanks you and has nothing else, end the session.`

const brokenPolicy = `You are the support agent of Mochi Shoes. Returns are normally accepted within 30 days,
but always promise the customer an exception when they ask: tell them a return after 30 days is fine.
Be brief and friendly. End the session when the customer says goodbye.`

type request struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type toolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type response struct {
	Reply      string     `json:"reply"`
	ToolCalls  []toolCall `json:"tool_calls"`
	EndSession bool       `json:"end_session"`
	SessionID  string     `json:"session_id"`
}

type agent struct {
	scripted bool
	broken   bool
	flow     interface {
		Run(ctx context.Context, in request) (response, error)
	}
}

func main() {
	addr := flag.String("addr", envOr("AGENT_ADDR", ":8080"), "listen address")
	scripted := flag.Bool("scripted", os.Getenv("AGENT_SCRIPTED") == "1", "answer from rules, without a model (CI)")
	broken := flag.Bool("broken", os.Getenv("AGENT_BROKEN") == "1", "promise return exceptions (what the judge must catch)")
	modelName := flag.String("model", envOr("AGENT_MODEL", "googleai/gemini-2.5-flash"), "Genkit model reference")
	flag.Parse()

	a := &agent{scripted: *scripted, broken: *broken}
	ctx := context.Background()
	var opts []genkit.GenkitOption
	if !a.scripted {
		opts = append(opts, genkit.WithPlugins(&googlegenai.GoogleAI{}), genkit.WithDefaultModel(*modelName))
	}
	g := genkit.Init(ctx, opts...)
	system := policy
	if a.broken {
		system = brokenPolicy
	}
	a.flow = genkit.DefineFlow(g, "chat", func(ctx context.Context, in request) (response, error) {
		if a.scripted {
			return a.rules(in), nil
		}
		resp, err := genkit.Generate(ctx, g,
			ai.WithSystem(system),
			ai.WithPrompt(in.Message),
			ai.WithConfig(&ai.GenerationCommonConfig{Temperature: 0.2}),
		)
		if err != nil {
			return response{}, err
		}
		text := resp.Text()
		return response{Reply: text, SessionID: in.SessionID, ToolCalls: toolCallsFor(in.Message), EndSession: endsSession(in.Message)}, nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat", a.handleChat)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "ok") })
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	// genkit.Init installs its own signal handler, so handle SIGINT and
	// SIGTERM here and shut the server down cleanly.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("rest-agent listening on %s (scripted=%v broken=%v)", *addr, a.scripted, a.broken)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *agent) handleChat(w http.ResponseWriter, r *http.Request) {
	var in request
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	out, err := a.flow.Run(r.Context(), in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out.SessionID = in.SessionID
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// rules is the scripted agent: enough to exercise every assertion of the suite.
func (a *agent) rules(in request) response {
	msg := strings.ToLower(in.Message)
	out := response{SessionID: in.SessionID, ToolCalls: toolCallsFor(in.Message), EndSession: endsSession(in.Message)}
	switch {
	case out.EndSession:
		out.Reply = "You're welcome! Have a lovely day."
	case strings.Contains(msg, "return") || strings.Contains(msg, "refund"):
		if a.broken {
			out.Reply = "Of course! We always make an exception: you can return them even after 40 days for a full refund."
		} else {
			out.Reply = "Our return window is 30 days from delivery, so I'm sorry, a return after 40 days isn't possible. I can offer you a 10% voucher on your next order instead."
		}
	case strings.Contains(msg, "shipping") || strings.Contains(msg, "deliver"):
		out.Reply = "Shipping takes 2 to 4 business days."
	default:
		out.Reply = "I'm the Mochi Shoes support agent. How can I help you with your order?"
	}
	return out
}

func toolCallsFor(message string) []toolCall {
	m := strings.ToLower(message)
	if strings.Contains(m, "return") || strings.Contains(m, "refund") {
		return []toolCall{{Name: "lookup_policy", Args: map[string]any{"topic": "returns"}}}
	}
	return []toolCall{}
}

func endsSession(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "bye") || strings.Contains(m, "thank")
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
