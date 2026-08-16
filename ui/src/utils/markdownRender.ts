// Framework-agnostic markdown rendering + sanitization pipeline.
// Extracted from components/MarkdownContent.tsx so both the React component
// and the Vue SFC can share an identical implementation. The React file now
// re-exports renderMarkdownToSafeHTML + classifyImageSrc from here so the
// existing test (components/MarkdownContent.test.ts) keeps passing.
import { Marked } from "marked";
import DOMPurify from "dompurify";

import { perfCount } from "./perf";

// Maximum size (in characters of the data: URI) we are willing to inline.
// Keeps the DOM and persisted payloads from ballooning when a model emits a
// huge base64 image directly in its markdown.
const MAX_DATA_URI_LENGTH = 2_000_000;

// Prefix of the per-message file endpoint that serves local images. Mirrors
// the route registered in server/server.go.
const FILE_ENDPOINT_RE = /^\/api\/message\/[^/]+\/file\?path=/;

// URL of the lazily-loaded syntax-highlighting chunk (ui/scripts/build.js).
// Kept behind a variable so unit tests can substitute a stub module without
// requiring the built chunk to exist.
const MARKDOWN_HIGHLIGHT_URL = "/markdown-highlight.js";

interface MarkdownHighlightAPI {
  highlightCodeBlock(code: string, lang: string): string | null;
}

let markdownHighlightURL = MARKDOWN_HIGHLIGHT_URL;
export function setMarkdownHighlightURLForTests(url: string): void {
  markdownHighlightURL = url;
  highlightModulePromise = undefined;
}
export function resetMarkdownHighlightURLForTests(): void {
  markdownHighlightURL = MARKDOWN_HIGHLIGHT_URL;
  highlightModulePromise = undefined;
}

let highlightModulePromise: Promise<MarkdownHighlightAPI | null> | undefined;
// Loads the highlight chunk once. Failure is not fatal: the renderer falls
// back to plain <pre><code> blocks (identical code text, no colors), but the
// error is surfaced to the console rather than swallowed.
function loadHighlightModule(): Promise<MarkdownHighlightAPI | null> {
  highlightModulePromise ??= import(/* @vite-ignore */ markdownHighlightURL)
    .then((m) => m as unknown as MarkdownHighlightAPI)
    .catch((err) => {
      console.error("failed to load syntax highlighter; code blocks render unhighlighted:", err);
      return null;
    });
  return highlightModulePromise;
}

// Counts ``` / ~~~ fence markers in the source. Streaming previews render
// incomplete documents; while a fence is open the block is still being
// generated, so highlighting it would tokenize half-written code on every
// keystroke. An odd count is unambiguous mid-stream state, so the whole
// render defers.
function fenceSummary(md: string): { fences: number; closed: boolean } {
  let fences = 0;
  for (const line of md.split("\n")) {
    const stripped = line.replace(/^\s+/, "");
    if (/^(?:```|~~~)/.test(stripped)) fences++;
  }
  return { fences, closed: fences % 2 === 0 };
}

// Escapes text the way marked's default code renderer does (escape with
// double=true: &, <, >, " and '), so the plain fallback is byte-identical to
// the pre-highlighting output.
const HTML_ESCAPES: Record<string, string> = {
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  '"': "&quot;",
  "'": "&#39;",
};
function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => HTML_ESCAPES[c]!);
}

// Mirrors marked's default code renderer exactly: first non-space token of the
// language for the class, and the body normalized to end in exactly one
// newline. Used when syntax highlighting is unavailable or unclosed (see
// allFencesClosed).
function defaultCodeBlock(
  text: string,
  lang: string | undefined,
  escaped: boolean | undefined,
): string {
  const langName = (lang || "").match(/^\S*/)?.[0] ?? "";
  const body = text.replace(/\n$/, "") + "\n";
  const content = escaped ? body : escapeHtml(body);
  const attrs = langName ? ` class="language-${escapeHtml(langName)}"` : "";
  return `<pre><code${attrs}>${content}</code></pre>\n`;
}

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

// The two token shapes the pipeline rewrites in place before parsing. Kept
// structural (not imported from marked) so the walker works across marked
// versions and does not couple rendering to token-internals that may change.
interface ImageToken {
  type: "image";
  href?: string;
}
interface CodeToken {
  type: "code";
  text: string;
  lang?: string | undefined;
  escaped?: boolean | undefined;
  // Stashed by highlightCodeTokens, consumed by the code renderer.
  __highlighted?: string | undefined;
}
interface ContainerToken {
  type: string;
  // Blockquotes, list items, tables, etc. nest their children here.
  tokens?: unknown[];
}
type WalkableToken = ImageToken | CodeToken | ContainerToken | { type: string };

// buildMarked returns a Marked instance whose code renderer emits highlighted
// HTML for code tokens decorated by highlightCodeTokens, and marked's default
// <pre><code> rendering for everything else. Callers split rendering into the
// three phases (lex → decorate → parse) themselves because syntax highlighting
// is asynchronous and marked's renderers are strictly synchronous.
function buildMarked(): Marked {
  const instance = new Marked({ gfm: true, breaks: true });
  instance.use({
    renderer: {
      code(token: {
        text: string;
        lang?: string | undefined;
        escaped?: boolean | undefined;
      }): string {
        const highlighted = (token as CodeToken).__highlighted;
        if (highlighted) return highlighted;
        // Mirrors marked's default code renderer exactly, so unhighlighted
        // blocks (unknown languages, deferred fences) are byte-identical to
        // the pre-highlighting output.
        return defaultCodeBlock(token.text, token.lang, token.escaped);
      },
    },
  });
  return instance;
}

// Block-level children of a token. Besides the common `tokens` array, marked
// stores list items under `items` (each ListItem with its own `tokens`) and
// table cells under `header` / `rows`, so a plain `tokens`-only recursion
// would miss code blocks and images nested under bullets and tables (marked's
// own walkTokens handles all of these; the hand-rolled walkers must too).
function childBlocks(token: WalkableToken): WalkableToken[][] {
  const t = token as WalkableToken & {
    tokens?: unknown[];
    items?: unknown[];
    header?: unknown[];
    rows?: unknown[][];
  };
  const blocks: WalkableToken[][] = [];
  if (Array.isArray(t.tokens)) blocks.push(t.tokens as WalkableToken[]);
  if (Array.isArray(t.items)) blocks.push(t.items as WalkableToken[]);
  if (Array.isArray(t.header)) blocks.push(t.header as WalkableToken[]);
  if (Array.isArray(t.rows)) blocks.push(...(t.rows as WalkableToken[][]));
  return blocks;
}

// Decorates a token tree for rendering: local-path image tokens are rewritten
// to the per-message file endpoint (remote/data images are left for the
// sanitizer), and fenced code blocks are syntax-highlighted. fencesClosed
// gates highlighting for streaming previews (see fenceSummary).
async function decorateTokens(
  tokens: WalkableToken[],
  messageId: string | undefined,
  fencesClosed: boolean,
): Promise<void> {
  for (const token of tokens) {
    switch (token.type) {
      case "image":
        rewriteImageToken(token as ImageToken, messageId);
        break;
      case "code":
        await highlightCodeToken(token as CodeToken, fencesClosed);
        break;
    }
    for (const child of childBlocks(token)) {
      await decorateTokens(child, messageId, fencesClosed);
    }
  }
}

function rewriteImageToken(token: ImageToken, messageId: string | undefined): void {
  const kind = classifyImageSrc(token.href ?? "");
  if (kind === "local") {
    // Only rewrite (and thus render) when we know the owning message.
    token.href = messageId ? fileEndpointURL(messageId, token.href ?? "") : "";
  }
  // data: kept as-is; remote/invalid left untouched and dropped by sanitize.
}

async function highlightCodeToken(token: CodeToken, fencesClosed: boolean): Promise<void> {
  // Language is the first non-space token of the info string, lowercased.
  const langName = (token.lang || "").match(/^\S*/)?.[0]?.toLowerCase() ?? "";
  if (langName === "" || !fencesClosed) return;
  const module = await loadHighlightModule();
  if (!module) return;
  const start = performance.now();
  const html = module.highlightCodeBlock(token.text.replace(/\n+$/, ""), langName);
  perfCount("markdown.highlight", performance.now() - start);
  if (html) token.__highlighted = html;
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
  // Style attributes are only ever allowed on the highlight pass. Keep
  // exactly shiki's --shiki-* declarations (colors, backgrounds, emphasis)
  // and drop everything else. Filtering here, on DOMPurify's normalized
  // attribute, closes the evasion space a pre-sanitize HTML regex leaves
  // open (unquoted, uppercase, or entity-encoded style=/STYLE= attributes).
  if (!allowShikiStyleAttrs) return;
  const style = node.getAttribute("style");
  if (style == null) return;
  const kept: string[] = [];
  for (const decl of style.split(";")) {
    const colon = decl.indexOf(":");
    if (colon < 0) continue;
    const name = decl.slice(0, colon).trim().toLowerCase();
    const value = decl.slice(colon + 1).trim();
    if (isAllowedShikiDeclaration(name, value)) kept.push(`${name}:${value}`);
  }
  if (kept.length === 0) node.removeAttribute("style");
  else node.setAttribute("style", kept.join("; "));
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

// --shiki-* custom properties shiki's codeToHtml (defaultColor: false) can
// emit. Colors come as --shiki-light/--shiki-dark per token; block
// backgrounds as the -bg variants; emphasis (italic/bold/underline for some
// grammars and themes) as the -font-style / -font-weight / -text-decoration
// variants.
const SHIKI_STYLE_PROP_RE =
  /^--shiki-(?:light|dark)(?:-bg|-font-style|-font-weight|-text-decoration)?$/;
const SHIKI_HEX_VALUE_RE = /^#(?:[0-9a-fA-F]{3,8})$/;
const SHIKI_FONT_STYLE_RE = /^(?:normal|italic|oblique|inherit)$/;
const SHIKI_FONT_WEIGHT_RE = /^(?:normal|bold|bolder|lighter|inherit|[1-9][0-9]{0,3})$/;
const SHIKI_TEXT_DECORATION_RE =
  /^(?:none|underline|line-through|overline|solid|double|wavy|dotted|dashed|inherit)(?:\s+(?:underline|line-through|overline|solid|double|wavy|dotted|dashed))*$/;

function isAllowedShikiDeclaration(name: string, value: string): boolean {
  if (!SHIKI_STYLE_PROP_RE.test(name)) return false;
  if (name.endsWith("-font-style")) return SHIKI_FONT_STYLE_RE.test(value);
  if (name.endsWith("-font-weight")) return SHIKI_FONT_WEIGHT_RE.test(value);
  if (name.endsWith("-text-decoration")) return SHIKI_TEXT_DECORATION_RE.test(value);
  return SHIKI_HEX_VALUE_RE.test(value); // --shiki-* color and -bg
}

// True only while sanitizing the highlight pass's output — the only pass that
// lets DOMPurify keep style attributes at all. DOMPurify hooks are global and
// cannot be scoped per call, so the afterSanitizeAttributes hook below checks
// this flag; ansi.ts and the synchronous (plain) markdown pass sanitize with
// the flag off and are untouched.
let allowShikiStyleAttrs = false;

// Cache of rendered+sanitized HTML, scoped to the lifetime of the immutable
// object (in practice, a Message) that owns a markdown run. Keying on the
// object reference itself — rather than on the rendered text — means no
// source text is retained as a cache key, and entries disappear for free once
// their owner becomes unreachable (conversation pruned, tab closed, etc.): no
// eviction policy or size cap needed. Values are promises so that concurrent
// renders of the same run (e.g. a message rendering while its sibling is
// still streaming) share one parse.
//
// A single owner can have multiple markdown runs (coalesceContent splits a
// message's content into several adjacent text blocks whenever tool calls
// interleave with prose), so each owner maps to a small Map<runKey, promise>
// rather than a single string. Callers supply a runKey that's stable and
// unique for a given run within that owner (Message.vue uses the
// coalescedContent index).
const cache = new WeakMap<object, Map<string, Promise<string>>>();

export interface MarkdownCacheKey {
  // Object whose lifetime bounds the cache entry.
  owner: object;
  // Distinguishes multiple runs within the same owner.
  runKey: string;
}

// renderMarkdownToSafeHTML parses markdown and returns sanitized HTML.
//
// `messageId` drives the local-image URL rewrite only (see buildMarked above)
// and plays no part in caching. `cacheKey`, when supplied, memoizes the result
// for the lifetime of `cacheKey.owner`; callers whose text can change without
// a new owner — the streaming preview, the distillation preview, export —
// omit it and always re-render.
// renderMarkdownToSafeHTMLSync parses markdown and returns sanitized HTML with
// plain (unhighlighted) code blocks. It is synchronous because the message
// list's geometry must settle in the same tick the component renders:
// asynchronous fills would shift the layout after mount (breaking scroll
// tracking, the bottom-pin, etc.). The async entry point below upgrades this
// to highlighted HTML when the shiki chunk is ready.
export function renderMarkdownToSafeHTMLSync(text: string, messageId?: string): string {
  const marked = buildMarked();
  const tokens = marked.lexer(text);
  decorateTokensSync(tokens as WalkableToken[], messageId);
  return sanitizeMarkdown(marked.parser(tokens), false);
}

// renderMarkdownToSafeHTML parses markdown and returns sanitized HTML, with
// fenced code blocks syntax-highlighted once the lazily-loaded shiki chunk is
// available. The markdown itself is cached, so the plain sync path can upgrade
// in place without re-parsing the source.
//
// `messageId` drives the local-image URL rewrite (both paths) and plays no
// part in caching. `cacheKey`, when supplied, memoizes the result for the
// lifetime of `cacheKey.owner`; callers whose text can change without a new
// owner — the streaming preview, the distillation preview, export — omit it
// and always re-render.
export async function renderMarkdownToSafeHTML(
  text: string,
  messageId?: string,
  cacheKey?: MarkdownCacheKey,
): Promise<string> {
  let runs = cacheKey ? cache.get(cacheKey.owner) : undefined;
  const cached = cacheKey ? runs?.get(cacheKey.runKey) : undefined;
  if (cached) return cached;

  const promise = renderHighlighted(text, messageId);
  if (cacheKey) {
    if (!runs) {
      runs = new Map();
      cache.set(cacheKey.owner, runs);
    }
    runs.set(cacheKey.runKey, promise);
  }
  return promise;
}

async function renderHighlighted(text: string, messageId?: string): Promise<string> {
  const fences = fenceSummary(text);
  // Nothing to highlight (no blocks, or a fence still open mid-stream): the
  // synchronous render is authoritative and costs no double work.
  if (fences.fences === 0 || !fences.closed) {
    return renderMarkdownToSafeHTMLSync(text, messageId);
  }
  try {
    const marked = buildMarked();
    const tokens = marked.lexer(text);
    // Highlighting is asynchronous (it may pull in the shiki chunk), while
    // marked renderers are synchronous, so the code blocks are decorated on
    // the token tree first and the parser renders the stashed output.
    await decorateTokens(tokens as WalkableToken[], messageId, true);
    // Plain blocks (unknown languages, chunk failure) went through the
    // markdown default renderer; nothing shiki emitted can reach the DOM
    // otherwise.
    return sanitizeMarkdown(marked.parser(tokens), true);
  } catch (err) {
    // Never cache a rejection for a (owner, runKey): fall back to the plain
    // render (geometry is already settled from the sync pass) and let the
    // next visit retry.
    console.error("markdown highlight render failed; rendering plain:", err);
    return renderMarkdownToSafeHTMLSync(text, messageId);
  }
}

// decorateTokensSync rewrites local-path image tokens, like decorateTokens,
// without touching code blocks (the synchronous render never highlights).
function decorateTokensSync(tokens: WalkableToken[], messageId: string | undefined): void {
  for (const token of tokens) {
    if (token.type === "image") rewriteImageToken(token as ImageToken, messageId);
    for (const child of childBlocks(token)) {
      decorateTokensSync(child, messageId);
    }
  }
}

function sanitizeMarkdown(raw: string, allowShikiStyles: boolean): string {
  const prev = allowShikiStyleAttrs;
  allowShikiStyleAttrs = allowShikiStyles;
  try {
    return DOMPurify.sanitize(raw, {
      ...SANITIZE_OPTS,
      ...(allowShikiStyles
        ? {
            // Token colors ride on style="--shiki-*" attributes (validated by
            // the hook above), and shiki marks wide blocks tabindex="0" so
            // keyboard users can scroll them horizontally.
            ALLOWED_ATTR: [...SANITIZE_OPTS.ALLOWED_ATTR, "style", "tabindex"],
          }
        : {}),
    });
  } finally {
    allowShikiStyleAttrs = prev;
  }
}
