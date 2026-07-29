// cryptoKey.ts — client side of the IndexedDB encryption scheme.
//
// Holds the per-browser AES-GCM key handed out by GET /api/cache-key, and
// exposes wrap/unwrap helpers used by messageStore.
//
// The key is imported with extractable=false so that the raw bytes cannot
// be re-exported from JS after import; the wire-format bytes only ever live
// in a local variable during fetch().
//
// Threat-model summary: see server/cache_key.go. Not end-to-end. Storing
// encrypted-at-rest in IDB so a stolen browser profile / shared OS account
// without a live auth session can't read prior conversations.

import { cacheDiag } from "./cacheDiag";

export interface CacheKeyMaterial {
  keyId: string;
  key: CryptoKey;
  /** Alg returned by server, e.g. "AES-GCM-256". For diagnostic logging. */
  alg: string;
}

export interface CacheKeyFetcher {
  /** Fetch+import a fresh key. Throws on any error so the caller can fall
   * back to network-only mode. */
  fetch(): Promise<CacheKeyMaterial>;
  /** Tell the server to wipe its session (logout / clear-cache). */
  clear(): Promise<void>;
}

interface FetchedKeyJSON {
  key_id: string;
  key: string; // base64-std, 32 bytes
  alg: string;
}

function b64decode(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** Production fetcher hitting /api/cache-key on the same origin. */
export class HttpCacheKeyFetcher implements CacheKeyFetcher {
  constructor(private readonly endpoint = "/api/cache-key") {}
  async fetch(): Promise<CacheKeyMaterial> {
    const r = await window.fetch(this.endpoint, {
      method: "GET",
      credentials: "include",
      cache: "no-store",
    });
    if (!r.ok) throw new Error(`cache-key: HTTP ${r.status}`);
    const body = (await r.json()) as FetchedKeyJSON;
    return importMaterial(body);
  }
  async clear(): Promise<void> {
    const r = await window.fetch("/api/cache-session/clear", {
      method: "POST",
      credentials: "include",
      cache: "no-store",
    });
    if (!r.ok) {
      // Don't silently downgrade: if the server didn't actually rotate
      // the cookie/session, returning success would leave the next
      // GET /api/cache-key handing back the same key with the same
      // key_id, defeating rotation. Caller (wipeAndRotateKey) catches
      // and logs.
      throw new Error(`cache-session/clear: HTTP ${r.status}`);
    }
  }
}

/** Import a server response into a non-extractable CryptoKey. */
export async function importMaterial(body: FetchedKeyJSON): Promise<CacheKeyMaterial> {
  if (!body.key_id || !body.key || !body.alg) {
    throw new Error("cache-key: malformed response");
  }
  const raw = b64decode(body.key);
  if (raw.byteLength !== 32) {
    throw new Error(`cache-key: bad key length ${raw.byteLength}`);
  }
  const buf = new ArrayBuffer(raw.byteLength);
  new Uint8Array(buf).set(raw);
  const key = await crypto.subtle.importKey(
    "raw",
    buf,
    { name: "AES-GCM" },
    /* extractable */ false,
    ["encrypt", "decrypt"],
  );
  // Best-effort zero of both local buffers. V8 may still have copies
  // and the underlying WebCrypto implementation may also retain its
  // own; this is intent more than guarantee.
  raw.fill(0);
  new Uint8Array(buf).fill(0);
  return { keyId: body.key_id, key, alg: body.alg };
}

// ─── AES-GCM wrap/unwrap helpers ─────────────────────────────────────────────

const IV_BYTES = 12; // AES-GCM nominal IV size
const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

/**
 * Wrap a JSON-serializable value into { iv, ct }. Random IV per call.
 *
 * Note: SubtleCrypto's TS bindings reject `Uint8Array<ArrayBufferLike>`
 * because the underlying buffer might be SharedArrayBuffer. We copy into
 * a fresh ArrayBuffer-backed view via `toArrayBuffer()` to make the types
 * check and to defend against shared-buffer aliasing.
 */
function toArrayBuffer(view: Uint8Array): ArrayBuffer {
  const out = new ArrayBuffer(view.byteLength);
  new Uint8Array(out).set(view);
  return out;
}

export async function wrapJSON(
  key: CryptoKey,
  value: unknown,
  aad?: Uint8Array,
): Promise<{ iv: Uint8Array; ct: Uint8Array }> {
  const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
  const pt = textEncoder.encode(JSON.stringify(value));
  const params: AesGcmParams = { name: "AES-GCM", iv: toArrayBuffer(iv) };
  if (aad) params.additionalData = toArrayBuffer(aad);
  const ctBuf = await crypto.subtle.encrypt(params, key, toArrayBuffer(pt));
  return { iv, ct: new Uint8Array(ctBuf) };
}

/**
 * Inverse of wrapJSON. Throws on auth-tag failure (wrong key / tampered /
 * AAD mismatch).
 *
 * Pass the same `aad` that was used at encryption time; mismatches surface
 * as decrypt failure (caller treats as undecryptable row).
 */
export async function unwrapJSON<T>(
  key: CryptoKey,
  iv: Uint8Array,
  ct: Uint8Array,
  aad?: Uint8Array,
): Promise<T> {
  const params: AesGcmParams = { name: "AES-GCM", iv: toArrayBuffer(iv) };
  if (aad) params.additionalData = toArrayBuffer(aad);
  const ptBuf = await crypto.subtle.decrypt(params, key, toArrayBuffer(ct));
  return JSON.parse(textDecoder.decode(ptBuf)) as T;
}

/**
 * Build a stable AAD byte string for a row. Binding the plaintext index
 * fields into AES-GCM's additional-authenticated-data prevents an attacker
 * with IDB write access from splicing a valid {iv,ct} blob from one row
 * onto another row's plaintext keys (e.g. swapping message bodies between
 * conversations). The AAD is not encrypted, only authenticated.
 *
 * Versioned with a leading tag so we can change the layout without
 * silently invalidating existing rows (mismatch reads as decrypt failure,
 * which we treat as undecryptable → effectively a per-row wipe).
 */
export function rowAAD(parts: Record<string, string | number>): Uint8Array {
  // Deterministic key order so wrap and unwrap agree without relying on
  // insertion order at the call site.
  const keys = Object.keys(parts).sort();
  const canonical: Record<string, string | number> = {};
  for (const k of keys) canonical[k] = parts[k];
  return textEncoder.encode("shelley-idb-aad-v1:" + JSON.stringify(canonical));
}

// ─── Single-tab key holder ───────────────────────────────────────────────────
//
// Most call sites only need a singleton view. Tests inject their own holder.

export class CacheKeyHolder {
  private material: CacheKeyMaterial | null = null;
  private pending: Promise<CacheKeyMaterial | null> | null = null;

  constructor(private readonly fetcher: CacheKeyFetcher) {}

  /** Force-acquire (or refresh) the key. Returns null if the server refuses. */
  async ensure(): Promise<CacheKeyMaterial | null> {
    if (this.material) return this.material;
    if (!this.pending) {
      this.pending = this.fetcher
        .fetch()
        .then((m) => {
          this.material = m;
          return m;
        })
        .catch((err) => {
          cacheDiag("fail", "cache_key.unavailable", { error: String(err) });
          this.material = null;
          return null;
        })
        .finally(() => {
          this.pending = null;
        });
    }
    return this.pending;
  }

  current(): CacheKeyMaterial | null {
    return this.material;
  }

  /** Drop the in-memory key. Used after server-side clear and on logout. */
  forget(): void {
    this.material = null;
  }

  async clear(): Promise<void> {
    await this.fetcher.clear();
    this.forget();
  }
}
