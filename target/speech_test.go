package target

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchSpeech transcribes whole utterances only.
type batchSpeech struct {
	mu   sync.Mutex
	reqs []TranscribeRequest
}

func (b *batchSpeech) Synthesize(context.Context, SynthesizeRequest) (Audio, error) {
	return Audio{}, errors.New("not used")
}

func (b *batchSpeech) Transcribe(_ context.Context, req TranscribeRequest) (Transcript, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reqs = append(b.reqs, req)
	return Transcript{Text: "heard", Confidence: 0.5}, nil
}

// streamSpeech hands out recording streams.
type streamSpeech struct {
	batchSpeech
	stream *recordingStream
	err    error
}

func (s *streamSpeech) OpenTranscription(context.Context, TranscriptionStreamRequest) (TranscriptionStream, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.stream, nil
}

type recordingStream struct {
	mu       sync.Mutex
	writes   [][]int16
	closed   bool
	writeErr error
	block    chan struct{} // when set, Write waits on it
}

func (r *recordingStream) Write(_ context.Context, samples []int16) error {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, samples)
	return r.writeErr
}

func (r *recordingStream) Close(context.Context) (Transcript, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return Transcript{Text: "streamed"}, nil
}

func TestStartTranscriptionBuffersForBatchSpeech(t *testing.T) {
	sp := &batchSpeech{}
	st, err := StartTranscription(context.Background(), sp, TranscriptionStreamRequest{Rate: 8000, Language: "en", Model: "x/y"})
	require.NoError(t, err)
	require.NoError(t, st.Write(context.Background(), []int16{1, 2}))
	require.NoError(t, st.Write(context.Background(), []int16{3}))
	assert.Empty(t, sp.reqs, "nothing is sent before Close")
	tr, err := st.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "heard", tr.Text)
	require.Len(t, sp.reqs, 1)
	assert.Equal(t, TranscribeRequest{Audio: Audio{Rate: 8000, Samples: []int16{1, 2, 3}}, Language: "en", Model: "x/y"}, sp.reqs[0])

	empty, err := StartTranscription(context.Background(), sp, TranscriptionStreamRequest{Rate: 8000})
	require.NoError(t, err)
	tr, err = empty.Close(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tr.Text)
	assert.Len(t, sp.reqs, 1, "no audio, no call")
}

func TestStartTranscriptionForwardsInOrder(t *testing.T) {
	rec := &recordingStream{}
	sp := &streamSpeech{stream: rec}
	st, err := StartTranscription(context.Background(), sp, TranscriptionStreamRequest{Rate: 16000})
	require.NoError(t, err)
	for i := range 50 {
		require.NoError(t, st.Write(context.Background(), []int16{int16(i)}))
	}
	tr, err := st.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "streamed", tr.Text)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.True(t, rec.closed)
	require.Len(t, rec.writes, 50, "every write reaches the stream, before Close")
	for i, w := range rec.writes {
		assert.Equal(t, []int16{int16(i)}, w)
	}
	assert.Empty(t, sp.reqs, "the batch path was not used")
	assert.Error(t, st.Write(context.Background(), []int16{1}), "a write after Close fails")
}

func TestStartTranscriptionWriteNeverBlocks(t *testing.T) {
	rec := &recordingStream{block: make(chan struct{})}
	sp := &streamSpeech{stream: rec}
	st, err := StartTranscription(context.Background(), sp, TranscriptionStreamRequest{Rate: 16000})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10 {
			_ = st.Write(context.Background(), []int16{1})
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Write blocked on a slow stream")
	}
	close(rec.block)
	_, err = st.Close(context.Background())
	require.NoError(t, err)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Len(t, rec.writes, 10)
}

func TestStartTranscriptionReportsWriteErrorAtClose(t *testing.T) {
	rec := &recordingStream{writeErr: errors.New("socket gone")}
	sp := &streamSpeech{stream: rec}
	st, err := StartTranscription(context.Background(), sp, TranscriptionStreamRequest{Rate: 16000})
	require.NoError(t, err)
	_ = st.Write(context.Background(), []int16{1})
	_, err = st.Close(context.Background())
	assert.ErrorContains(t, err, "socket gone")
	assert.True(t, rec.closed, "the stream is released even so")

	_, err = StartTranscription(context.Background(), &streamSpeech{err: errors.New("no network")}, TranscriptionStreamRequest{Rate: 16000})
	assert.ErrorContains(t, err, "no network")
}

func TestStartTranscriptionCloseIsBoundedByContext(t *testing.T) {
	rec := &recordingStream{block: make(chan struct{})}
	defer close(rec.block)
	st, err := StartTranscription(context.Background(), &streamSpeech{stream: rec}, TranscriptionStreamRequest{Rate: 16000})
	require.NoError(t, err)
	_ = st.Write(context.Background(), []int16{1})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = st.Close(ctx)
	assert.Less(t, time.Since(start), time.Second, "Close returns once the ctx ends even with a Write stuck")
}
