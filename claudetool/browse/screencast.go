package browse

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	// screencastStopTimeout bounds finalization if ffmpeg stops reading stdin.
	screencastStopTimeout = 30 * time.Second
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
	writers   sync.WaitGroup // frame writes already in progress when stopping
	stop      *screencastStopResult

	// ackCh sends frame session IDs to the ack goroutine.
	ackCh chan int64
	// stopCh is closed to signal the ack goroutine to stop.
	stopCh chan struct{}
	// stopped is closed by the ack goroutine when it exits.
	stopped chan struct{}
}

type screencastStopResult struct {
	done chan struct{}
	err  error // written before done is closed
}

type screencastStopResources struct {
	result     *screencastStopResult
	stopCh     chan struct{}
	stopped    chan struct{}
	ffmpegIn   io.WriteCloser
	ffmpegCmd  *exec.Cmd
	outputPath string
	timeout    time.Duration
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
		go func() {
			if err := b.screencastStopInternal(); err != nil {
				log.Printf("screencast: auto-stop failed: %v", err)
			}
		}()
		return
	}

	sc.frameCount++
	ffmpegIn := sc.ffmpegIn
	ackCh := sc.ackCh
	sc.writers.Add(1)
	sc.mu.Unlock()
	defer sc.writers.Done()

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
	if sc.stop != nil {
		select {
		case <-sc.stop.done:
			sc.stop = nil
		default:
			sc.mu.Unlock()
			return "", fmt.Errorf("previous screencast is still stopping")
		}
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

	ffmpegCmd := screencastFFmpegCommand(inputFormat, outputPath)
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
		if err := b.screencastStopInternal(); err != nil {
			log.Printf("screencast: auto-stop failed: %v", err)
		}
	})
	sc.mu.Unlock()

	return sessionID, nil
}

// screencastFFmpegCommand encodes CDP's JPEG or PNG frames as H.264 MP4.
func screencastFFmpegCommand(inputFormat, outputPath string) *exec.Cmd {
	return exec.Command(
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-f", inputFormat,
		"-framerate", "4",
		"-i", "pipe:0",
		// yuv420p requires even dimensions. Padding preserves every source
		// pixel (unlike cropping). FFmpeg's default autoscale keeps the
		// output size fixed if the viewport changes mid-recording.
		"-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2:0:0",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "fast",
		"-abort_on", "empty_output",
		"-movflags", "+faststart",
		outputPath,
	)
}

// claimStopLocked assigns one caller the teardown. Caller must hold sc.mu.
func (sc *screencastState) claimStopLocked() *screencastStopResources {
	result := &screencastStopResult{done: make(chan struct{})}
	sc.stop = result
	sc.active = false
	if sc.stopTimer != nil {
		sc.stopTimer.Stop()
		sc.stopTimer = nil
	}
	resources := &screencastStopResources{
		result: result, stopCh: sc.stopCh, stopped: sc.stopped,
		ffmpegIn: sc.ffmpegIn, ffmpegCmd: sc.ffmpegCmd, outputPath: sc.outputPath,
		timeout: screencastStopTimeout,
	}
	sc.stopCh = nil
	sc.ffmpegIn = nil
	return resources
}

// screencastStopInternal stops the screencast, or waits for an in-progress
// stop to finish. Safe to call from any goroutine.
func (b *BrowseTools) screencastStopInternal() error {
	sc := &b.screencast
	sc.mu.Lock()
	var resources *screencastStopResources
	if sc.active {
		resources = sc.claimStopLocked()
	}
	result := sc.stop
	sc.mu.Unlock()
	if resources != nil {
		return b.finishScreencastStop(resources)
	}
	if result != nil {
		<-result.done
		return result.err
	}
	return nil
}

func (b *BrowseTools) finishScreencastStop(resources *screencastStopResources) (err error) {
	defer func() {
		resources.result.err = err
		close(resources.result.done)
	}()
	// A stalled encoder can block a frame writer forever. Killing this
	// recording's ffmpeg process closes the pipe and makes stop report an
	// error instead of hanging the tool (or browser shutdown).
	timer := time.AfterFunc(resources.timeout, func() {
		if resources.ffmpegCmd != nil && resources.ffmpegCmd.Process != nil {
			if err := resources.ffmpegCmd.Process.Kill(); err == nil {
				log.Printf("screencast: ffmpeg did not finish within %v; killed encoder", resources.timeout)
			}
		}
	})
	defer timer.Stop()

	// Signal the ack goroutine to stop.
	if resources.stopCh != nil {
		close(resources.stopCh)
	}
	if resources.stopped != nil {
		<-resources.stopped
	}

	// Finish any frame writes claimed before active was cleared, then
	// signal EOF and wait for the MP4 to be finalized.
	b.screencast.writers.Wait()
	if resources.ffmpegIn != nil {
		resources.ffmpegIn.Close()
	}
	if resources.ffmpegCmd != nil {
		if err := resources.ffmpegCmd.Wait(); err != nil {
			if lb, ok := resources.ffmpegCmd.Stderr.(*limitedBuffer); ok {
				return fmt.Errorf("ffmpeg failed: %w: %s", err, lb.String())
			}
			return fmt.Errorf("ffmpeg failed: %w", err)
		}
	}
	info, err := os.Stat(resources.outputPath)
	if err != nil {
		return fmt.Errorf("screencast output %s: %w", resources.outputPath, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("screencast output %s is not a nonempty regular file", resources.outputPath)
	}
	return nil
}

// screencastStop stops the screencast and returns summary info.
func (b *BrowseTools) screencastStop() (sessionID, outputPath string, frameCount int, duration time.Duration, err error) {
	sc := &b.screencast
	sc.mu.Lock()
	if !sc.active {
		sc.mu.Unlock()
		return "", "", 0, 0, fmt.Errorf("no active screencast — call screencast_start first")
	}
	sessionID = sc.sessionID
	outputPath = sc.outputPath
	frameCount = sc.frameCount
	duration = time.Since(sc.startTime)
	resources := sc.claimStopLocked()
	sc.mu.Unlock()

	if err := b.finishScreencastStop(resources); err != nil {
		return sessionID, outputPath, frameCount, duration, fmt.Errorf("screencast %s failed after %d frames (MP4 at %s): %w", sessionID, frameCount, outputPath, err)
	}
	return sessionID, outputPath, frameCount, duration, nil
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

// limitedBuffer keeps the end of ffmpeg's stderr, where the failure is reported.
type limitedBuffer struct {
	buf []byte
	max int
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= lb.max {
		lb.buf = append(lb.buf[:0], p[n-lb.max:]...)
	} else {
		if drop := len(lb.buf) + n - lb.max; drop > 0 {
			lb.buf = lb.buf[drop:]
		}
		lb.buf = append(lb.buf, p...)
	}
	// Always report full length consumed so ffmpeg doesn't get write errors.
	return n, nil
}

func (lb *limitedBuffer) String() string {
	return string(lb.buf)
}
