// cacheDiag.ts — observability for the IndexedDB message cache.
//
// The cache is invisible when it works and, until now, equally invisible
// when it didn't: a poisoned hydration or a refused cache key just showed
// up as "the conversation reloads from the server every time", with nothing
// in the console to say why. This module gives every cache decision a name,
// counts it, and surfaces the ones that mean something is broken.
//
// Three severities:
//   fail  — the cache is not doing its job (key refused, IDB open/read
//           failure, undecryptable rows, gaps). Always warns, but at most
//           once per (event, key) pair per page load so a per-message
//           failure can't flood the console.
//   info  — a legitimate reload (cold cache, stream reconnect, server ahead
//           of us). Silent unless verbose logging is on.
//   hit   — the cache served the conversation. Silent unless verbose.
//
// Humans and agents:
//   __shelleyCache.stats()          -> { event: count }
//   __shelleyCache.log()            -> console.table of the same
//   __shelleyCache.verbose(true)    -> per-decision logging, persisted
//   __shelleyCache.events()         -> recent decisions with details
//
// Verbose mode is also enabled by ?cache_debug=1 or localStorage
// shelley_cache_debug=1, so it can be turned on before the first frame.

export type CacheDiagLevel = "hit" | "info" | "fail";

export interface CacheDiagEvent {
  level: CacheDiagLevel;
  event: string;
  detail?: Record<string, unknown>;
  at: number;
}

const VERBOSE_KEY = "shelley_cache_debug";
const RING_SIZE = 100;

const counts = new Map<string, number>();
const ring: CacheDiagEvent[] = [];
const warnedOnce = new Set<string>();

function initialVerbose(): boolean {
  if (typeof window === "undefined") return false;
  try {
    const params = new URLSearchParams(window.location.search);
    if (params.get("cache_debug") === "1") return true;
    return window.localStorage?.getItem(VERBOSE_KEY) === "1";
  } catch {
    return false;
  }
}

let verboseEnabled = initialVerbose();

/**
 * Record a cache decision. `key` (usually a conversation id) only dedupes
 * the console warning; the counter always increments.
 */
export function cacheDiag(
  level: CacheDiagLevel,
  event: string,
  detail?: Record<string, unknown>,
  key?: string,
): void {
  counts.set(event, (counts.get(event) ?? 0) + 1);
  const rec: CacheDiagEvent = { level, event, detail, at: Date.now() };
  ring.push(rec);
  if (ring.length > RING_SIZE) ring.shift();
  if (level === "fail") {
    const dedupeKey = `${event}\u0000${key ?? ""}`;
    if (!warnedOnce.has(dedupeKey)) {
      warnedOnce.add(dedupeKey);
      console.warn(`[shelley-cache] ${event}`, detail ?? {});
      return;
    }
  }
  if (verboseEnabled) {
    console.debug(`[shelley-cache] ${event}`, detail ?? {});
  }
}

export function cacheDiagStats(): Record<string, number> {
  return Object.fromEntries([...counts.entries()].sort(([a], [b]) => a.localeCompare(b)));
}

export function cacheDiagEvents(): CacheDiagEvent[] {
  return ring.slice();
}

export function cacheDiagReset(): void {
  counts.clear();
  ring.length = 0;
  warnedOnce.clear();
}

export function cacheDiagVerbose(on?: boolean): boolean {
  if (on === undefined) return verboseEnabled;
  verboseEnabled = on;
  try {
    if (on) window.localStorage?.setItem(VERBOSE_KEY, "1");
    else window.localStorage?.removeItem(VERBOSE_KEY);
  } catch {
    // private mode / storage disabled: keep the in-memory setting
  }
  return verboseEnabled;
}

declare global {
  interface Window {
    __shelleyCache?: {
      stats: typeof cacheDiagStats;
      events: typeof cacheDiagEvents;
      reset: typeof cacheDiagReset;
      verbose: typeof cacheDiagVerbose;
      log: () => void;
    };
  }
}

if (typeof window !== "undefined") {
  window.__shelleyCache = {
    stats: cacheDiagStats,
    events: cacheDiagEvents,
    reset: cacheDiagReset,
    verbose: cacheDiagVerbose,
    log: () => {
      const rows = Object.entries(cacheDiagStats()).map(([event, count]) => ({ event, count }));
      console.table(rows);
    },
  };
}
