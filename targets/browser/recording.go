package browser

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Recording formats for Config.Recording.
const (
	RecordingAuto = "auto" // webm when ffmpeg is installed, gif otherwise
	RecordingGIF  = "gif"
	RecordingWebM = "webm"
	RecordingOff  = "off"
)

// RecordingFormats are the accepted values of Config.Recording.
var RecordingFormats = []string{RecordingAuto, RecordingGIF, RecordingWebM, RecordingOff}

// The recording files written into the run directory.
const (
	RecordingGIFFile  = "recording.gif"
	RecordingWebMFile = "recording.webm"
)

const (
	// recordingMaxWidth bounds the frames Chromium sends: wide enough to read,
	// small enough to keep the file reasonable.
	recordingMaxWidth = 960
	// recordingMaxFrames bounds memory on a long run; later frames are dropped.
	recordingMaxFrames = 3000
	recordingQuality   = 70
)

// frame is one screencast frame as Chromium sent it.
type frame struct {
	at   time.Time
	jpeg []byte
}

// screencast collects the page's screencast frames for the session recording.
type screencast struct {
	mu      sync.Mutex
	frames  []frame
	dropped int
}

// add keeps one screencast event, skipping a frame identical to the last so
// a still page costs nothing.
func (r *screencast) add(e *page.EventScreencastFrame) {
	// The protocol carries the frame as base64; the decoder already undid it.
	data := e.Data
	if len(data) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.frames); n > 0 && bytes.Equal(r.frames[n-1].jpeg, data) {
		return
	}
	if len(r.frames) >= recordingMaxFrames {
		r.dropped++
		return
	}
	r.frames = append(r.frames, frame{at: time.Now(), jpeg: data})
}

func (r *screencast) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

// startRecording asks Chromium for a screencast of the page.
func (s *session) startRecording(w int) error {
	maxW := w
	if maxW > recordingMaxWidth {
		maxW = recordingMaxWidth
	}
	s.rec = &screencast{}
	return chromedp.Do(s.ctx, call(page.StartScreencast, page.StartScreencastParams{
		Format: page.StartScreencastFormatJpeg, Quality: ptr(int64(recordingQuality)),
		MaxWidth: int64(maxW), MaxHeight: int64(maxW * 4), EveryNthFrame: 1,
	}))
}

// onFrame stores a screencast frame and acknowledges it so the next one
// comes; the ack runs off the event goroutine, which must not block.
func (s *session) onFrame(e *page.EventScreencastFrame) {
	s.rec.add(e)
	go func() {
		_ = chromedp.Do(s.ctx, call(page.ScreencastFrameAck, page.ScreencastFrameAckParams{SessionID: e.SessionID}))
	}()
}

// resolveRecordingFormat turns Config.Recording into the format to write.
func resolveRecordingFormat(format string) string {
	switch format {
	case "", RecordingAuto:
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			return RecordingWebM
		}
		return RecordingGIF
	default:
		return format
	}
}

// writeRecording writes the frames into dir as the configured format and
// returns the file name; a WebM that cannot be encoded falls back to a GIF.
func (r *screencast) write(dir, format string, end time.Time) (string, error) {
	r.mu.Lock()
	frames := append([]frame(nil), r.frames...)
	r.mu.Unlock()
	if len(frames) == 0 {
		return "", nil
	}
	if format == RecordingWebM {
		if err := writeWebM(frames, end, filepath.Join(dir, RecordingWebMFile)); err == nil {
			return RecordingWebMFile, nil
		}
	}
	if err := writeGIF(frames, end, filepath.Join(dir, RecordingGIFFile)); err != nil {
		return "", err
	}
	return RecordingGIFFile, nil
}

// frameDurations gives each frame the time until the next one (the last one
// until end), clamped so a long still does not freeze the playback.
func frameDurations(frames []frame, end time.Time) []time.Duration {
	const minD, maxD = 50 * time.Millisecond, 3 * time.Second
	out := make([]time.Duration, len(frames))
	for i, f := range frames {
		next := end
		if i+1 < len(frames) {
			next = frames[i+1].at
		}
		d := next.Sub(f.at)
		if d < minD {
			d = minD
		}
		if d > maxD {
			d = maxD
		}
		out[i] = d
	}
	return out
}

// writeGIF encodes the frames as an animated GIF with their real timing.
// Chromium's frames can differ in size (the viewport settles after the first
// paints), and a GIF's frames must all fit its canvas, so every frame is
// drawn onto a canvas of the largest size seen.
func writeGIF(frames []frame, end time.Time, path string) error {
	durations := frameDurations(frames, end)
	var imgs []image.Image
	var delays []int
	w, h := 0, 0
	for i, f := range frames {
		img, err := jpeg.Decode(bytes.NewReader(f.jpeg))
		if err != nil {
			continue
		}
		imgs = append(imgs, img)
		delays = append(delays, int(durations[i]/(10*time.Millisecond)))
		w, h = max(w, img.Bounds().Dx()), max(h, img.Bounds().Dy())
	}
	if len(imgs) == 0 {
		return fmt.Errorf("browser: no decodable frame for the recording")
	}
	g := &gif.GIF{LoopCount: 0, Config: image.Config{ColorModel: color.Palette(palette.Plan9), Width: w, Height: h}}
	for i, img := range imgs {
		pal := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
		draw.Draw(pal, img.Bounds().Sub(img.Bounds().Min), img, img.Bounds().Min, draw.Src)
		g.Image = append(g.Image, pal)
		g.Delay = append(g.Delay, delays[i])
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := gif.EncodeAll(f, g); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// writeWebM encodes the frames with ffmpeg (VP9) through its concat demuxer,
// which carries each frame's duration.
func writeWebM(frames []frame, end time.Time, path string) error {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "tamesu-recording-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	durations := frameDurations(frames, end)
	var list strings.Builder
	for i, f := range frames {
		name := fmt.Sprintf("%06d.jpg", i)
		if err := os.WriteFile(filepath.Join(tmp, name), f.jpeg, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(&list, "file '%s'\nduration %.3f\n", name, durations[i].Seconds())
	}
	// The concat demuxer ignores the last duration unless the file repeats.
	fmt.Fprintf(&list, "file '%06d.jpg'\n", len(frames)-1)
	if err := os.WriteFile(filepath.Join(tmp, "frames.txt"), []byte(list.String()), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(ffmpeg, "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", "frames.txt",
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-fps_mode", "vfr", "-pix_fmt", "yuv420p",
		"-c:v", "libvpx-vp9", "-b:v", "0", "-crf", "34", path)
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
