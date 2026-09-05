package browse

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
)

// Screencast limits.
const (
	// ScreencastMaxFrames is the maximum number of frames before auto-stopping.
	ScreencastMaxFrames = 10000
	// ScreencastMaxDuration is the maximum duration before auto-stopping.
	ScreencastMaxDuration = 30 * time.Minute
	// ScreencastDir is the directory where screencast output files are stored.
	ScreencastDir = "/tmp/shelley-screencasts"
)

// screencastState holds the state of an active screencast recording.
type screencastState struct {
	mu         sync.Mutex
	active     bool
	starting   bool // true while screencastStart is in progress (prevents TOCTOU)
	sessionID  string
	outputPath string
	frameCount int
	startTime  time.Time
	stopTimer  *time.Timer

	// ffmpeg process — frames are piped directly to stdin.
	ffmpegCmd *exec.Cmd
	ffmpegIn  io.WriteCloser // ffmpeg's stdin pipe

	// ackCh sends frame session IDs to the ack goroutine.
	ackCh chan int64
	// stopCh is closed to signal the ack goroutine to stop.
	stopCh chan struct{}
	// stopped is closed by the ack goroutine when it exits.
	stopped chan struct{}
}

// handleScreencastFrame processes incoming screencast frame events.
// Called from handleBrowserEvent — must NOT call chromedp.Run (deadlock).
func (b *BrowseTools) handleScreencastFrame(e *page.EventScreencastFrame) {
	sc := &b.screencast
	sc.mu.Lock()
	if !sc.active {
		sc.mu.Unlock()
		return
	}

	// Check frame limit.
	if sc.frameCount >= ScreencastMaxFrames {
		log.Printf("screencast: max frames (%d) reached, will auto-stop", ScreencastMaxFrames)
		sc.mu.Unlock()
		// Full teardown in a goroutine (can't call chromedp.Run from here).
		go b.screencastStopInternal()
		return
	}

	sc.frameCount++
	ffmpegIn := sc.ffmpegIn
	ackCh := sc.ackCh
	sc.mu.Unlock()

	// Decode and pipe frame to ffmpeg outside the lock.
	data, err := base64.StdEncoding.DecodeString(e.Data)
	if err != nil {
		log.Printf("screencast: failed to decode frame: %v", err)
	} else if ffmpegIn != nil {
		if _, err := ffmpegIn.Write(data); err != nil {
			log.Printf("screencast: failed to write frame to ffmpeg: %v", err)
		}
	}

	// Send ack to background goroutine (non-blocking).
	select {
	case ackCh <- e.SessionID:
	default:
	}
}

// screencastAckLoop runs in a goroutine and acks screencast frames.
// It stops the CDP screencast and exits when stopCh is closed.
func (b *BrowseTools) screencastAckLoop(browserCtx context.Context, ackCh chan int64, stopCh, stopped chan struct{}) {
	defer close(stopped)
	for {
		select {
		case sessionID := <-ackCh:
			if err := chromedp.Run(browserCtx, page.ScreencastFrameAck(sessionID)); err != nil {
				log.Printf("screencast: failed to ack frame: %v", err)
			}
		case <-stopCh:
			// Drain any pending acks.
			for {
				select {
				case sessionID := <-ackCh:
					if err := chromedp.Run(browserCtx, page.ScreencastFrameAck(sessionID)); err != nil {
						log.Printf("screencast: failed to ack frame during drain: %v", err)
					}
				default:
					goto done
				}
			}
		}
	}
done:
	if err := chromedp.Run(browserCtx, page.StopScreencast()); err != nil {
		log.Printf("screencast: failed to stop CDP screencast: %v", err)
	}
}

// screencastStart begins a screencast recording, piping frames into ffmpeg.
func (b *BrowseTools) screencastStart(format string, quality, maxWidth, maxHeight, everyNthFrame int64) (string, error) {
	sc := &b.screencast
	sc.mu.Lock()
	if sc.active || sc.starting {
		sid := sc.sessionID
		fc := sc.frameCount
		sc.mu.Unlock()
		return "", fmt.Errorf("screencast is already active (session %s, %d frames so far) — stop it first", sid, fc)
	}
	sc.starting = true
	sc.mu.Unlock()

	var started bool
	defer func() {
		if !started {
			sc.mu.Lock()
			sc.starting = false
			sc.mu.Unlock()
		}
	}()

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return "", err
	}

	// Defaults.
	scFormat := page.ScreencastFormatJpeg
	inputFormat := "mjpeg"
	if format == "png" {
		scFormat = page.ScreencastFormatPng
		inputFormat = "image2pipe" // for piped PNG frames
	}
	if quality <= 0 {
		quality = 60
	}
	if maxWidth <= 0 {
		maxWidth = 1280
	}
	if maxHeight <= 0 {
		maxHeight = 720
	}
	if everyNthFrame <= 0 {
		everyNthFrame = 1
	}

	sessionID := uuid.New().String()[:8]
	if err := os.MkdirAll(ScreencastDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create screencast dir: %w", err)
	}
	outputPath := filepath.Join(ScreencastDir, sessionID+".mp4")

	// Start ffmpeg: read frames from stdin, output MP4.
	// -framerate 4: assume ~4fps from Chrome screencast (adjustable via every_nth_frame)
	// -f mjpeg or image2pipe: tell ffmpeg the input format
	// -vf crop=trunc(iw/2)*2:trunc(ih/2)*2: trim to even dimensions (at most a
	//   1px centered crop, no rescaling) — libx264 with yuv420p cannot encode
	//   odd sizes, which would otherwise kill ffmpeg on the first frame and
	//   yield a 0-byte MP4. Covers odd viewports, fractional device scale
	//   factors, downscale rounding, zoom, and mid-recording resizes.
	// -c:v libx264 -pix_fmt yuv420p: widely compatible H.264 MP4
	ffmpegCmd := exec.Command(
		"ffmpeg",
		"-y",
		"-f", inputFormat,
		"-framerate", "4",
		"-i", "pipe:0",
		"-vf", "crop=trunc(iw/2)*2:trunc(ih/2)*2",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "fast",
		"-movflags", "+faststart",
		outputPath,
	)
	ffmpegIn, err := ffmpegCmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create ffmpeg stdin pipe: %w", err)
	}
	// Capture stderr for diagnostics on failure.
	ffmpegCmd.Stderr = &limitedBuffer{max: 4096}

	if err := ffmpegCmd.Start(); err != nil {
		ffmpegIn.Close()
		return "", fmt.Errorf("failed to start ffmpeg (is it installed?): %w", err)
	}

	// Start CDP screencast.
	err = chromedp.Run(
		browserCtx,
		page.StartScreencast().
			WithFormat(scFormat).
			WithQuality(quality).
			WithMaxWidth(maxWidth).
			WithMaxHeight(maxHeight).
			WithEveryNthFrame(everyNthFrame),
	)
	if err != nil {
		ffmpegIn.Close()
		ffmpegCmd.Wait()
		os.Remove(outputPath)
		return "", fmt.Errorf("failed to start screencast: %w", err)
	}

	ackCh := make(chan int64, 4)
	stopCh := make(chan struct{})
	stoppedCh := make(chan struct{})
	go b.screencastAckLoop(browserCtx, ackCh, stopCh, stoppedCh)

	started = true
	sc.mu.Lock()
	sc.active = true
	sc.starting = false
	sc.sessionID = sessionID
	sc.outputPath = outputPath
	sc.frameCount = 0
	sc.startTime = time.Now()
	sc.ffmpegCmd = ffmpegCmd
	sc.ffmpegIn = ffmpegIn
	sc.ackCh = ackCh
	sc.stopCh = stopCh
	sc.stopped = stoppedCh
	sc.stopTimer = time.AfterFunc(ScreencastMaxDuration, func() {
		log.Printf("screencast: max duration (%v) reached, auto-stopping", ScreencastMaxDuration)
		b.screencastStopInternal()
	})
	sc.mu.Unlock()

	return sessionID, nil
}

// screencastStopInternal stops the screencast and verifies the recording.
// Safe to call from any goroutine. recErr is non-nil if ffmpeg failed or the
// output file is missing/empty/unplayable, so callers can surface encoder
// failures instead of silently reporting success. note carries a non-fatal
// caveat about the recording (empty when there is nothing to flag).
func (b *BrowseTools) screencastStopInternal() (recErr error, note string) {
	sc := &b.screencast
	sc.mu.Lock()
	if !sc.active {
		sc.mu.Unlock()
		return nil, ""
	}
	sc.active = false
	if sc.stopTimer != nil {
		sc.stopTimer.Stop()
		sc.stopTimer = nil
	}
	stopCh := sc.stopCh
	stopped := sc.stopped
	ffmpegIn := sc.ffmpegIn
	ffmpegCmd := sc.ffmpegCmd
	outputPath := sc.outputPath
	sc.stopCh = nil
	sc.ffmpegIn = nil
	sc.mu.Unlock()

	// Signal the ack goroutine to stop.
	if stopCh != nil {
		close(stopCh)
	}
	if stopped != nil {
		<-stopped
	}

	// Close ffmpeg's stdin to signal EOF, then wait for it to finish encoding.
	if ffmpegIn != nil {
		ffmpegIn.Close()
	}
	var stderr string
	var waitErr error
	if ffmpegCmd != nil {
		waitErr = ffmpegCmd.Wait()
		if lb, ok := ffmpegCmd.Stderr.(*limitedBuffer); ok {
			stderr = lb.String()
		}
		if waitErr != nil {
			log.Printf("screencast: ffmpeg exited with error: %v; stderr: %s", waitErr, stderr)
		}
	}

	// Verify the output file actually exists and has content — a failed
	// encoder leaves a 0-byte (or missing) file. A non-empty file is probed
	// with ffprobe before declaring failure, because ffmpeg can exit
	// non-zero (e.g. on SIGTERM) while still finalizing a complete, playable
	// MP4.
	if outputPath != "" {
		info, statErr := os.Stat(outputPath)
		switch {
		case statErr != nil:
			recErr = fmt.Errorf("output file %s was not created (%v)", outputPath, statErr)
		case info.Size() == 0:
			recErr = fmt.Errorf("output file %s is empty", outputPath)
		case waitErr != nil:
			if probeErr := verifyPlayable(outputPath); probeErr != nil {
				recErr = fmt.Errorf("ffmpeg exited with error (%v) and the output MP4 did not verify as playable (%v)", waitErr, probeErr)
			} else {
				note = fmt.Sprintf("ffmpeg exited with an error (%v), but the output MP4 was verified playable", waitErr)
			}
		}
	}
	if recErr != nil {
		// Surface the most diagnostic part of ffmpeg's stderr (the last lines
		// carry the actual failure reason) to the caller.
		if tail := stderrTail(stderr, 600); tail != "" {
			recErr = fmt.Errorf("%w\nffmpeg stderr (tail): %s", recErr, tail)
		}
	}
	return recErr, note
}

// verifyPlayable reports whether the MP4 at path contains a decodable video
// stream, using ffprobe. It fails (fail closed) if ffprobe is unavailable.
func verifyPlayable(path string) error {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return fmt.Errorf("ffprobe not available: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	if err != nil {
		// Include ffprobe's own diagnostic text (e.g. "moov atom not found")
		// when available.
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("no video stream found")
	}
	return nil
}

// stderrTail returns the last maxRunes runes of ffmpeg's stderr, with the
// version/configuration banner (which carries no diagnostic value) stripped
// and carriage returns from progress updates normalized.
func stderrTail(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isBannerLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	joined := strings.Join(kept, " | ")
	r := []rune(joined)
	if len(r) > maxRunes {
		r = r[len(r)-maxRunes:]
	}
	return string(r)
}

func isBannerLine(line string) bool {
	for _, prefix := range []string{
		"ffmpeg version", "built with", "configuration:",
		"libavutil", "libavcodec", "libavformat", "libavdevice",
		"libavfilter", "libswscale", "libswresample", "libpostproc",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// screencastSummary describes a completed (or failed) screencast recording.
type screencastSummary struct {
	SessionID  string
	OutputPath string
	FrameCount int
	Duration   time.Duration
	// RecErr is non-nil if the ffmpeg encoder failed or produced no usable
	// output.
	RecErr error
	// Note carries a non-fatal caveat about the recording (empty when there
	// is nothing to flag).
	Note string
}

// screencastStop stops the screencast and returns summary info.
func (b *BrowseTools) screencastStop() (screencastSummary, error) {
	sc := &b.screencast
	sc.mu.Lock()
	if !sc.active {
		sc.mu.Unlock()
		return screencastSummary{}, fmt.Errorf("no active screencast — call screencast_start first")
	}
	sum := screencastSummary{
		SessionID:  sc.sessionID,
		OutputPath: sc.outputPath,
		FrameCount: sc.frameCount,
		Duration:   time.Since(sc.startTime),
	}
	sc.mu.Unlock()

	sum.RecErr, sum.Note = b.screencastStopInternal()
	return sum, nil
}

// screencastStatus returns the current status of the screencast.
func (b *BrowseTools) screencastStatus() (active bool, sessionID string, frameCount int, elapsed time.Duration) {
	sc := &b.screencast
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.active {
		return false, "", 0, 0
	}
	return true, sc.sessionID, sc.frameCount, time.Since(sc.startTime)
}

// limitedBuffer is a byte buffer that keeps only the last max bytes written.
// ffmpeg writes its fatal error message at the very end of stderr (after
// thousands of progress updates), so keeping the head would discard exactly
// the useful part.
type limitedBuffer struct {
	buf []byte
	max int
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) >= lb.max {
		lb.buf = append(lb.buf[:0], p[len(p)-lb.max:]...)
		return len(p), nil
	}
	lb.buf = append(lb.buf, p...)
	if overflow := len(lb.buf) - lb.max; overflow > 0 {
		lb.buf = lb.buf[overflow:]
	}
	// Always report full length consumed so ffmpeg doesn't get write errors.
	return len(p), nil
}

func (lb *limitedBuffer) String() string {
	return string(lb.buf)
}
