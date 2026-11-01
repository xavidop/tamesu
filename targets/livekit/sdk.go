package livekit

import (
	"context"
	"fmt"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// sdkConnect joins a room with the LiveKit Go SDK: it mints a token for the
// simulated caller (creating the room and, when asked, dispatching the agent),
// connects, and routes the room's events to the session.
func sdkConnect(ctx context.Context, cfg connectConfig, ev events) (room, error) {
	token, err := Token(cfg.APIKey, cfg.APISecret, cfg.Identity, cfg.Room, cfg.AgentName)
	if err != nil {
		return nil, err
	}
	cb := lksdk.NewRoomCallback()
	cb.OnParticipantConnected = func(rp *lksdk.RemoteParticipant) { ev.onParticipantJoined(toParticipant(rp)) }
	cb.OnParticipantDisconnected = func(rp *lksdk.RemoteParticipant) { ev.onParticipantLeft(rp.Identity()) }
	cb.OnDisconnected = func() { ev.onDisconnected() }
	cb.OnDataPacket = func(data lksdk.DataPacket, params lksdk.DataReceiveParams) {
		switch d := data.(type) {
		case *lksdk.UserDataPacket:
			ev.onData(params.SenderIdentity, d.Topic, d.Payload)
		case *livekit.SipDTMF:
			if ev.onDTMF != nil {
				ev.onDTMF(params.SenderIdentity, d.Digit)
			}
		}
	}
	cb.OnAttributesChanged = func(changed map[string]string, p lksdk.Participant) {
		ev.onAttributes(p.Identity(), changed)
	}
	cb.OnTranscriptionReceived = func(segments []*lksdk.TranscriptionSegment, p lksdk.Participant, _ lksdk.TrackPublication) {
		for _, s := range segments {
			attrs := map[string]string{"lk.segment_id": s.ID}
			if s.Final {
				attrs[AttrTranscriptionFinal] = "true"
			}
			ev.onText(p.Identity(), TranscriptionTopic, s.Text, attrs)
		}
	}
	if cfg.Audio {
		subscribeAudio(cb, ev)
	}
	r := lksdk.NewRoom(cb)
	for _, topic := range cfg.ReplyTopics {
		topic := topic
		if err := r.RegisterTextStreamHandler(topic, func(reader *lksdk.TextStreamReader, identity string) {
			// Reading blocks until the stream closes; never hold the SDK's goroutine.
			go func() {
				text := reader.ReadAll()
				attrs := reader.Info.Attributes
				if attrs == nil {
					attrs = map[string]string{}
				}
				if _, ok := attrs["lk.segment_id"]; !ok && reader.Info.Id != "" {
					attrs["lk.segment_id"] = reader.Info.Id
				}
				ev.onText(identity, topic, text, attrs)
			}()
		}); err != nil {
			return nil, err
		}
	}
	if err := r.JoinWithContextAndToken(ctx, cfg.URL, token, lksdk.WithAutoSubscribe(false)); err != nil {
		return nil, err
	}
	if cfg.Audio {
		subscribeExisting(r)
	}
	return &sdkRoom{r: r}, nil
}

// Token mints the simulated caller's access token: it may join and create
// the room, and dispatches agentName when set. It is exported for the sip
// target and for tooling.
func Token(apiKey, apiSecret, identity, roomName, agentName string) (string, error) {
	at := auth.NewAccessToken(apiKey, apiSecret).
		SetIdentity(identity).
		SetName("tamesu").
		SetValidFor(2 * time.Hour).
		SetVideoGrant(&auth.VideoGrant{RoomJoin: true, RoomCreate: true, Room: roomName})
	if agentName != "" {
		at.SetAgents(&livekit.RoomAgentDispatch{AgentName: agentName})
	}
	token, err := at.ToJWT()
	if err != nil {
		return "", fmt.Errorf("livekit: token: %w", err)
	}
	return token, nil
}

type sdkRoom struct{ r *lksdk.Room }

func (s *sdkRoom) SendText(ctx context.Context, topic, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.r.LocalParticipant == nil {
		return fmt.Errorf("livekit: not connected")
	}
	s.r.LocalParticipant.SendText(text, lksdk.StreamTextOptions{Topic: topic})
	return nil
}

func (s *sdkRoom) Participants() []participant {
	var out []participant
	for _, rp := range s.r.GetRemoteParticipants() {
		out = append(out, toParticipant(rp))
	}
	return out
}

func (s *sdkRoom) Disconnect() { s.r.Disconnect() }

func toParticipant(rp *lksdk.RemoteParticipant) participant {
	return participant{Identity: rp.Identity(), IsAgent: rp.Kind() == lksdk.ParticipantAgent, IsSIP: rp.Kind() == lksdk.ParticipantSIP, Attributes: rp.Attributes()}
}

// SendDTMF sends each digit as a SIP DTMF data packet (RFC 4733 on the SIP leg).
func (s *sdkRoom) SendDTMF(ctx context.Context, digits string) error {
	if s.r.LocalParticipant == nil {
		return fmt.Errorf("livekit: not connected")
	}
	for _, d := range digits {
		if err := ctx.Err(); err != nil {
			return err
		}
		code, ok := dtmfCodes[d]
		if !ok {
			continue
		}
		if err := s.r.LocalParticipant.PublishDTMF(code, string(d)); err != nil {
			return err
		}
		time.Sleep(160 * time.Millisecond)
	}
	return nil
}

// dtmfCodes are the RFC 4733 event codes.
var dtmfCodes = map[rune]uint32{
	'0': 0, '1': 1, '2': 2, '3': 3, '4': 4, '5': 5, '6': 6, '7': 7, '8': 8, '9': 9,
	'*': 10, '#': 11, 'A': 12, 'B': 13, 'C': 14, 'D': 15,
}
