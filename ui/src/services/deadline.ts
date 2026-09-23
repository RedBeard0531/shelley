// deadline.ts — bounded waits for the IndexedDB message cache.
//
// The cache layer awaits several APIs that have no timeout of their own and,
// in a multi-tab browser, can genuinely wait forever:
//
//   * fetch() for /api/cache-key. On HTTP/1.1 every open tab pins one of the
//     six per-origin sockets with its SSE stream, so a later tab's request is
//     never dispatched — no error, no response, just silence.
//   * indexedDB.open(). If another tab still holds a connection at a lower
//     version, the open fires `blocked` and then waits for that tab to close
//     it, which a tab left open across a deploy never does.
//   * navigator.locks.request(). Waits indefinitely for the grant, so a
//     frozen tab holding the lock blocks every sibling.
//
// Each of those sits between "user focuses a conversation" and "the spinner
// clears", so an unbounded wait there is an unbounded spinner. The cache is
// only ever an optimization, so the right response to a slow cache is to give
// up and use the network — never to wait.

/** Thrown when a bounded wait misses its deadline. */
export class DeadlineExceededError extends Error {
  constructor(what: string, ms: number) {
    super(`${what} exceeded its ${ms}ms deadline`);
    this.name = "DeadlineExceededError";
  }
}

export interface DeadlineOptions<T> {
  /** Label used in the error message, e.g. "indexedDB.open". */
  what: string;
  /**
   * Called with a value that arrives *after* we gave up. The underlying
   * operation can't be cancelled (neither IDB opens nor a fetch queued behind
   * a lock), so late results are real and must be disposed of deliberately:
   * close the late DB connection, discard the superseded key.
   */
  onLate?: (value: T) => void;
  /**
   * Called with an error that arrives after we gave up. Never omit this
   * silently — swallowing it recreates the failure this module exists to
   * kill, a cache that quietly stopped working with nothing to say why.
   */
  onLateError?: (err: unknown) => void;
}

/**
 * In-flight bounded waits, for diagnosing a hang WHILE it is hanging.
 *
 * cacheDiag records a decision once it completes, which is exactly the wrong
 * time for this class of bug: a tab stuck on the spinner has, by definition,
 * completed nothing, so its stats() is empty. That silence is what made the
 * original multi-tab hang so hard to place. Registering a wait for its
 * duration means the question "what is this tab waiting on?" has an answer at
 * the moment you need it, from a console, on someone else's machine.
 */
const inFlight = new Set<WaitRecord>();

export interface PendingWait {
  /** Unique per wait; several waits can share a label (two hydrates in flight). */
  id: number;
  /** Operation label, e.g. "indexedDB.open". */
  what: string;
  /** Deadline it was given, in ms. */
  deadlineMs: number;
  /** How long it has been waiting so far, in ms. */
  elapsedMs: number;
}

interface WaitRecord {
  id: number;
  what: string;
  deadlineMs: number;
  startedAt: number;
}

let nextWaitId = 1;

/** Snapshot of the waits currently outstanding, longest-waiting first. */
export function pendingWaits(): PendingWait[] {
  const now = Date.now();
  return [...inFlight]
    .map((w) => ({
      id: w.id,
      what: w.what,
      deadlineMs: w.deadlineMs,
      elapsedMs: now - w.startedAt,
    }))
    .sort((a, b) => b.elapsedMs - a.elapsedMs);
}

/**
 * Waits that missed their deadline, and what became of them.
 *
 * pendingWaits answers "what is this tab stuck on right now"; this answers
 * "what has it given up on", which is the durable trace of lock contention.
 * The cache is never *broken* in that scenario, only slow, so the interesting
 * field is the outcome: an operation that completed late was healthy and was
 * merely waiting on someone else's lock. In production Safari that someone
 * was a throttled background tab holding IndexedDB, and the only visible
 * symptom was conversations quietly loading from the network. The perf HUD
 * turns this record into an alert so the next such regression is seen.
 */
export interface MissedDeadline {
  /** The PendingWait.id this was while in flight. */
  id: number;
  /** Operation label, e.g. "indexedDB.open". */
  what: string;
  /** Deadline it was given, in ms. */
  deadlineMs: number;
  /** Date.now() when the caller gave up. */
  at: number;
  /**
   * pending   — the operation has not come back yet.
   * completed — it succeeded after we stopped waiting: slow, not broken.
   * failed    — it errored after we stopped waiting.
   * abandoned — the caller cancelled it itself after giving up (AbortError).
   */
  outcome: "pending" | "completed" | "failed" | "abandoned";
  /** Time from the start of the wait until the operation settled. */
  settledAfterMs?: number;
  /** The late error, when outcome is "failed". */
  error?: string;
}

export const MISSED_DEADLINE_BUFFER = 20;
const missed: MissedDeadline[] = [];

/** Missed deadlines, oldest first (last MISSED_DEADLINE_BUFFER). */
export function missedDeadlines(): MissedDeadline[] {
  return missed.map((m) => ({ ...m }));
}

export function resetMissedDeadlines(): void {
  missed.length = 0;
}

/**
 * Every bounded IndexedDB wait labels itself "indexedDB.…" (see messageStore),
 * which is how observers tell cache lock contention from a stalled fetch or
 * lock request.
 */
export function isIndexedDBWait(what: string): boolean {
  return what.startsWith("indexedDB.");
}

function recordMiss(entry: WaitRecord): MissedDeadline {
  const rec: MissedDeadline = {
    id: entry.id,
    what: entry.what,
    deadlineMs: entry.deadlineMs,
    at: Date.now(),
    outcome: "pending",
  };
  missed.push(rec);
  if (missed.length > MISSED_DEADLINE_BUFFER) missed.shift();
  return rec;
}

/** True for the DOMException a caller raises by cancelling its own request
 * (tx.abort(), AbortController) — a decision, not a failure. */
export function isAbortError(err: unknown): boolean {
  return typeof err === "object" && err !== null && "name" in err && err.name === "AbortError";
}

/**
 * Await `p`, rejecting with DeadlineExceededError after `ms`.
 *
 * `p` keeps running; see DeadlineOptions.onLate.
 */
export function withDeadline<T>(p: Promise<T>, ms: number, opts: DeadlineOptions<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    let settled = false;
    const entry: WaitRecord = {
      id: nextWaitId++,
      what: opts.what,
      deadlineMs: ms,
      startedAt: Date.now(),
    };
    inFlight.add(entry);
    // Every exit path must deregister, or the diagnostic becomes a liar that
    // reports phantom waits outliving the code that started them.
    const done = () => {
      settled = true;
      inFlight.delete(entry);
    };
    // Set once the deadline fires; the late handlers fill in its outcome.
    let miss: MissedDeadline | undefined;
    const timer = setTimeout(() => {
      if (settled) return;
      done();
      miss = recordMiss(entry);
      reject(new DeadlineExceededError(opts.what, ms));
    }, ms);
    p.then(
      (v) => {
        if (settled) {
          if (miss) {
            miss.outcome = "completed";
            miss.settledAfterMs = Date.now() - entry.startedAt;
          }
          // Guarded: this runs in a promise chain nobody awaits, so a throwing
          // callback would surface as an unhandled rejection.
          try {
            opts.onLate?.(v);
          } catch (err) {
            console.warn("deadline: onLate threw", err);
          }
          return;
        }
        done();
        clearTimeout(timer);
        resolve(v);
      },
      (err) => {
        if (settled) {
          if (miss) {
            miss.settledAfterMs = Date.now() - entry.startedAt;
            if (isAbortError(err)) {
              miss.outcome = "abandoned";
            } else {
              miss.outcome = "failed";
              miss.error = String(err);
            }
          }
          try {
            opts.onLateError?.(err);
          } catch (e) {
            console.warn("deadline: onLateError threw", e);
          }
          return;
        }
        done();
        clearTimeout(timer);
        reject(err);
      },
    );
  });
}

/** True when `err` came from a missed deadline rather than a real failure. */
export function isDeadlineExceeded(err: unknown): boolean {
  return err instanceof DeadlineExceededError;
}
