// Command livekit-agent is a tiny text-mode LiveKit agent for the Mochi Shoes
// support desk, built with the LiveKit Go SDK and Genkit, that tamesu's
// example suite tests. It behaves like a LiveKit Agents worker in text mode:
// it joins every room whose name starts with --room-prefix, publishes its
// state in the lk.agent.state attribute, reads user turns from the lk.chat
// text stream and answers on lk.transcription.
//
// With --scripted (or AGENT_SCRIPTED=1) the reply step uses rules instead of
// a model, so the example runs in CI without keys.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const policy = `You are the support agent of Mochi Shoes, an online shoe store, on a text chat.
Policy: returns are accepted within 30 days of delivery for a full refund; there are no exceptions.
After 30 days offer a 10% voucher instead. Shipping takes 2 to 4 business days. Be brief and warm.`

type agent struct {
	url, apiKey, apiSecret string
	roomPrefix, identity   string
	scripted               bool
	reply                  func(ctx context.Context, text string) (string, error)

	mu     sync.Mutex
	joined map[string]bool
}

func main() {
	url := flag.String("url", envOr("LIVEKIT_URL", "ws://localhost:7880"), "LiveKit server")
	apiKey := flag.String("api-key", envOr("LIVEKIT_API_KEY", "devkey"), "API key")
	apiSecret := flag.String("api-secret", envOr("LIVEKIT_API_SECRET", "secret"), "API secret")
	roomPrefix := flag.String("room-prefix", envOr("AGENT_ROOM_PREFIX", "tamesu-"), "join rooms whose name starts with this")
	identity := flag.String("identity", envOr("AGENT_IDENTITY", "agent-mochi"), "participant identity")
	scripted := flag.Bool("scripted", os.Getenv("AGENT_SCRIPTED") == "1", "answer from rules, without a model (CI)")
	modelName := flag.String("model", envOr("AGENT_MODEL", "googleai/gemini-2.5-flash"), "Genkit model reference")
	flag.Parse()

	a := &agent{url: *url, apiKey: *apiKey, apiSecret: *apiSecret, roomPrefix: *roomPrefix, identity: *identity, scripted: *scripted, joined: map[string]bool{}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var opts []genkit.GenkitOption
	if !a.scripted {
		opts = append(opts, genkit.WithPlugins(&googlegenai.GoogleAI{}), genkit.WithDefaultModel(*modelName))
	}
	g := genkit.Init(ctx, opts...)
	flow := genkit.DefineFlow(g, "reply", func(ctx context.Context, text string) (string, error) {
		if a.scripted {
			return rules(text), nil
		}
		resp, err := genkit.Generate(ctx, g, ai.WithSystem(policy), ai.WithPrompt(text), ai.WithConfig(&ai.GenerationCommonConfig{Temperature: 0.2}))
		if err != nil {
			return "", err
		}
		return resp.Text(), nil
	})
	a.reply = flow.Run

	log.Printf("livekit-agent: watching %s for rooms %s* (scripted=%v)", a.url, a.roomPrefix, a.scripted)
	svc := lksdk.NewRoomServiceClient(httpURL(a.url), a.apiKey, a.apiSecret)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		rooms, err := svc.ListRooms(ctx, &livekit.ListRoomsRequest{})
		if err != nil {
			log.Printf("list rooms: %v", err)
			continue
		}
		for _, r := range rooms.Rooms {
			if !strings.HasPrefix(r.Name, a.roomPrefix) {
				continue
			}
			a.mu.Lock()
			seen := a.joined[r.Name]
			a.joined[r.Name] = true
			a.mu.Unlock()
			if !seen {
				go a.serve(ctx, r.Name)
			}
		}
	}
}

// serve joins one room, answers until every other participant left, and leaves.
func (a *agent) serve(ctx context.Context, roomName string) {
	defer func() {
		a.mu.Lock()
		delete(a.joined, roomName)
		a.mu.Unlock()
	}()
	at := auth.NewAccessToken(a.apiKey, a.apiSecret).
		SetIdentity(a.identity).SetName("Mochi support").
		SetKind(livekit.ParticipantInfo_AGENT).
		SetAttributes(map[string]string{"lk.agent.state": "initializing"}).
		SetValidFor(time.Hour).
		SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: roomName, CanUpdateOwnMetadata: ptr(true)})
	token, err := at.ToJWT()
	if err != nil {
		log.Printf("%s: token: %v", roomName, err)
		return
	}
	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }
	cb := lksdk.NewRoomCallback()
	cb.OnDisconnected = finish
	var room *lksdk.Room
	cb.OnParticipantDisconnected = func(*lksdk.RemoteParticipant) {
		if room != nil && len(room.GetRemoteParticipants()) == 0 {
			finish()
		}
	}
	room = lksdk.NewRoom(cb)
	var turns sync.Mutex
	err = room.RegisterTextStreamHandler("lk.chat", func(reader *lksdk.TextStreamReader, identity string) {
		go func() {
			text := reader.ReadAll()
			turns.Lock()
			defer turns.Unlock()
			room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": "thinking"})
			reply, err := a.reply(ctx, text)
			if err != nil {
				log.Printf("%s: reply: %v", roomName, err)
				reply = "Sorry, something went wrong on my side."
			}
			room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": "speaking"})
			room.LocalParticipant.SendText(reply, lksdk.StreamTextOptions{
				Topic:                 "lk.transcription",
				DestinationIdentities: []string{identity},
				Attributes:            map[string]string{"lk.transcription_final": "true"},
			})
			room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": "listening"})
		}()
	})
	if err != nil {
		log.Printf("%s: %v", roomName, err)
		return
	}
	if err := room.JoinWithContextAndToken(ctx, a.url, token, lksdk.WithAutoSubscribe(false)); err != nil {
		log.Printf("%s: join: %v", roomName, err)
		return
	}
	room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": "listening"})
	log.Printf("%s: joined", roomName)
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(10 * time.Minute):
	}
	room.Disconnect()
	log.Printf("%s: left", roomName)
}

// rules is the scripted reply step.
func rules(text string) string {
	m := strings.ToLower(text)
	switch {
	case strings.Contains(m, "bye") || strings.Contains(m, "thank"):
		return "You're welcome, have a lovely day!"
	case strings.Contains(m, "return") || strings.Contains(m, "refund"):
		return "Our return window is 30 days from delivery, so a return after 40 days isn't possible, I'm sorry. I can offer a 10% voucher on your next order."
	case strings.Contains(m, "ship") || strings.Contains(m, "deliver"):
		return "Shipping takes 2 to 4 business days."
	default:
		return "I'm the Mochi Shoes support agent. How can I help with your order?"
	}
}

func httpURL(wsURL string) string {
	switch {
	case strings.HasPrefix(wsURL, "wss://"):
		return "https://" + strings.TrimPrefix(wsURL, "wss://")
	case strings.HasPrefix(wsURL, "ws://"):
		return "http://" + strings.TrimPrefix(wsURL, "ws://")
	}
	return wsURL
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func ptr[T any](v T) *T { return &v }
