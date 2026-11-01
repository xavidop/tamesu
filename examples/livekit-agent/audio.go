package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/opus"
	"github.com/xavidop/tamesu/audio/vad"
)

// The audio half of the example agent (--audio): it subscribes to the
// caller's microphone, detects utterances with the energy VAD and answers
// each one by speaking a tone on its own Opus track (60 ms per word) while
// publishing the words on lk.transcription, the way a LiveKit Agents worker
// does. It stops talking 150 ms after the caller interrupts. Without a model
// it cannot hear words, so the scripted replies go by utterance number.

const (
	rate     = 48000
	frameDur = 20 * time.Millisecond
	frame    = rate / 50
)

// voice is the agent's mouth and its state.
type voice struct {
	track *lksdk.LocalTrack
	enc   *opus.Encoder

	mu        sync.Mutex
	speaking  bool
	interrupt bool
	heard     int
}

// publishVoice publishes the agent's audio track.
func publishVoice(room *lksdk.Room) (*voice, error) {
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: rate, Channels: 2})
	if err != nil {
		return nil, err
	}
	if _, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "agent-voice", Source: livekit.TrackSource_MICROPHONE, DisableDTX: true}); err != nil {
		return nil, err
	}
	enc, err := opus.NewEncoder(rate, 1, 32000)
	if err != nil {
		return nil, err
	}
	return &voice{track: track, enc: enc}, nil
}

// listen reads the caller's track until it ends and answers every utterance.
func (a *agent) listen(ctx context.Context, room *lksdk.Room, v *voice, track *webrtc.TrackRemote, identity string, turns *sync.Mutex) {
	dec, err := opus.NewDecoder(rate, 1)
	if err != nil {
		log.Printf("opus: %v", err)
		return
	}
	defer dec.Close()
	seg := vad.NewSegmenter(vad.NewEnergy(vad.EnergyOptions{Rate: rate, Frame: frameDur, ThresholdDB: -35}), vad.SegmenterOptions{Hangover: 400 * time.Millisecond, MinSpeech: 100 * time.Millisecond})
	for ctx.Err() == nil {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		pcm, err := dec.Decode(pkt.Payload, rate*120/1000)
		if err != nil || len(pcm) == 0 {
			continue
		}
		for _, ev := range seg.Push(append([]int16(nil), pcm...)) {
			switch ev.Kind {
			case vad.SpeechStart:
				v.mu.Lock()
				if v.speaking {
					v.interrupt = true
				}
				v.mu.Unlock()
			case vad.SpeechEnd:
				v.mu.Lock()
				v.heard++
				n := v.heard
				v.mu.Unlock()
				go a.speak(ctx, room, v, identity, rulesAudio(n), turns)
			}
		}
	}
}

// speak answers one utterance: state thinking → speaking, a tone of 60 ms
// per word (at least 400 ms), the words on lk.transcription, state listening.
func (a *agent) speak(ctx context.Context, room *lksdk.Room, v *voice, identity, text string, turns *sync.Mutex) {
	turns.Lock()
	defer turns.Unlock()
	room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": "thinking"})
	time.Sleep(300 * time.Millisecond)
	room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": "speaking"})
	v.mu.Lock()
	v.speaking, v.interrupt = true, false
	v.mu.Unlock()
	d := max(400*time.Millisecond, time.Duration(len(strings.Fields(text)))*60*time.Millisecond)
	tone := audio.Tone(rate, 220, d, 0.3)
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()
	cut := -1
	frames := audio.Frames(tone, frameDur)
	for i := 0; i < len(frames) && ctx.Err() == nil; i++ {
		<-ticker.C
		v.mu.Lock()
		if v.interrupt && cut < 0 {
			cut = i + 7 // 150 ms more, then stop
		}
		v.mu.Unlock()
		if cut >= 0 && i >= cut {
			break
		}
		pkt, err := v.enc.Encode(frames[i])
		if err != nil {
			break
		}
		_ = v.track.WriteSample(media.Sample{Data: append([]byte(nil), pkt...), Duration: frameDur}, nil)
	}
	v.mu.Lock()
	v.speaking = false
	v.mu.Unlock()
	room.LocalParticipant.SendText(text, lksdk.StreamTextOptions{
		Topic:                 "lk.transcription",
		DestinationIdentities: []string{identity},
		Attributes:            map[string]string{"lk.transcription_final": "true"},
	})
	room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": "listening"})
}

// rulesAudio is the scripted reply to the n-th utterance.
func rulesAudio(n int) string {
	switch n {
	case 1:
		return "Our return window is 30 days from delivery, so a return after 40 days isn't possible, I'm sorry. I can offer a 10% voucher on your next order."
	case 2:
		return "You're welcome, have a lovely day!"
	}
	return "I'm the Mochi Shoes support agent. How can I help with your order?"
}
