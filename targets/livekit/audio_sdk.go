package livekit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/xavidop/tamesu/audio/opus"
)

// maxOpusFrame is the largest Opus frame (120 ms at 48 kHz).
const maxOpusFrame = AudioRate * 120 / 1000

// subscribeAudio wires the SDK callbacks that deliver the agent's audio: every
// audio track is subscribed as it is published, read as RTP and decoded.
func subscribeAudio(cb *lksdk.RoomCallback, ev events) {
	cb.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		if pub.Kind() == lksdk.TrackKindAudio {
			_ = pub.SetSubscribed(true)
		}
	}
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		go readAudio(track, rp.Identity(), ev)
	}
}

// subscribeExisting subscribes to the audio tracks already published when we joined.
func subscribeExisting(r *lksdk.Room) {
	for _, rp := range r.GetRemoteParticipants() {
		for _, pub := range rp.TrackPublications() {
			if rpub, ok := pub.(*lksdk.RemoteTrackPublication); ok && pub.Kind() == lksdk.TrackKindAudio {
				_ = rpub.SetSubscribed(true)
			}
		}
	}
}

// readAudio decodes a remote Opus track to 48 kHz mono and hands the frames
// to the session until the track ends.
func readAudio(track *webrtc.TrackRemote, identity string, ev events) {
	dec, err := opus.NewDecoder(AudioRate, 1)
	if err != nil {
		return
	}
	defer dec.Close()
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if len(pkt.Payload) == 0 {
			continue
		}
		pcm, err := dec.Decode(pkt.Payload, maxOpusFrame)
		if err != nil || len(pcm) == 0 {
			continue
		}
		ev.onAudio(identity, append([]int16(nil), pcm...))
	}
}

// PublishAudio publishes the caller's microphone track (Opus, 48 kHz mono).
func (s *sdkRoom) PublishAudio(ctx context.Context) (audioPublisher, error) {
	if !opus.Available() {
		return nil, opus.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.r.LocalParticipant == nil {
		return nil, errors.New("livekit: not connected")
	}
	// The RTP codec is pion's registered Opus (48 kHz, 2 channels in the SDP);
	// the packets themselves are mono, which Opus signals in-band.
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: AudioRate, Channels: 2})
	if err != nil {
		return nil, fmt.Errorf("livekit: audio track: %w", err)
	}
	pub, err := s.r.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "microphone", Source: livekit.TrackSource_MICROPHONE, DisableDTX: true})
	if err != nil {
		return nil, fmt.Errorf("livekit: publish audio: %w", err)
	}
	enc, err := opus.NewEncoder(AudioRate, 1, 32000)
	if err != nil {
		return nil, err
	}
	return &sdkPublisher{room: s.r, track: track, sid: pub.SID(), enc: enc}, nil
}

type sdkPublisher struct {
	room  *lksdk.Room
	track *lksdk.LocalTrack
	sid   string
	enc   *opus.Encoder
}

func (p *sdkPublisher) WriteFrame(pcm []int16) error {
	pkt, err := p.enc.Encode(pcm)
	if err != nil {
		return err
	}
	return p.track.WriteSample(media.Sample{Data: append([]byte(nil), pkt...), Duration: FrameDur}, nil)
}

func (p *sdkPublisher) Close() error {
	p.enc.Close()
	if p.room.LocalParticipant != nil {
		_ = p.room.LocalParticipant.UnpublishTrack(p.sid)
	}
	return nil
}

// A publisher must never block the publish loop for long; WriteSample paces
// itself, so a 20 ms frame takes about 20 ms.
var _ = time.Second
