// Package vad detects speech in PCM: a Detector classifies fixed frames, and
// the Segmenter turns frame decisions into speech start and end events with
// hangover, which the audio metrics (time to first audio, interruptions,
// talk-over, silence gaps) are computed from. The Energy detector is pure Go
// and the default; Silero (onnxruntime) is behind the onnx build tag.
package vad

import (
	"math"
	"time"
)

// Detector classifies one frame of 16-bit PCM as speech.
type Detector interface {
	// Speech reports whether the frame holds speech. Frames are FrameSize
	// samples at Rate.
	Speech(frame []int16) bool
	// Rate and FrameSize are what the detector expects.
	Rate() int
	FrameSize() int
	Reset()
}

// Event is a speech boundary.
type Event struct {
	Kind EventKind
	At   time.Duration
}

// EventKind is SpeechStart or SpeechEnd.
type EventKind string

// Event kinds.
const (
	SpeechStart EventKind = "speech_start"
	SpeechEnd   EventKind = "speech_end"
)

// Energy is an RMS detector with an adaptive noise floor: a frame is speech
// when its level is above both an absolute threshold and the floor plus a
// margin. Simple, fast, deterministic; good for the clean audio of a TTS
// voice and a LiveKit agent.
type Energy struct {
	rate       int
	frame      int
	thresholdD float64 // absolute threshold, dBFS
	marginDB   float64 // above the noise floor
	floor      float64 // running estimate of the noise level (linear)
	frames     int
}

// EnergyOptions configure the Energy detector; zero values take the defaults
// (16 kHz, 10 ms frames, -40 dBFS, 10 dB over the floor).
type EnergyOptions struct {
	Rate        int
	Frame       time.Duration
	ThresholdDB float64
	MarginDB    float64
}

// NewEnergy returns an Energy detector.
func NewEnergy(o EnergyOptions) *Energy {
	if o.Rate == 0 {
		o.Rate = 16000
	}
	if o.Frame == 0 {
		o.Frame = 10 * time.Millisecond
	}
	if o.ThresholdDB == 0 {
		o.ThresholdDB = -40
	}
	if o.MarginDB == 0 {
		o.MarginDB = 10
	}
	e := &Energy{rate: o.Rate, frame: int(math.Round(o.Frame.Seconds() * float64(o.Rate))), thresholdD: o.ThresholdDB, marginDB: o.MarginDB}
	e.Reset()
	return e
}

func (e *Energy) Rate() int      { return e.rate }
func (e *Energy) FrameSize() int { return e.frame }

// Reset forgets the noise floor.
func (e *Energy) Reset() {
	e.floor = math.Pow(10, -60/20) // start at -60 dBFS
	e.frames = 0
}

// Speech implements Detector.
func (e *Energy) Speech(frame []int16) bool {
	var sum float64
	for _, s := range frame {
		f := float64(s) / 32768
		sum += f * f
	}
	level := 0.0
	if len(frame) > 0 {
		level = math.Sqrt(sum / float64(len(frame)))
	}
	db := -120.0
	if level > 0 {
		db = 20 * math.Log10(level)
	}
	floorDB := -120.0
	if e.floor > 0 {
		floorDB = 20 * math.Log10(e.floor)
	}
	speech := db > e.thresholdD && db > floorDB+e.marginDB
	// The floor tracks non-speech frames only, so a long utterance cannot
	// pull it up to its own level and cut itself off.
	if !speech {
		e.floor = e.floor*0.9 + level*0.1
	}
	e.frames++
	return speech
}

// Segmenter turns frame decisions into speech segments.
type Segmenter struct {
	det       Detector
	hangover  time.Duration // silence needed to end a segment
	minSpeech time.Duration // speech needed to start one
	frameDur  time.Duration
	pos       time.Duration
	inSpeech  bool
	speechRun time.Duration
	silentRun time.Duration
	startAt   time.Duration
	rest      []int16
}

// SegmenterOptions configure the Segmenter; zero values take 300 ms hangover
// and 60 ms minimum speech.
type SegmenterOptions struct {
	Hangover  time.Duration
	MinSpeech time.Duration
}

// NewSegmenter returns a Segmenter over det.
func NewSegmenter(det Detector, o SegmenterOptions) *Segmenter {
	if o.Hangover == 0 {
		o.Hangover = 300 * time.Millisecond
	}
	if o.MinSpeech == 0 {
		o.MinSpeech = 60 * time.Millisecond
	}
	return &Segmenter{det: det, hangover: o.Hangover, minSpeech: o.MinSpeech, frameDur: time.Duration(float64(det.FrameSize()) / float64(det.Rate()) * float64(time.Second))}
}

// Push feeds samples (at the detector's rate) and returns the boundary events
// they produced. Positions are from the first sample ever pushed.
func (s *Segmenter) Push(samples []int16) []Event {
	var events []Event
	buf := append(s.rest, samples...)
	size := s.det.FrameSize()
	i := 0
	for ; i+size <= len(buf); i += size {
		frame := buf[i : i+size]
		speech := s.det.Speech(frame)
		frameEnd := s.pos + s.frameDur
		switch {
		case speech && !s.inSpeech:
			s.speechRun += s.frameDur
			s.silentRun = 0
			if s.speechRun >= s.minSpeech {
				s.inSpeech = true
				s.startAt = frameEnd - s.speechRun
				events = append(events, Event{Kind: SpeechStart, At: s.startAt})
			}
		case speech && s.inSpeech:
			s.silentRun = 0
		case !speech && s.inSpeech:
			s.silentRun += s.frameDur
			if s.silentRun >= s.hangover {
				s.inSpeech = false
				s.speechRun = 0
				events = append(events, Event{Kind: SpeechEnd, At: frameEnd - s.silentRun})
			}
		default:
			s.speechRun = 0
		}
		s.pos = frameEnd
	}
	s.rest = append([]int16(nil), buf[i:]...)
	return events
}

// Flush ends an open segment at the current position.
func (s *Segmenter) Flush() []Event {
	if !s.inSpeech {
		return nil
	}
	s.inSpeech = false
	return []Event{{Kind: SpeechEnd, At: s.pos - s.silentRun}}
}

// Position is how much audio has been pushed.
func (s *Segmenter) Position() time.Duration { return s.pos }

// InSpeech reports whether a segment is open.
func (s *Segmenter) InSpeech() bool { return s.inSpeech }
