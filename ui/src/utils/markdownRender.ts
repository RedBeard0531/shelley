// Framework-agnostic markdown rendering + sanitization pipeline.
// Extracted from components/MarkdownContent.tsx so both the React component
// and the Vue SFC can share an identical implementation. The React file now
// re-exports renderMarkdownToSafeHTML + classifyImageSrc from here so the
// existing test (components/MarkdownContent.test.ts) keeps passing.
import { Marked, type Token } from "marked";
import DOMPurify from "dompurify";

// Maximum size (in characters of the data: URI) we are willing to inline.
// Keeps the DOM and persisted payloads from ballooning when a model emits a
// huge base64 image directly in its markdown.
const MAX_DATA_URI_LENGTH = 2_000_000;

// Prefix of the per-message file endpoint that serves local images. Mirrors
// the route registered in server/server.go.
const FILE_ENDPOINT_RE = /^\/api\/message\/[^/]+\/file\?path=/;

type ImageKind = "local" | "data" | "remote" | "invalid";

// classifyImageSrc decides how a markdown image src should be handled.
export function classifyImageSrc(src: string): ImageKind {
  const s = src.trim();
  if (s === "") return "invalid";
  // Protocol-relative URLs (//host/...) are remote.
  if (s.startsWith("//")) return "remote";
  // data: URIs are inlined when they are images and small enough.
  if (/^data:/i.test(s)) {
    return /^data:image\//i.test(s) && s.length <= MAX_DATA_URI_LENGTH ? "data" : "invalid";
  }
  // Any other explicit scheme (http:, https:, file:, javascript:, etc.) is
  // treated as remote and not auto-loaded.
  if (/^[a-z][a-z0-9+.-]*:/i.test(s)) return "remote";
  // Everything else is a local path: absolute (/foo.png) or relative
  // (./out/x.png, out/x.png, ../shared/x.png).
  return "local";
}

// fileEndpointURL builds the same-origin URL that serves a local image
// referenced by a specific message.
export function fileEndpointURL(messageId: string, path: string): string {
  return `/api/message/${encodeURIComponent(messageId)}/file?path=${encodeURIComponent(path)}`;
}

// buildMarked returns a Marked instance that rewrites local-path image tokens
// to the per-message file endpoint. Remote images are left with their original
// href (and later stripped by the sanitizer); data images are passed through.
function buildMarked(messageId?: string): Marked {
  const instance = new Marked({ gfm: true, breaks: true });
  instance.use({
    walkTokens(token) {
      if (token.type !== "image") return;
      const kind = classifyImageSrc(token.href ?? "");
      if (kind === "local") {
        // Only rewrite (and thus render) when we know the owning message.
        token.href = messageId ? fileEndpointURL(messageId, token.href) : "";
      }
      // data: kept as-is; remote/invalid left untouched and dropped by sanitize.
    },
  });
  return instance;
}

// Make all links open in new tabs, and restrict <input> to checkboxes only.
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
  // Only allow checkbox inputs (for GFM task lists); remove all others.
  if (node.tagName === "INPUT" && node.getAttribute("type") !== "checkbox") {
    node.remove();
  }
  // Images are admitted only when they point at the same-origin per-message
  // file endpoint or are a small inline image data: URI. Anything else
  // (remote URLs, oversized/non-image data URIs, unrewritten local paths) is
  // removed so we never auto-load arbitrary remote or unauthorized content.
  if (node.tagName === "IMG") {
    const src = node.getAttribute("src") ?? "";
    const allowed =
      FILE_ENDPOINT_RE.test(src) ||
      (/^data:image\//i.test(src) && src.length <= MAX_DATA_URI_LENGTH);
    if (!allowed) {
      node.remove();
      return;
    }
    node.setAttribute("loading", "lazy");
  }
});

const SANITIZE_OPTS = {
  ALLOWED_TAGS: [
    "p",
    "br",
    "strong",
    "em",
    "code",
    "pre",
    "blockquote",
    "ul",
    "ol",
    "li",
    "a",
    "img",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "hr",
    "table",
    "thead",
    "tbody",
    "tr",
    "th",
    "td",
    "del",
    "input",
    "span",
    "sup",
    "div",
    "details",
    "summary",
  ],
  ALLOWED_ATTR: [
    "href",
    "src",
    "alt",
    "title",
    "loading",
    "target",
    "rel",
    "type",
    "checked",
    "disabled",
    "class",
    "open",
  ],
};

// Cache of rendered+sanitized HTML, scoped to the lifetime of the immutable
// object (in practice, a Message) that owns a markdown run. Keying on the
// object reference itself — rather than on the rendered text — means no
// source text is retained as a cache key, and entries disappear for free once
// their owner becomes unreachable (conversation pruned, tab closed, etc.): no
// eviction policy or size cap needed.
//
// A single owner can have multiple markdown runs (coalesceContent splits a
// message's content into several adjacent text blocks whenever tool calls
// interleave with prose), so each owner maps to a small Map<runKey, html>
// rather than a single string. Callers supply a runKey that's stable and
// unique for a given run within that owner (Message.vue uses the
// coalescedContent index).
const cache = new WeakMap<object, Map<string, string>>();

export interface MarkdownCacheKey {
  // Object whose lifetime bounds the cache entry.
  owner: object;
  // Distinguishes multiple runs within the same owner.
  runKey: string;
}

export interface MarkdownRenderInfo {
  // True when `text` ends inside an unterminated fenced code block — the live
  // streaming case where that block is still receiving tokens. Only meaningful
  // for uncached renders; cached (finalized message) callers ignore it.
  endsInOpenFence: boolean;
}

// Walk past container tokens (blockquote, list > item) to the real last leaf
// token — an unterminated fence inside a trailing blockquote or list item is
// still "the open fence" (marked strips the "> " / indentation prefixes from
// the nested token's raw).
function deepestLastToken(tokens: Token[]): Token | null {
  const last = tokens.length > 0 ? tokens[tokens.length - 1] : null;
  if (!last) return null;
  if (last.type === "list" && last.items.length > 0) {
    const itemTokens = last.items[last.items.length - 1].tokens;
    if (itemTokens && itemTokens.length > 0) return deepestLastToken(itemTokens);
  }
  const nested = (last as { tokens?: Token[] }).tokens;
  if (nested && nested.length > 0) return deepestLastToken(nested);
  return last;
}

// An unterminated fence consumes the rest of the document, so it is always the
// deepest last token: a fenced code token whose raw has no closing fence that
// matches its opener (marked's rule: same exact fence characters, with only
// trailing ~/` and spaces allowed after them — so ``` markers inside code or
// shorter / differently-charactered lines can't mislead a fence counter).
function endsInOpenFence(tokens: Token[]): boolean {
  const last = deepestLastToken(tokens);
  // Indented code blocks have no `lang`; fenced ones do ("" when unlabeled).
  if (!last || last.type !== "code" || last.lang === undefined) return false;
  const raw = last.raw;
  const opener = /^ {0,3}(`{3,}(?=[^`\n]*(?:\n|$))|~{3,})/.exec(raw);
  if (!opener) return false;
  const fence = opener[1];
  // The closer must be the last non-blank line (a closed fence's raw ends
  // with the closer line; trailing blank lines don't count).
  const lines = raw.split("\n");
  let lastLine = "";
  for (let i = lines.length - 1; i >= 0 && lastLine === ""; i--) {
    lastLine = lines[i].trimEnd();
  }
  if (lastLine === "") return true;
  return !new RegExp(`^ {0,3}${fence}[~\`]* *$`).test(lastLine);
}

// renderMarkdownToSafeHTML parses markdown and returns sanitized HTML.
//
// `messageId` drives the local-image URL rewrite only (see buildMarked above)
// and plays no part in caching. `cacheKey`, when supplied, memoizes the result
// for the lifetime of `cacheKey.owner`; callers whose text can change without
// a new owner — the streaming preview, the distillation preview, export —
// omit it and always re-render. `out`, when supplied, receives parse-state
// information (see MarkdownRenderInfo) for live text.
export function renderMarkdownToSafeHTML(
  text: string,
  messageId?: string,
  cacheKey?: MarkdownCacheKey,
  out?: MarkdownRenderInfo,
): string {
  let runs = cacheKey ? cache.get(cacheKey.owner) : undefined;
  const cached = cacheKey ? runs?.get(cacheKey.runKey) : undefined;
  if (cached !== undefined) return cached;

  const marked = buildMarked(messageId);
  const tokens = marked.lexer(text);
  // parse() runs walkTokens — where the local-image rewrite hook lives —
  // between lexing and parsing; replicate that when splitting the two phases.
  if (marked.defaults.walkTokens) marked.walkTokens(tokens, marked.defaults.walkTokens);
  const raw = marked.parser(tokens);
  const html = DOMPurify.sanitize(raw, SANITIZE_OPTS);

  if (out) out.endsInOpenFence = endsInOpenFence(tokens);

  if (cacheKey) {
    if (!runs) {
      runs = new Map();
      cache.set(cacheKey.owner, runs);
    }
    runs.set(cacheKey.runKey, html);
  }
  return html;
}
