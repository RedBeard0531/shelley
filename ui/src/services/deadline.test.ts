// deadline tests — the contract the cache layer relies on to stay live.
//
// The point of withDeadline is that the caller always gets an answer even
// though the underlying operation cannot be cancelled. That makes what happens
// to a LATE result the interesting part: an IndexedDB connection that arrives
// after we stopped waiting must be closed (an abandoned connection at the
// current version would itself block the next version bump), and a late error
// must still be reported (a cache that silently stops working with nothing in
// the console is the failure mode this whole area exists to prevent).
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import {
  withDeadline,
  isDeadlineExceeded,
  DeadlineExceededError,
  pendingWaits,
  missedDeadlines,
  resetMissedDeadlines,
  isIndexedDBWait,
  MISSED_DEADLINE_BUFFER,
} from "./deadline";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}
async function run(name: string, fn: () => Promise<void>): Promise<void> {
  // Every case that misses a deadline leaves a record; start each one clean.
  resetMissedDeadlines();
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function main(): Promise<void> {
  await run("resolves normally when the operation beats the deadline", async () => {
    const got = await withDeadline(Promise.resolve("ok"), 1000, { what: "test" });
    assert(got === "ok", "value passes through");
  });

  await run("rejects with DeadlineExceededError when it doesn't", async () => {
    let err: unknown;
    try {
      await withDeadline(new Promise(() => {}), 20, { what: "stalled-op" });
    } catch (e) {
      err = e;
    }
    assert(err instanceof DeadlineExceededError, "throws the typed error");
    assert(isDeadlineExceeded(err), "and the predicate recognizes it");
    assert(String(err).includes("stalled-op"), `error names the operation: ${String(err)}`);
  });

  await run("a real failure is reported as itself, not as a timeout", async () => {
    // Callers branch on isDeadlineExceeded to decide whether to back off, so
    // conflating the two would apply a cooldown to errors that should retry.
    let err: unknown;
    try {
      await withDeadline(Promise.reject(new Error("quota")), 1000, { what: "test" });
    } catch (e) {
      err = e;
    }
    assert(!isDeadlineExceeded(err), "not a deadline error");
    assert(String(err).includes("quota"), "the original error survives");
  });

  await run("a value that lands after the deadline is handed to onLate", async () => {
    let late: string | null = null;
    let resolveIt!: (v: string) => void;
    const p = new Promise<string>((r) => {
      resolveIt = r;
    });
    await withDeadline(p, 20, { what: "test", onLate: (v) => (late = v) }).catch(() => {});
    assert(late === null, "not called before the value arrives");
    resolveIt("arrived");
    await sleep(0);
    assert(late === "arrived", "the late value is handed over for disposal");
  });

  await run("an error that lands after the deadline is handed to onLateError", async () => {
    let seen: unknown;
    let rejectIt!: (e: unknown) => void;
    const p = new Promise<string>((_r, rej) => {
      rejectIt = rej;
    });
    await withDeadline(p, 20, { what: "test", onLateError: (e) => (seen = e) }).catch(() => {});
    rejectIt(new Error("late boom"));
    await sleep(0);
    assert(String(seen).includes("late boom"), "the late error is surfaced, not swallowed");
  });

  await run("onLate is not called when the operation wins", async () => {
    let called = false;
    const got = await withDeadline(Promise.resolve(7), 1000, {
      what: "test",
      onLate: () => (called = true),
    });
    await sleep(30);
    assert(got === 7 && !called, "a timely value is returned, not disposed of");
  });

  await run("an in-flight wait is visible while it is still waiting", async () => {
    // The gap this closes: every cacheDiag event is recorded when a decision
    // COMPLETES, so a tab that is currently hanging reports stats() === {}.
    // That emptiness is what made the original bug so hard to place. A wait
    // must be inspectable while it is still a wait.
    let release: (v: string) => void = () => {};
    const p = new Promise<string>((res) => {
      release = res;
    });
    const wait = withDeadline(p, 5_000, { what: "indexedDB.open" });
    const inflight = pendingWaits();
    assert(inflight.length === 1, `expected 1 in-flight wait, got ${inflight.length}`);
    assert(inflight[0].what === "indexedDB.open", `wrong label: ${inflight[0].what}`);
    assert(inflight[0].deadlineMs === 5_000, "the deadline is reported");
    assert(inflight[0].elapsedMs >= 0, "elapsed time is reported");
    release("done");
    await wait;
    assert(pendingWaits().length === 0, "a settled wait is no longer in flight");
  });

  await run("a wait that misses its deadline stops being in flight", async () => {
    // Leaking registry entries would turn the diagnostic into a liar, showing
    // phantom waits that outlive the code that started them.
    let err: unknown;
    try {
      await withDeadline(new Promise<string>(() => {}), 20, { what: "GET /api/cache-key" });
    } catch (e) {
      err = e;
    }
    assert(isDeadlineExceeded(err), "the deadline fired");
    assert(pendingWaits().length === 0, `registry leaked: ${JSON.stringify(pendingWaits())}`);
  });

  await run("a rejected wait stops being in flight", async () => {
    let err: unknown;
    try {
      await withDeadline(Promise.reject(new Error("boom")), 5_000, { what: "x" });
    } catch (e) {
      err = e;
    }
    assert(String(err).includes("boom"), "the real error propagates");
    assert(pendingWaits().length === 0, "registry cleared on rejection");
  });

  // ── Missed-deadline record ──────────────────────────────────────────────
  //
  // A missed deadline is the tab's one durable trace of cache lock contention:
  // the cache did not fail, it was too slow, and the caller has already moved
  // on to the network by the time the answer arrives. The perf HUD reads this
  // record to raise an alert, so its shape is a contract.

  await run("a missed deadline is recorded with its label and budget", async () => {
    await withDeadline(new Promise<string>(() => {}), 20, {
      what: "indexedDB.read conversation_meta",
    }).catch(() => {});
    const missed = missedDeadlines();
    assert(missed.length === 1, `expected 1 record, got ${missed.length}`);
    assert(missed[0].what === "indexedDB.read conversation_meta", `label: ${missed[0].what}`);
    assert(missed[0].deadlineMs === 20, "budget recorded");
    assert(Date.now() - missed[0].at < 5_000, "timestamped now");
    assert(missed[0].outcome === "pending", "no outcome until the operation settles");
  });

  await run("a timely wait leaves no record", async () => {
    await withDeadline(Promise.resolve(1), 1_000, { what: "indexedDB.open" });
    await withDeadline(Promise.reject(new Error("quota")), 1_000, { what: "indexedDB.open" }).catch(
      () => {},
    );
    assert(missedDeadlines().length === 0, "only missed deadlines are recorded");
  });

  await run("a late value marks the miss as completed with its true duration", async () => {
    // "Completed late" is what separates contention from breakage: the lock
    // was held by someone else, and the operation was fine once it got it.
    let release!: (v: string) => void;
    const p = new Promise<string>((r) => {
      release = r;
    });
    await withDeadline(p, 20, { what: "indexedDB.startup transaction" }).catch(() => {});
    await sleep(30);
    release("done");
    await sleep(0);
    const [rec] = missedDeadlines();
    assert(rec.outcome === "completed", `outcome: ${rec.outcome}`);
    assert(
      rec.settledAfterMs !== undefined && rec.settledAfterMs >= 40,
      `duration spans the whole wait, not just the overrun: ${rec.settledAfterMs}`,
    );
  });

  await run("a late error marks the miss as failed and keeps the error", async () => {
    let fail!: (e: unknown) => void;
    const p = new Promise<string>((_r, rej) => {
      fail = rej;
    });
    await withDeadline(p, 20, { what: "indexedDB.open" }).catch(() => {});
    fail(new Error("VersionError"));
    await sleep(0);
    const [rec] = missedDeadlines();
    assert(rec.outcome === "failed", `outcome: ${rec.outcome}`);
    assert(String(rec.error).includes("VersionError"), "error text kept for the HUD");
  });

  await run("a caller that aborts its own late operation is recorded as abandoned", async () => {
    // hydrate aborts its readonly tx after giving up so the bulk read cannot
    // wake later and duplicate the REST work; that AbortError is the caller's
    // own doing, not a cache failure, and must not read as one in the HUD.
    let fail!: (e: unknown) => void;
    const p = new Promise<string>((_r, rej) => {
      fail = rej;
    });
    await withDeadline(p, 20, { what: "indexedDB.read conversation_meta" }).catch(() => {});
    fail(new DOMException("The transaction was aborted", "AbortError"));
    await sleep(0);
    assert(missedDeadlines()[0].outcome === "abandoned", "self-inflicted abort is not a failure");
  });

  await run("the record is bounded and newest-last", async () => {
    for (let i = 0; i < MISSED_DEADLINE_BUFFER + 5; i++) {
      await withDeadline(new Promise<string>(() => {}), 1, { what: `op${i}` }).catch(() => {});
    }
    const missed = missedDeadlines();
    assert(missed.length === MISSED_DEADLINE_BUFFER, `bounded at ${MISSED_DEADLINE_BUFFER}`);
    assert(missed[missed.length - 1].what === `op${MISSED_DEADLINE_BUFFER + 4}`, "newest last");
    assert(missed[0].what === "op5", "oldest dropped first");
    resetMissedDeadlines();
    assert(missedDeadlines().length === 0, "reset empties the record");
  });

  await run("waits sharing a label are still distinguishable", async () => {
    // Two hydrates can be in flight at once (a quick conversation switch does
    // not cancel the first), both labelled "indexedDB.read conversation_meta";
    // the HUD keys its rows on the id, so it must be unique per wait.
    const a = withDeadline(new Promise<string>(() => {}), 20, { what: "indexedDB.read x" });
    const b = withDeadline(new Promise<string>(() => {}), 20, { what: "indexedDB.read x" });
    const [w1, w2] = pendingWaits();
    assert(w1.id !== w2.id, `in-flight ids differ: ${w1.id} vs ${w2.id}`);
    await Promise.allSettled([a, b]);
    const [m1, m2] = missedDeadlines();
    assert(m1.id !== m2.id, `missed ids differ: ${m1.id} vs ${m2.id}`);
    assert([w1.id, w2.id].includes(m1.id), "a miss keeps the id it had in flight");
  });

  await run("IndexedDB waits are told apart from other bounded waits", async () => {
    // The HUD alert is specifically about cache lock contention, so it must
    // not fire for a stalled cache-key fetch or a lock request.
    assert(isIndexedDBWait("indexedDB.open"), "open");
    assert(isIndexedDBWait("indexedDB.startup transaction"), "startup tx");
    assert(isIndexedDBWait("indexedDB.read conversation_meta"), "read");
    assert(!isIndexedDBWait("GET /api/cache-key"), "fetch is not IDB");
    assert(!isIndexedDBWait("navigator.locks shelley-cache-key"), "lock is not IDB");
  });

  console.log("\ndeadline tests passed");
}

await main();
