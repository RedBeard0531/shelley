// Pure-function tests for the voice transcription capture pipeline:
// resampling to 16 kHz and PCM16 encoding. The websocket/microphone parts
// are not unit-testable in node, so they stay in stt.ts.
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import { encodePCM16, resampleLinear, STT_SAMPLE_RATE } from "./stt";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}
function assertClose(actual: number, expected: number, tol: number, msg: string): void {
  if (Math.abs(actual - expected) > tol) {
    throw new Error(`Assertion failed: ${msg} — got ${actual}, want ~${expected}`);
  }
}
async function run(name: string, fn: () => void | Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

async function main(): Promise<void> {
  await run("resampleLinear at equal rates returns the input unchanged", () => {
    const in_ = new Float32Array([0, 0.5, -0.5, 1]);
    assert(resampleLinear(in_, STT_SAMPLE_RATE, STT_SAMPLE_RATE) === in_, "same object back");
  });

  await run("resampleLinear upsamples to double length with interpolation", () => {
    const out = resampleLinear(new Float32Array([0, 1]), 8000, 16000);
    assert(out.length === 4, "length doubles (2 × ratio 2)");
    assertClose(out[0], 0, 1e-5, "first sample");
    assertClose(out[1], 0.5, 1e-4, "lerp midpoint");
    assertClose(out[2], 1, 1e-4, "second sample");
    assertClose(out[3], 1, 1e-4, "last sample clamps to the final input");
  });

  await run("resampleLinear downsamples, interpolating between endpoints", () => {
    // 0, 0.5, 1, 0.5 at ratio 0.5: out[0] = in[0] = 0; out[1] = in[2] = 1
    const out = resampleLinear(new Float32Array([0, 0.5, 1, 0.5]), 16000, 8000);
    assert(out.length === 2, "length halves");
    assertClose(out[0], 0, 1e-5, "first sample");
    assertClose(out[1], 1, 1e-4, "second sample");
  });

  await run("resampleLinear survives a single-sample input", () => {
    const out = resampleLinear(new Float32Array([0.25]), 48000, 16000);
    assert(out.length === 1, "one sample out");
    assertClose(out[0], 0.25, 1e-5, "value preserved");
  });

  await run("encodePCM16 clamps and scales to int16 little-endian", () => {
    const buf = encodePCM16(new Float32Array([1, -1, 0.5, -0.5, 0]));
    const view = new DataView(buf);
    assert(view.getInt16(0, true) === 32767, "positive max clamped");
    assert(view.getInt16(2, true) === -32768, "negative max clamped");
    assert(view.getInt16(4, true) === 16383, "0.5 scales to 16383 (0.5 × 32767, truncated)");
    assert(view.getInt16(6, true) === -16384, "-0.5 scales to -16384");
    assert(view.getInt16(8, true) === 0, "silence is zero");
  });

  await run("encodePCM16 writes little-endian byte order", () => {
    const bytes = new Uint8Array(encodePCM16(new Float32Array([1])));
    assert(bytes[0] === 0xff, "low byte");
    assert(bytes[1] === 0x7f, "high byte");
  });

  console.log("\nstt tests passed");
}

await main();
