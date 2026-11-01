package model

import "github.com/xavidop/tamesu/target"

// The speech types live in the target package, where voice targets consume
// them; they are aliased here so the AI layer's contract reads in one place.
type (
	Audio             = target.Audio
	SynthesizeRequest = target.SynthesizeRequest
	TranscribeRequest = target.TranscribeRequest
	Transcript        = target.Transcript
	// Speech is the speech layer of audio mode. The llm module implements it.
	Speech = target.Speech
)
