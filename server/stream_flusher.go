package server

import (
	"sync"
	"time"

	"shelley.exe.dev/llm"
)

// streamFlusher batches LLM stream deltas and flushes them periodically.
// Anthropic's SSE stream emits hundreds of tiny text_delta/thinking_delta
// events per second. Broadcasting each one individually overwhelms the bounded
// subpub queue, causing subscriber disconnections (and, on the unified
// /api/stream2 endpoint, forced SSE reconnect loops that make the UI look
// frozen while the agent keeps working). Instead, we accumulate deltas and
// flush the combined text every interval (e.g., 50ms), yielding ~20
// updates/second regardless of provider chattiness.
type streamFlusher struct {
	cm       *ConversationManager
	interval time.Duration

	mu      sync.Mutex
	buf     string // accumulated delta text since last flush
	typ     string // kind of accumulated deltas: "text" or "thinking"
	index   int    // content block index of accumulated text
	timer   *time.Timer
	running bool
}

// nextSeq returns the next monotonically increasing sequence number. The
// counter lives on the ConversationManager so it survives loop resets and is
// truly per-conversation. Safe to call without holding sf.mu.
func (sf *streamFlusher) nextSeq() int64 {
	return sf.cm.streamDeltaSeq.Add(1)
}

func newStreamFlusher(cm *ConversationManager, interval time.Duration) *streamFlusher {
	return &streamFlusher{
		cm:       cm,
		interval: interval,
	}
}

// Push adds a stream delta to the buffer and schedules a flush.
//
// Both "text" and "thinking" deltas are batched: reasoning models emit
// thinking deltas at the same token-by-token rate as text, and passing them
// through unbatched used to flood the bounded subscriber queues (the exact
// stampede batching exists to prevent). A delta of a different kind or block
// index flushes the previous buffer first so ordering is preserved.
func (sf *streamFlusher) Push(delta llm.StreamDelta) {
	var out []llm.StreamDelta
	sf.mu.Lock()
	switch delta.Type {
	case "text", "thinking":
		if sf.buf != "" && (sf.typ != delta.Type || sf.index != delta.Index) {
			out = append(out, sf.takeLocked())
		}
		sf.buf += delta.Text
		sf.typ = delta.Type
		sf.index = delta.Index
		if !sf.running {
			sf.running = true
			sf.timer = time.AfterFunc(sf.interval, sf.flush)
		}
	default:
		// Unknown delta kinds pass through immediately; emit anything
		// buffered first so relative order is preserved.
		if sf.buf != "" {
			out = append(out, sf.takeLocked())
		}
		delta.Seq = sf.nextSeq()
		out = append(out, delta)
	}
	sf.mu.Unlock()

	for i := range out {
		sf.cm.broadcastStream(StreamResponse{StreamDelta: &out[i]})
	}
}

// takeLocked drains the buffer into a broadcast-ready delta, assigning its
// seq while sf.mu is held so seq order matches accumulation order. The caller
// must hold sf.mu and broadcast the result after releasing it; the window
// between seq assignment and broadcast means a concurrent emitter can, in
// principle, put seq N+1 on the wire before N. That reordering window is
// pre-existing (the old flush had the same shape), providers deliver deltas
// single-threaded from inside Do, and Seq exists precisely so clients can
// detect it — accepted.
func (sf *streamFlusher) takeLocked() llm.StreamDelta {
	d := llm.StreamDelta{
		Type:  sf.typ,
		Text:  sf.buf,
		Index: sf.index,
		Seq:   sf.nextSeq(),
	}
	sf.buf = ""
	return d
}

func (sf *streamFlusher) flush() {
	sf.mu.Lock()
	var out *llm.StreamDelta
	if sf.buf != "" {
		d := sf.takeLocked()
		out = &d
	}
	sf.running = false
	if sf.timer != nil {
		sf.timer.Stop()
		sf.timer = nil
	}
	sf.mu.Unlock()

	if out != nil {
		sf.cm.broadcastStream(StreamResponse{StreamDelta: out})
	}
}

// Flush forces any buffered text to be broadcast immediately.
// Call this before recording the final assistant message to ensure
// deltas reach the UI before the full message replaces them.
func (sf *streamFlusher) Flush() {
	sf.flush()
}
