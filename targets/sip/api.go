package sip

import (
	"context"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ParticipantRequest is an outbound call: a SIP participant dialed into a room.
type ParticipantRequest struct {
	Room, TrunkID, To, From, Identity string
	RingTimeout, MaxCallDuration      time.Duration
	Headers                           map[string]string
}

// API is the slice of the LiveKit SIP and room APIs the target uses; the
// SDK and the test fake implement it.
type API interface {
	// CreateParticipant dials To through the trunk into Room and returns
	// once the call is answered (or fails).
	CreateParticipant(ctx context.Context, req ParticipantRequest) error
	// RemoveParticipant hangs up a SIP participant.
	RemoveParticipant(ctx context.Context, room, identity string) error
}

// HTTPURL turns a ws(s):// LiveKit URL into the http(s):// API URL.
func HTTPURL(u string) string {
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	}
	return u
}

type sdkAPI struct {
	sip  *lksdk.SIPClient
	room *lksdk.RoomServiceClient
}

func newSDK(url, key, secret string) *sdkAPI {
	h := HTTPURL(url)
	return &sdkAPI{sip: lksdk.NewSIPClient(h, key, secret), room: lksdk.NewRoomServiceClient(h, key, secret)}
}

func (a *sdkAPI) CreateParticipant(ctx context.Context, req ParticipantRequest) error {
	r := &livekit.CreateSIPParticipantRequest{
		SipTrunkId:          req.TrunkID,
		SipCallTo:           req.To,
		SipNumber:           req.From,
		RoomName:            req.Room,
		ParticipantIdentity: req.Identity,
		ParticipantName:     "tamesu callee",
		ParticipantAttributes: map[string]string{
			"tamesu.role": "agent",
		},
		Headers:           req.Headers,
		WaitUntilAnswered: true,
	}
	if req.RingTimeout > 0 {
		r.RingingTimeout = durationpb.New(req.RingTimeout)
	}
	if req.MaxCallDuration > 0 {
		r.MaxCallDuration = durationpb.New(req.MaxCallDuration)
	}
	_, err := a.sip.CreateSIPParticipant(ctx, r)
	return err
}

func (a *sdkAPI) RemoveParticipant(ctx context.Context, room, identity string) error {
	_, err := a.room.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{Room: room, Identity: identity})
	return err
}
