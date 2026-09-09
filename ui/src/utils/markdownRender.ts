// Framework-agnostic markdown rendering + sanitization pipeline.
// Extracted from components/MarkdownContent.tsx so both the React component
// and the Vue SFC can share an identical implementation. The React file now
// re-exports renderMarkdownToSafeHTML + classifyImageSrc from here so the
// existing test (components/MarkdownContent.test.ts) keeps passing.
import { Marked, type Token } from "marked";
import DOMPurify from "dompurify";
import { rewriteLocalhostLink, type LocalhostLinkOptions } from "./linkify";

// A file reference: an inline-code span holding a file path with an optional
// 1-based line or line range (`src/app.ts`, `src/app.ts:42`, `src/app.ts:42-87`).
// The UI renders these as clickable links that open the file in the editor at
// that line, selecting the range when given (line defaults to 1).
export interface FileRef {
  path: string;
  line?: number;
  endLine?: number;
}

// Path characters exclude whitespace and markup characters, so commands
// (`npm run build`), slices (`data[10:20]`), and quoted log lines never match.
const FILE_REF_RE = /^([A-Za-z0-9._~/-]+)(?::(\d{1,9})(?:-(\d{1,9}))?)?$/;

// parseFileRef decides whether an inline-code span is a file reference.
// Purely syntactic (the renderer has no filesystem access): the span must be
// a path (restricted character set) with an optional 1-based line/range
// suffix, and the path must contain a slash — the system prompt directs the
// model to write top-level files with a `./` prefix. That single shape rule
// keeps host:port (`localhost:3000`), IP:port (`127.0.0.1:8080`), times
// (`12:30`), and dotted versions (`v1.2.3`) from ever matching. Home-relative
// `~/...` paths are references (the server's file endpoints expand them);
// a trailing slash (a directory) or a non-positive line is not a reference.
export function parseFileRef(text: string): FileRef | null {
  const m = FILE_REF_RE.exec(text);
  if (!m) return null;
  const path = m[1];
  if (!path.includes("/") || path.endsWith("/")) return null;
  // Tilde is only legal in the leading `~/` form: `~user/x` (another user's
  // home), mid-path tildes (`a~b/c`), and bare `~` are not references the
  // endpoints can serve.
  if (path.includes("~") && !path.startsWith("~/")) return null;
  const hasLine = m[2] !== undefined;
  if (!hasLine) return { path };
  let line = parseInt(m[2], 10);
  let endLine = m[3] !== undefined ? parseInt(m[3], 10) : line;
  if (endLine < line) [line, endLine] = [endLine, line];
  if (line < 1) return null;
  return { path, line, endLine };
}

function escapeAttr(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// fileRefAnchor renders a parsed FileRef as an anchor labeled with the
// original span text (path:line). The click is handled by MarkdownContent's
// delegated listener (via useOpenFileEditor); the href exists so the link is
// keyboard-activatable.
function fileRefAnchor(ref: FileRef, label: string): string {
  const attrs = [
    'href="#"',
    'class="file-ref"',
    `data-file-path="${escapeAttr(ref.path)}"`,
  ];
  if (ref.line !== undefined) attrs.push(`data-line="${ref.line}"`);
  if (ref.endLine !== undefined && ref.endLine !== ref.line) {
    attrs.push(`data-end-line="${ref.endLine}"`);
  }
  return `<a ${attrs.join(" ")}>${escapeAttr(label)}</a>`;
}

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
function buildMarked(messageId?: string, localhostLinks?: LocalhostLinkOptions): Marked {
  const instance = new Marked({ gfm: true, breaks: true });
  instance.use({
    walkTokens(token) {
      if (token.type === "image") {
        const kind = classifyImageSrc(token.href ?? "");
        if (kind === "local") {
          // Only rewrite (and thus render) when we know the owning message.
          token.href = messageId ? fileEndpointURL(messageId, token.href) : "";
        }
        // data: kept as-is; remote/invalid left untouched and dropped by sanitize.
        return;
      }
      if (token.type === "link" && localhostLinks) {
        const original = token.href;
        const rewritten = rewriteLocalhostLink(original, localhostLinks);
        token.href = rewritten;
        // Bare URLs and <autolinks> use the URL as their visible label.
        if (rewritten !== original && token.text === original) {
          token.text = rewritten;
          const labelToken = token.tokens?.length === 1 ? token.tokens[0] : undefined;
          if (labelToken?.type === "text") {
            labelToken.text = rewritten;
            labelToken.raw = rewritten;
          }
        }
      }
    },
    renderer: {
      // File references (`path:line` inline code) render as clickable
      // anchors; anything else falls through to the default <code> rendering
      // (marked's object-renderer contract: return false for the default).
      codespan(token) {
        const ref = parseFileRef(token.text);
        return ref ? fileRefAnchor(ref, token.text) : false;
      },
    },
  });
  return instance;
}

// Make all links open in new tabs, and restrict <input> to checkboxes only.
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    // File references open the in-app editor via MarkdownContent's delegated
    // click handler; they must not be turned into new-tab links.
    if (!node.hasAttribute("data-file-path")) {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer");
    } else {
      // Our renderer only emits file-ref anchors that are exactly
      // parseFileRef(label)'s canonical output: the label is the original
      // `path:line` span text, data-file-path is the parsed path (no :line
      // suffix), and the line attrs match the parsed values exactly
      // (data-line present iff the label has a line, data-end-line iff a
      // true range; 1-based, unpadded). Raw HTML in message markdown — or
      // in web pages rendered through this same pipeline — could otherwise
      // forge the affordance or make the target disagree with the displayed
      // reference; remove anything that isn't that exact contract.
      const label = node.textContent ?? "";
      const ref = parseFileRef(label);
      const path = node.getAttribute("data-file-path") ?? "";
      const line = node.getAttribute("data-line") ?? "";
      const endLine = node.getAttribute("data-end-line") ?? "";
      const expectEnd =
        ref?.endLine !== undefined && ref.endLine !== ref.line ? String(ref.endLine) : "";
      const valid =
        ref !== null &&
        path === ref.path &&
        line === String(ref.line ?? "") &&
        endLine === expectEnd;
      if (!valid) {
        node.remove();
        return;
      }
      // Neutralize href/target a forged-but-contract-valid anchor might
      // carry: refs open the in-app editor and never navigate (middle-click
      // bypasses the click handler, so the href itself must be inert).
      node.setAttribute("href", "#");
      node.removeAttribute("target");
      node.removeAttribute("rel");
    }
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
  // The file-reference feature relies on data-* attributes surviving
  // sanitization (DOMPurify's default); pinned explicitly so a future
  // default change can't silently disable it.
  ALLOW_DATA_ATTR: true,
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
  localhostLinks?: LocalhostLinkOptions,
  out?: MarkdownRenderInfo,
): string {
  let runs = cacheKey ? cache.get(cacheKey.owner) : undefined;
  const cached = cacheKey ? runs?.get(cacheKey.runKey) : undefined;
  if (cached !== undefined) return cached;

  const marked = buildMarked(messageId, localhostLinks);
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
