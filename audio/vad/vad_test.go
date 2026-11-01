package vad

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
)

// speechLike is a tone with amplitude wobble, loud enough for the detector.
func speechLike(rate int, d time.Duration) audio.PCM {
	return audio.Tone(rate, 220, d, 0.3)
}

func TestEnergySegmenterFindsSpeechBoundaries(t *testing.T) {
	const rate = 16000
	// 500 ms silence, 800 ms speech, 600 ms silence, 400 ms speech, 500 ms silence.
	pcm := audio.Silence(rate, 500*time.Millisecond)
	pcm = audio.Concat(pcm, speechLike(rate, 800*time.Millisecond))
	pcm = audio.Concat(pcm, audio.Silence(rate, 600*time.Millisecond))
	pcm = audio.Concat(pcm, speechLike(rate, 400*time.Millisecond))
	pcm = audio.Concat(pcm, audio.Silence(rate, 500*time.Millisecond))

	seg := NewSegmenter(NewEnergy(EnergyOptions{Rate: rate}), SegmenterOptions{})
	var events []Event
	// Push in 37 ms chunks to exercise partial frames.
	for _, chunk := range chunks(pcm.Samples, 592) {
		events = append(events, seg.Push(chunk)...)
	}
	events = append(events, seg.Flush()...)
	require.Len(t, events, 4, "%v", events)
	assert.Equal(t, SpeechStart, events[0].Kind)
	assert.InDelta(t, 0.5, events[0].At.Seconds(), 0.03, "speech starts at 500 ms")
	assert.Equal(t, SpeechEnd, events[1].Kind)
	assert.InDelta(t, 1.3, events[1].At.Seconds(), 0.03, "speech ends at 1300 ms (hangover removed)")
	assert.Equal(t, SpeechStart, events[2].Kind)
	assert.InDelta(t, 1.9, events[2].At.Seconds(), 0.03)
	assert.Equal(t, SpeechEnd, events[3].Kind)
	assert.InDelta(t, 2.3, events[3].At.Seconds(), 0.03)
	assert.InDelta(t, 2.8, seg.Position().Seconds(), 0.04)
	assert.False(t, seg.InSpeech())
}

func TestEnergyIgnoresShortBlipsAndNoiseFloor(t *testing.T) {
	const rate = 16000
	det := NewEnergy(EnergyOptions{Rate: rate})
	seg := NewSegmenter(det, SegmenterOptions{MinSpeech: 100 * time.Millisecond})
	// A 40 ms blip is shorter than MinSpeech.
	pcm := audio.Concat(audio.Silence(rate, 200*time.Millisecond), speechLike(rate, 40*time.Millisecond))
	pcm = audio.Concat(pcm, audio.Silence(rate, 300*time.Millisecond))
	events := seg.Push(pcm.Samples)
	events = append(events, seg.Flush()...)
	assert.Empty(t, events)

	// Quiet steady noise at -45 dBFS is below the threshold.
	det.Reset()
	quiet := audio.Gain(audio.Tone(rate, 100, time.Second, 1), 0.005)
	seg = NewSegmenter(det, SegmenterOptions{})
	assert.Empty(t, seg.Push(quiet.Samples))
	assert.Empty(t, seg.Flush())
}

func TestFlushClosesAnOpenSegment(t *testing.T) {
	seg := NewSegmenter(NewEnergy(EnergyOptions{Rate: 8000, Frame: 20 * time.Millisecond}), SegmenterOptions{})
	ev := seg.Push(speechLike(8000, 300*time.Millisecond).Samples)
	require.Len(t, ev, 1)
	assert.Equal(t, SpeechStart, ev[0].Kind)
	ev = seg.Flush()
	require.Len(t, ev, 1)
	assert.Equal(t, SpeechEnd, ev[0].Kind)
	assert.InDelta(t, 0.3, ev[0].At.Seconds(), 0.03)
	assert.Equal(t, 160, seg.det.FrameSize())
}

func chunks(s []int16, n int) [][]int16 {
	var out [][]int16
	for i := 0; i < len(s); i += n {
		end := min(i+n, len(s))
		out = append(out, s[i:end])
	}
	return out
}

func TestNew(t *testing.T) {
	det, err := New("", Options{Rate: 8000})
	require.NoError(t, err)
	assert.Equal(t, 8000, det.Rate())
	_, err = New("silero", Options{Rate: 16000})
	if SileroAvailable() {
		// Needs the onnxruntime shared library; an error here names it.
		if err != nil {
			assert.ErrorContains(t, err, "onnxruntime")
		}
	} else {
		assert.ErrorIs(t, err, ErrSileroUnavailable)
	}
	_, err = New("webrtc", Options{})
	assert.ErrorContains(t, err, `unknown detector "webrtc"`)
}
