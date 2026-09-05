package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestScreencastStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Verify no screencast is running.
	active, _, _, _ := tools.screencastStatus()
	if active {
		t.Fatal("expected no active screencast")
	}

	// Start via combined tool.
	tool := tools.CombinedTool()
	out := tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "Screencast recording") {
		if strings.Contains(text, "failed to start browser") || strings.Contains(text, "ffmpeg") {
			t.Skip("Browser or ffmpeg not available")
		}
		t.Fatalf("unexpected start result: %s", text)
	}
	if !strings.Contains(text, ".mp4") {
		t.Fatalf("expected .mp4 in start message, got: %s", text)
	}
	t.Logf("Start result: %s", text)

	// Double-start should error.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "already active") {
		t.Fatalf("expected already-active error, got: %s", text)
	}

	// Navigate to generate some frames.
	out = tool.Run(ctx, json.RawMessage(`{"action":"navigate","url":"data:text/html,<h1>Screencast Test</h1>"}`))
	text = contentText(t, out)
	if strings.Contains(text, "Error") {
		t.Fatalf("navigate failed: %s", text)
	}

	// Poll until we have at least one frame.
	var sessionID string
	var frameCount int
	var elapsed time.Duration
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var active bool
		active, sessionID, frameCount, elapsed = tools.screencastStatus()
		if active && frameCount > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if frameCount == 0 {
		t.Fatal("expected at least one screencast frame")
	}
	t.Logf("Status: session=%s frames=%d elapsed=%v", sessionID, frameCount, elapsed)

	// Stop via combined tool.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "Screencast stopped") {
		t.Fatalf("unexpected stop result: %s", text)
	}
	if !strings.Contains(text, ".mp4") {
		t.Fatalf("expected .mp4 path in stop message, got: %s", text)
	}
	t.Logf("Stop result: %s", text)

	// Verify MP4 file exists on disk.
	mp4Path := ScreencastDir + "/" + sessionID + ".mp4"
	info, err := os.Stat(mp4Path)
	if err != nil {
		t.Fatalf("MP4 file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("MP4 file is empty")
	}
	t.Logf("MP4 file: %s (%d bytes)", mp4Path, info.Size())

	// Double-stop should error.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "no active screencast") {
		t.Fatalf("expected no-active error, got: %s", text)
	}

	// Status should show inactive.
	active, _, _, _ = tools.screencastStatus()
	if active {
		t.Fatal("expected no active screencast after stop")
	}
}

func TestScreencastLimitsAreReasonable(t *testing.T) {
	if ScreencastMaxFrames < 1000 {
		t.Fatalf("ScreencastMaxFrames too low: %d", ScreencastMaxFrames)
	}
	if ScreencastMaxDuration < 10*time.Minute {
		t.Fatalf("ScreencastMaxDuration too low: %v", ScreencastMaxDuration)
	}
}

func TestScreencastStatusWhenInactive(t *testing.T) {
	ctx := t.Context()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	out := tool.Run(ctx, json.RawMessage(`{"action":"screencast_status"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "No active screencast") {
		t.Fatalf("expected no-active message, got: %s", text)
	}
}

func TestScreencastSchemaIncludes(t *testing.T) {
	tools := NewBrowseTools(t.Context(), 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}

	for _, action := range []string{"screencast_start", "screencast_stop", "screencast_status"} {
		found := false
		for _, a := range schema.Properties["action"].Enum {
			if a == action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("action %q not in schema enum", action)
		}
	}

	for _, prop := range []string{"format", "quality", "max_width", "max_height", "every_nth_frame"} {
		if _, ok := schema.Properties[prop]; !ok {
			t.Errorf("expected property %q in schema", prop)
		}
	}
}

// TestScreencastOddViewportEncodes is a regression test: an odd-sized
// viewport (e.g. 881x495) used to kill ffmpeg on the first frame ("height
// not divisible by 2"), producing a 0-byte MP4. The crop filter now trims to
// even dimensions (at most a 1px centered crop, no rescaling) so recording
// succeeds with even output dimensions.
func TestScreencastOddViewportEncodes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	out := tool.Run(ctx, json.RawMessage(`{"action":"resize","width":881,"height":495}`))
	if text := contentText(t, out); strings.Contains(text, "Error") {
		t.Fatalf("resize failed: %s", text)
	}
	out = tool.Run(ctx, json.RawMessage(`{"action":"navigate","url":"data:text/html,<h1>Odd Viewport Test</h1>"}`))
	if text := contentText(t, out); strings.Contains(text, "Error") {
		t.Fatalf("navigate failed: %s", text)
	}

	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "Screencast recording") {
		if strings.Contains(text, "failed to start browser") || strings.Contains(text, "ffmpeg") {
			t.Skip("Browser or ffmpeg not available")
		}
		t.Fatalf("unexpected start result: %s", text)
	}

	// Poll until at least one frame is captured, like TestScreencastStartStop.
	var frameCount int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var active bool
		if active, _, frameCount, _ = tools.screencastStatus(); !active {
			t.Fatal("screencast stopped before any frames were captured")
		}
		if frameCount > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if frameCount == 0 {
		t.Fatal("expected at least one screencast frame")
	}

	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "Screencast stopped") {
		t.Fatalf("unexpected stop result: %s", text)
	}
	m := regexp.MustCompile(`/tmp/shelley-screencasts/([0-9a-f]+)\.mp4`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no MP4 path in stop result: %s", text)
	}
	info, err := os.Stat(m[0])
	if err != nil {
		t.Fatalf("MP4 file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("MP4 file is empty (odd-dimension regression)")
	}
	t.Logf("MP4 file: %s (%d bytes)", m[0], info.Size())

	// The encoded video must have even dimensions.
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
	probe := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", m[0])
	outBytes, err := probe.Output()
	if err != nil {
		t.Fatalf("ffprobe failed: %v", err)
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(outBytes)), "%d,%d", &w, &h); err != nil {
		t.Fatalf("unexpected ffprobe output %q: %v", string(outBytes), err)
	}
	if w%2 != 0 || h%2 != 0 {
		t.Fatalf("encoded dimensions not even: %dx%d", w, h)
	}
	t.Logf("Encoded dimensions: %dx%d", w, h)
}

// startScreencastSession starts a screencast and returns the session ID.
func startScreencastSession(t *testing.T, ctx context.Context, tool *llm.Tool) string {
	t.Helper()
	out := tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "Screencast recording") {
		if strings.Contains(text, "failed to start browser") || strings.Contains(text, "ffmpeg") {
			t.Skip("Browser or ffmpeg not available")
		}
		t.Fatalf("unexpected start result: %s", text)
	}
	m := regexp.MustCompile(`/tmp/shelley-screencasts/([0-9a-f]+)\.mp4`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no session path in start result: %s", text)
	}
	return m[1]
}

// waitForScreencastFrames polls until at least minFrames frames are captured.
func waitForScreencastFrames(t *testing.T, tools *BrowseTools, minFrames int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, frames, _ := tools.screencastStatus(); frames >= minFrames {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected at least %d screencast frames", minFrames)
}

// TestScreencastKillReportedAsFailure verifies that SIGKILLing ffmpeg
// mid-recording (an unrecoverable encoder death) makes screencast_stop report
// an error instead of silently claiming the MP4 was saved.
func TestScreencastKillReportedAsFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})
	tool := tools.CombinedTool()

	sessionID := startScreencastSession(t, ctx, tool)
	t.Logf("Session: %s", sessionID)

	// Navigate to a page that repaints continuously so frames keep flowing.
	animated := `data:text/html,<h1 id=t></h1><script>setInterval(()=>{document.getElementById('t').textContent=Date.now()},100)</script>`
	out := tool.Run(ctx, json.RawMessage(`{"action":"navigate","url":"`+animated+`"}`))
	if text := contentText(t, out); strings.Contains(text, "Error") {
		t.Fatalf("navigate failed: %s", text)
	}
	waitForScreencastFrames(t, tools, 3)

	// Kill ffmpeg mid-recording with SIGKILL: no chance to finalize the MP4.
	// Its command line contains the output path, which embeds the session ID,
	// so this only matches the right process. Note SIGTERM is NOT used here:
	// ffmpeg finalizes the MP4 on SIGTERM (see the test below).
	if err := exec.Command("pkill", "-9", "-f", sessionID+".mp4").Run(); err != nil {
		t.Fatalf("failed to kill ffmpeg: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Stop must report the encoder failure, not success.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "recording failed") {
		t.Fatalf("expected screencast_stop to report the encoder failure, got: %s", text)
	}
	t.Logf("Stop result: %s", text)
}

// TestScreencastSigtermYieldsPlayableMP4 verifies that a SIGTERM'd ffmpeg —
// which finalizes the MP4 and exits non-zero — is reported as a successful
// recording with a caveat note, not as a failure.
func TestScreencastSigtermYieldsPlayableMP4(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})
	tool := tools.CombinedTool()

	sessionID := startScreencastSession(t, ctx, tool)
	t.Logf("Session: %s", sessionID)

	// Navigate to a page that repaints continuously so frames keep flowing.
	animated := `data:text/html,<h1 id=t></h1><script>setInterval(()=>{document.getElementById('t').textContent=Date.now()},100)</script>`
	out := tool.Run(ctx, json.RawMessage(`{"action":"navigate","url":"`+animated+`"}`))
	if text := contentText(t, out); strings.Contains(text, "Error") {
		t.Fatalf("navigate failed: %s", text)
	}
	waitForScreencastFrames(t, tools, 3)
	// Let ffmpeg actually process frames and write the MP4 header before
	// signaling, so the finalize path has data to work with.
	time.Sleep(2 * time.Second)

	// SIGTERM: ffmpeg finalizes the MP4 (writes moov, honors +faststart) and
	// exits with a non-zero status.
	if err := exec.Command("pkill", "-f", sessionID+".mp4").Run(); err != nil {
		t.Fatalf("failed to signal ffmpeg: %v", err)
	}
	time.Sleep(time.Second)

	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "Screencast stopped") {
		t.Fatalf("expected SIGTERM'd recording to be reported as stopped (playable), got: %s", text)
	}
	if !strings.Contains(text, "verified playable") {
		t.Fatalf("expected a caveat note about the non-zero ffmpeg exit, got: %s", text)
	}
	t.Logf("Stop result: %s", text)
}

// contentText extracts the text from a tool output, including errors.
func contentText(t *testing.T, out llm.ToolOut) string {
	t.Helper()
	if out.Error != nil {
		return out.Error.Error()
	}
	var parts []string
	for _, c := range out.LLMContent {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}
