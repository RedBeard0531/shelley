// markdownRender tests: the sanitize pipeline, syntax-highlighting
// integration, plus the WeakMap-owner-scoped render cache. Run via
// `pnpm test` (see scripts/run-tests.mjs).
//
// DOMPurify needs a real `window`/`document` in Node (it auto-detects the
// browser global otherwise). Set that up before importing markdownRender, the
// same way ansi.test.ts does.
import { JSDOM } from "jsdom";
import DOMPurify from "dompurify";

const dom = new JSDOM("");
const g = globalThis as Record<string, unknown>;
g.window = dom.window;
g.document = dom.window.document;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const purify = DOMPurify(dom.window as any);
Object.assign(DOMPurify, purify);

const {
  renderMarkdownToSafeHTML,
  renderMarkdownToSafeHTMLSync,
  setMarkdownHighlightURLForTests,
  resetMarkdownHighlightURLForTests,
} = await import("./markdownRender");

// The production pipeline dynamic-imports the built chunk
// (/markdown-highlight.js), which doesn't exist under the unit test runner.
// Substitute a tiny stand-in that mimics the real module's contract (return
// highlighted HTML or null) so the pipeline itself can be tested without
// building dist/. The shipped chunk is covered separately by
// markdown-highlight.test.ts.
const STUB_HIGHLIGHT_URL =
  "data:text/javascript," +
  encodeURIComponent(`
export function highlightCodeBlock(code, lang) {
  if (!lang || lang === "nope") return null;
  const esc = code.replace(/&/g, "&amp;").replace(/</g, "&lt;");
  return '<pre class="shiki shiki-themes github-light github-dark" style="--shiki-light:#24292e;--shiki-dark:#e1e4e8;--shiki-light-bg:#fff;--shiki-dark-bg:#24292e"><code><span style="--shiki-light:#D73A49;--shiki-dark:#F97583">' + esc + '</span></code></pre>';
}
  `);
// A module whose import rejects (simulating a chunk that failed to load).
const FAILING_HIGHLIGHT_URL = "data:text/javascript,throw new Error%28%22boom%22%29";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string): void {
  if (cond) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// Count actual parse+sanitize invocations via DOMPurify.sanitize, which every
// non-cache-hit call must go through exactly once. Async: the call is made
// when the returned promise runs, so fn is awaited inside.
const origSanitize = DOMPurify.sanitize;
let calls = 0;
async function withCallCounting<T>(fn: () => Promise<T>): Promise<T> {
  calls = 0;
  DOMPurify.sanitize = ((...args: Parameters<typeof origSanitize>) => {
    calls++;
    return origSanitize(...args);
  }) as typeof DOMPurify.sanitize;
  try {
    return await fn();
  } finally {
    DOMPurify.sanitize = origSanitize;
  }
}

// ---- Baseline: rendering + sanitization behavior is unchanged ----

assert(
  (await renderMarkdownToSafeHTML("# Title\n\nSome **bold** text.")).includes(
    "<strong>bold</strong>",
  ),
  "basic markdown renders bold",
);
{
  const html = await renderMarkdownToSafeHTML("<script>alert(1)</script>hello");
  assert(html.includes("hello") && !html.includes("<script"), "raw script tags are stripped");
}

// Local-image rewriting is keyed by messageId, independent of caching.
const withId = await renderMarkdownToSafeHTML("![alt](out/plot.png)", "msg-1");
assert(
  withId.includes("/api/message/msg-1/file?path=out%2Fplot.png"),
  "local image rewritten to per-message file endpoint",
);
const withoutId = await renderMarkdownToSafeHTML("![alt](out/plot.png)");
assert(!withoutId.includes("<img"), "local image dropped with no messageId to authorize it");

// ---- Syntax highlighting: fences become shiki blocks, styles are allowlisted ----

setMarkdownHighlightURLForTests(STUB_HIGHLIGHT_URL);
try {
  {
    // Balanced fence + language → shiki markup, and the emitted --shiki-*
    // style attributes survive sanitization.
    const html = await renderMarkdownToSafeHTML('before\n\n```go\nfmt.Println("hi")\n```\n\nafter');
    assert(html.includes('class="shiki'), "fenced code block is highlighted");
    assert(
      html.includes("--shiki-light:#D73A49") && html.includes("--shiki-dark:#F97583"),
      "token color custom properties survive sanitization",
    );
    assert(html.includes("--shiki-dark-bg:#24292e"), "block background colors survive");
    assert(html.includes("fmt.Println"), "code text preserved");
  }

  {
    // Unclosed fence (mid-stream): plain rendering, no shiki markup.
    const html = await renderMarkdownToSafeHTML('```go\nfmt.Println("hi")');
    assert(!html.includes("shiki"), "unclosed fence defers highlighting");
    assert(html.includes('<pre><code class="language-go">'), "unclosed fence renders plain");
  }

  {
    // Unknown language: chunk contract says null → plain fallback, same as
    // pre-highlighting output (body normalized to one trailing newline).
    const html = await renderMarkdownToSafeHTML("```nope\nx = 1\n```");
    assert(!html.includes("shiki"), "unknown language renders plain");
    assert(
      html.includes('<pre><code class="language-nope">x = 1\n</code></pre>'),
      "plain fallback matches marked's default output",
    );
  }

  {
    // Raw HTML with a non-shiki style attribute is stripped (models can't
    // smuggle arbitrary CSS through the new style allowlist).
    const html = await renderMarkdownToSafeHTML(
      '<div style="position:fixed;left:0;top:0">x</div> <p style="color:red">y</p>',
    );
    assert(
      !html.includes("position:fixed") && !html.includes("color:red"),
      "non-shiki styles stripped",
    );
    assert(html.includes(">x</div>"), "element content kept");
  }

  {
    // Shiki-looking but invalid style values are still rejected.
    const html = await renderMarkdownToSafeHTML(
      '<span style="--shiki-light:url(javascript:alert(1))">x</span>',
    );
    assert(!html.includes("url("), "style values must be plain hex colors");
  }

  {
    // Single-quoted style attributes must be stripped too: the highlighted
    // render path has to allow the style attribute at all, and the
    // pre-sanitize filter is the only gate against raw-HTML styles.
    const html = await renderMarkdownToSafeHTML(
      "<div style='position:fixed;left:0;top:0'>x</div>\n\n```go\ny\n```",
    );
    assert(!html.includes("position:fixed"), "single-quoted non-shiki styles stripped");
    assert(html.includes(">x</div>"), "single-quoted style element content kept");
  }

  {
    // Unquoted and uppercase style attributes must be stripped too: DOMPurify
    // normalizes them before the style allowlist hook runs, but they still
    // must not survive into the DOM with a closeable fence present.
    const html = await renderMarkdownToSafeHTML(
      "<div style=position:fixed;z-index:9999>x</div> <div STYLE='color:red'>y</div>\n\n```go\nz\n```",
    );
    assert(
      !html.includes("position:fixed") && !html.includes("z-index"),
      "unquoted style stripped",
    );
    assert(!html.includes("color:red"), "uppercase STYLE stripped");
    assert(html.includes(">x</div>") && html.includes(">y</div>"), "elements kept");
  }

  {
    // Emphasis claims on tokens (italic/bold) arrive as -font-* custom
    // properties and must survive alongside the color.
    setMarkdownHighlightURLForTests(
      "data:text/javascript," +
        encodeURIComponent(
          'export function highlightCodeBlock(code, lang) { return \'<pre class="shiki" style="--shiki-light:#fff;--shiki-dark:#000"><code><span style="--shiki-light:#222;--shiki-dark:#ccc;--shiki-light-font-style:italic;--shiki-dark-font-weight:bold">x</span></code></pre>\'; }',
        ),
    );
    const em = await renderMarkdownToSafeHTML("```go\nx\n```", "msg-em", {
      owner: {},
      runKey: "0",
    });
    assert(
      em.includes("--shiki-light-font-style:italic") &&
        em.includes("--shiki-dark-font-weight:bold"),
      "emphasis custom properties survive sanitization",
    );
    resetMarkdownHighlightURLForTests();
    setMarkdownHighlightURLForTests(STUB_HIGHLIGHT_URL);
  }

  {
    // Nested blocks: code fences and local images inside list items and
    // table cells must be decorated like top-level ones (marked stores list
    // items under `items` and table cells under `header`/`rows`).
    const html = await renderMarkdownToSafeHTML(
      "- item\n\n  ```go\n  fmt.Println(1)\n  ```\n\n- ![plot](out/a.png)\n\n| col |\n| --- |\n| ![t](out/b.png) |",
      "msg-nested",
    );
    assert(html.includes('class="shiki'), "code fence inside a list item is highlighted");
    assert(
      html.includes("/api/message/msg-nested/file?path=out%2Fa.png"),
      "local image inside a list item is rewritten",
    );
    assert(
      html.includes("/api/message/msg-nested/file?path=out%2Fb.png"),
      "local image inside a table cell is rewritten",
    );
    assert(!html.includes('src="out'), "no unreachable local image href remains unstripped");
  }

  {
    // Unhighlighted fallback path still escapes code text.
    const html = await renderMarkdownToSafeHTML("```go\nif a < b { x }\n```", "msg-fallback", {
      owner: {},
      runKey: "0",
    });
    assert(html.includes("&lt;") && !html.includes("if a < b {"), "plain code block is escaped");
  }

  // Chunk load failure degrades to plain rendering, not a crash.
  resetMarkdownHighlightURLForTests();
  setMarkdownHighlightURLForTests(FAILING_HIGHLIGHT_URL);
  {
    const html = await renderMarkdownToSafeHTML("```go\nx = 1\n```");
    assert(!html.includes("shiki"), "unloadable chunk falls back to plain code");
    assert(html.includes("<pre><code"), "plain fallback still renders the block");
  }
} finally {
  resetMarkdownHighlightURLForTests();
}

// ---- Synchronous path: layout-critical plain render never highlights ----

setMarkdownHighlightURLForTests(STUB_HIGHLIGHT_URL);
try {
  const sync = renderMarkdownToSafeHTMLSync(
    '```go\nfmt.Println("hi")\n```\n\n![p](out/x.png)',
    "msg-sync",
  );
  assert(
    !sync.includes("shiki"),
    "sync render keeps plain code blocks (layout settles immediately)",
  );
  assert(
    sync.includes('<pre><code class="language-go">'),
    "sync render still emits marked's default block",
  );
  assert(
    sync.includes("/api/message/msg-sync/file?path=out%2Fx.png"),
    "sync render still rewrites local images",
  );
} finally {
  resetMarkdownHighlightURLForTests();
}

// ---- Same owner + same runKey: a cache hit, no re-parse ----

{
  const owner = {};
  const text = "# Hello\n\nWorld, with `code` and a [link](https://example.com).";
  let first = "";
  let second = "";
  await withCallCounting(async () => {
    first = await renderMarkdownToSafeHTML(text, "msg-cache-1", { owner, runKey: "0" });
  });
  assert(calls === 1, "first render for a (owner, runKey) parses markdown");
  await withCallCounting(async () => {
    second = await renderMarkdownToSafeHTML(text, "msg-cache-1", { owner, runKey: "0" });
  });
  assert(calls === 0, "second render for the same (owner, runKey) is a cache hit (no re-parse)");
  assert(first === second, "cache hit returns the same HTML");
}

// ---- Distinct run keys under the same owner ----

{
  const owner = {};
  let runA = "";
  let runB = "";
  await withCallCounting(async () => {
    runA = await renderMarkdownToSafeHTML("first run of the message", "msg-multi", {
      owner,
      runKey: "0",
    });
    runB = await renderMarkdownToSafeHTML("second run of the same message", "msg-multi", {
      owner,
      runKey: "1",
    });
  });
  assert(calls === 2, "two distinct run keys under one owner each parse once");
  assert(runA !== runB, "distinct run keys render distinct HTML, not one clobbering the other");
  assert(runA.includes("first run"), "run 0's cached entry keeps its own text");
  assert(runB.includes("second run"), "run 1's cached entry keeps its own text");

  await withCallCounting(async () => {
    const runA2 = await renderMarkdownToSafeHTML("first run of the message", "msg-multi", {
      owner,
      runKey: "0",
    });
    const runB2 = await renderMarkdownToSafeHTML("second run of the same message", "msg-multi", {
      owner,
      runKey: "1",
    });
    assert(runA2 === runA, "remounting run 0 hits its own cache entry");
    assert(runB2 === runB, "remounting run 1 hits its own cache entry");
  });
  assert(calls === 0, "remounting both runs is served entirely from cache");
}

// ---- Distinct owners must not share cache entries ----

{
  const ownerA = {};
  const ownerB = {};
  const img = "![alt](pic.png)";
  const rA = await renderMarkdownToSafeHTML(img, "msg-A", { owner: ownerA, runKey: "0" });
  const rB = await renderMarkdownToSafeHTML(img, "msg-B", { owner: ownerB, runKey: "0" });
  assert(
    rA.includes("/api/message/msg-A/file") && !rA.includes("msg-B"),
    "owner A's cached render points at message A's file endpoint",
  );
  assert(
    rB.includes("/api/message/msg-B/file") && !rB.includes("msg-A"),
    "owner B's cached render points at message B's file endpoint (not A's, stale)",
  );

  await withCallCounting(async () => {
    await renderMarkdownToSafeHTML(img, "msg-A", { owner: ownerA, runKey: "0" });
    await renderMarkdownToSafeHTML(img, "msg-B", { owner: ownerB, runKey: "0" });
  });
  assert(calls === 0, "both owners' entries are independently cached and both hit");
}

// ---- No owner: never cached, by design ----

{
  await withCallCounting(async () => {
    await renderMarkdownToSafeHTML("streaming so far");
    await renderMarkdownToSafeHTML("streaming so far");
  });
  assert(calls === 2, "calls without a cacheKey are never cached (each one re-parses)");
}

console.log(`\nmarkdownRender: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  console.error(`FAILED: ${failed} assertions in markdownRender.test.ts`);
  process.exit(1);
}
