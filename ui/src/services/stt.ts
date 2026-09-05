// Server-side streaming voice transcription.
//
// The mic button no longer depends on the browser's Web Speech API (which
// Firefox, mobile or desktop, has never shipped). Instead the browser
// captures raw microphone audio with Web Audio (supported everywhere,
// including mobile Firefox and iOS Safari), downsamples it to 16 kHz mono,
// and streams PCM16 frames over a websocket to the server, where sherpa-onnx
// transcribes them in real time. Partial results stream back while talking;
// the server finalizes the dangling utterance when the user stops.

export const STT_SAMPLE_RATE = 16000;

export interface STTInfo {
  available: boolean;
  reason?: string;
}

let infoPromise: Promise<STTInfo> | null = null;

/** Whether the server has transcription enabled. Cached across calls. */
export function getSTTInfo(): Promise<STTInfo> {
  infoPromise ??= fetch("/api/stt")
    .then((r) => r.json() as Promise<STTInfo>)
    .catch(() => ({ available: false, reason: "Voice transcription endpoint unreachable" }));
  return infoPromise;
}

export interface TranscriptionCallbacks {
  /** A live (non-final) rendering of the current utterance. Replaces the tail. */
  onPartial(text: string): void;
  /** A finalized utterance. Appends to the committed dictation. */
  onFinal(text: string): void;
  onError(message: string): void;
}

/**
 * Start dictation: opens the microphone, streams PCM16 to the server, and
 * returns a stop function. Exactly one session may be active at a time.
 */
export function startTranscription(cbs: TranscriptionCallbacks): () => void {
  let stopped = false;
  let active = false;
  let ws: WebSocket | null = null;
  let audioCtx: AudioContext | null = null;
  let sourceNode: MediaStreamAudioSourceNode | null = null;
  let processor: ScriptProcessorNode | null = null;
  let stream: MediaStream | null = null;

  function cleanupDevices() {
    if (processor) {
      processor.onaudioprocess = null;
      try {
        processor.disconnect();
      } catch {
        /* already torn down */
      }
      processor = null;
    }
    if (sourceNode) {
      try {
        sourceNode.disconnect();
      } catch {
        /* already torn down */
      }
      sourceNode = null;
    }
    if (stream) {
      for (const track of stream.getTracks()) track.stop();
      stream = null;
    }
    if (audioCtx) {
      audioCtx.close().catch(() => {});
      audioCtx = null;
    }
  }

  function fail(message: string) {
    if (stopped) return;
    stopped = true;
    cleanupDevices();
    cbs.onError(message);
  }

  const fromRate = (r: number): number | null => {
    // Resample to 16 kHz with linear interpolation. Most browsers honor the
    // requested sampleRate; those that don't (old Safari) get a downmix here.
    return r === STT_SAMPLE_RATE ? null : r;
  };

  async function start() {
    let mic: MediaStream;
    try {
      mic = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      });
    } catch (err) {
      fail(
        err instanceof DOMException && err.name === "NotAllowedError"
          ? "Microphone access was denied"
          : "No microphone found",
      );
      return;
    }
    stream = mic;

    const AC =
      window.AudioContext ??
      (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!AC) {
      fail("Audio capture is not supported in this browser");
      return;
    }
    try {
      audioCtx = new AC({ sampleRate: STT_SAMPLE_RATE });
    } catch {
      try {
        audioCtx = new AC();
      } catch {
        fail("Audio capture is not supported in this browser");
        return;
      }
    }
    const ctx = audioCtx;

    ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/stt/ws`);
    ws.binaryType = "arraybuffer";
    let opened = false;
    ws.onopen = () => {
      opened = true;
      try {
        sourceNode = ctx.createMediaStreamSource(mic);
        processor = ctx.createScriptProcessor(4096, 1, 1);
        sourceNode.connect(processor);
        processor.connect(ctx.destination); // muted sink; keeps the graph alive
        active = true;
        processor.onaudioprocess = (e) => {
          if (!active || !ws || ws.readyState !== WebSocket.OPEN || stopped) return;
          const input = e.inputBuffer.getChannelData(0);
          const rate = fromRate(ctx.sampleRate);
          const samples = rate === null ? input : resampleLinear(input, rate, STT_SAMPLE_RATE);
          ws.send(encodePCM16(samples));
        };
      } catch {
        fail("Could not start audio capture");
      }
    };
    ws.onmessage = (e) => {
      if (stopped || typeof e.data !== "string") return;
      let msg: { partial?: string; final?: string };
      try {
        msg = JSON.parse(e.data);
      } catch {
        return;
      }
      if (msg.partial !== undefined && msg.partial !== "") cbs.onPartial(msg.partial);
      if (msg.final !== undefined && msg.final !== "") cbs.onFinal(msg.final);
    };
    ws.onerror = () => fail("Voice connection failed");
    ws.onclose = () => {
      if (opened && !stopped) fail("Voice connection closed unexpectedly");
    };
  }
  void start();

  return () => {
    if (stopped) return;
    stopped = true;
    // Ask the server to flush the dangling utterance as a final; the server
    // closes the websocket after sending it, so onFinal still lands.
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ stop: true }));
    }
    cleanupDevices();
  };
}

/** Linear-interpolation resampler for Float32 audio. */
export function resampleLinear(input: Float32Array, fromRate: number, toRate: number): Float32Array {
  if (fromRate === toRate) return input;
  const ratio = toRate / fromRate;
  const outLen = Math.max(1, Math.floor(input.length * ratio));
  const out = new Float32Array(outLen);
  for (let i = 0; i < outLen; i++) {
    const pos = i / ratio;
    const i0 = Math.floor(pos);
    const i1 = Math.min(i0 + 1, input.length - 1);
    const frac = pos - i0;
    out[i] = input[i0] + (input[i1] - input[i0]) * frac;
  }
  return out;
}

/** Convert float samples in [-1, 1] to little-endian PCM16 bytes. */
export function encodePCM16(samples: Float32Array): ArrayBuffer {
  const buf = new ArrayBuffer(samples.length * 2);
  const view = new DataView(buf);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    view.setInt16(i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return buf;
}
