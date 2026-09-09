// markdownRender tests: the sanitize pipeline, plus the WeakMap-owner-scoped
// render cache. Run via `pnpm test` (see scripts/run-tests.mjs).
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

const { renderMarkdownToSafeHTML, parseFileRef } = await import("./markdownRender");

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
// non-cache-hit call must go through exactly once.
const origSanitize = DOMPurify.sanitize;
let calls = 0;
function withCallCounting<T>(fn: () => T): T {
  calls = 0;
  DOMPurify.sanitize = ((...args: Parameters<typeof origSanitize>) => {
    calls++;
    return origSanitize(...args);
  }) as typeof DOMPurify.sanitize;
  try {
    return fn();
  } finally {
    DOMPurify.sanitize = origSanitize;
  }
}

// ---- Baseline: rendering + sanitization behavior is unchanged ----

assert(
  renderMarkdownToSafeHTML("# Title\n\nSome **bold** text.").includes("<strong>bold</strong>"),
  "basic markdown renders bold",
);
const fencedCode = renderMarkdownToSafeHTML("```typescript\nconst answer = 42;\n```");
assert(
  fencedCode.includes('<pre><code class="language-typescript">const answer = 42;\n</code></pre>'),
  "fenced code preserves its language class for post-render highlighting",
);
const unknownFencedCode = renderMarkdownToSafeHTML("```unknown-language\nplain text\n```");
assert(
  unknownFencedCode.includes(
    '<pre><code class="language-unknown-language">plain text\n</code></pre>',
  ),
  "unknown fenced code keeps its source and language class for a plain render",
);
assert(
  renderMarkdownToSafeHTML("```\nplain text\n```").includes("<pre><code>plain text\n</code></pre>"),
  "unlabeled fenced code remains plain",
);
assert(
  renderMarkdownToSafeHTML("<script>alert(1)</script>hello").includes("hello") &&
    !renderMarkdownToSafeHTML("<script>alert(1)</script>hello").includes("<script>"),
  "raw script tags are stripped by sanitization",
);

// Local-image rewriting is keyed by messageId, independent of caching.
const withId = renderMarkdownToSafeHTML("![alt](out/plot.png)", "msg-1");
assert(
  withId.includes("/api/message/msg-1/file?path=out%2Fplot.png"),
  "local image rewritten to per-message file endpoint",
);
const withoutId = renderMarkdownToSafeHTML("![alt](out/plot.png)");
assert(!withoutId.includes("<img"), "local image dropped with no messageId to authorize it");

const exeDevLinks = { isExeDev: true, hostname: "demo.exe.xyz" };
const rewrittenMarkdownLink = renderMarkdownToSafeHTML(
  "[Open app](http://localhost:3000/path?q=one#two)",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  rewrittenMarkdownLink.includes('href="https://demo.exe.xyz:3000/path?q=one#two"') &&
    rewrittenMarkdownLink.includes(">Open app</a>"),
  "opted-in markdown links use the public exe.dev URL",
);
const rewrittenAutolink = renderMarkdownToSafeHTML(
  "http://127.0.0.1:4321/live",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  rewrittenAutolink.includes('href="https://demo.exe.xyz:4321/live"') &&
    rewrittenAutolink.includes(">https://demo.exe.xyz:4321/live</a>"),
  "opted-in markdown autolinks rewrite both href and visible URL",
);
const unrewrittenMarkdownLink = renderMarkdownToSafeHTML("[Open app](http://localhost:3000/path)");
assert(
  unrewrittenMarkdownLink.includes('href="http://localhost:3000/path"') &&
    unrewrittenMarkdownLink.includes(">Open app</a>"),
  "markdown links are unchanged without the opt-in",
);
const localURLCode = renderMarkdownToSafeHTML(
  "`http://localhost:3000/inline`\n\n```\nhttp://localhost:3000/fenced\n```",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  localURLCode.includes("<code>http://localhost:3000/inline</code>") &&
    localURLCode.includes("<pre><code>http://localhost:3000/fenced\n</code></pre>") &&
    !localURLCode.includes("demo.exe.xyz"),
  "inline and fenced code URLs are never rewritten",
);
const remoteLocalhostImage = renderMarkdownToSafeHTML(
  "![plot](http://localhost:3000/plot.png)",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  !remoteLocalhostImage.includes("demo.exe.xyz") && !remoteLocalhostImage.includes("<img"),
  "image sources are not rewritten into user-facing links",
);

// ---- Same owner + same runKey: a cache hit, no re-parse ----

{
  const owner = {};
  const text = "# Hello\n\nWorld, with `code` and a [link](https://example.com).";
  let first = "";
  let second = "";
  withCallCounting(() => {
    first = renderMarkdownToSafeHTML(text, "msg-cache-1", { owner, runKey: "0" });
  });
  assert(calls === 1, "first render for a (owner, runKey) parses markdown");
  withCallCounting(() => {
    second = renderMarkdownToSafeHTML(text, "msg-cache-1", { owner, runKey: "0" });
  });
  assert(calls === 0, "second render for the same (owner, runKey) is a cache hit (no re-parse)");
  assert(first === second, "cache hit returns the same HTML string");
}

// ---- Distinct run keys under the same owner: a message with multiple
// markdown runs (coalesceContent splits interleaved text blocks) must not let
// one run's cache entry answer for another's. ----

{
  const owner = {};
  let runA = "";
  let runB = "";
  withCallCounting(() => {
    runA = renderMarkdownToSafeHTML("first run of the message", "msg-multi", {
      owner,
      runKey: "0",
    });
    runB = renderMarkdownToSafeHTML("second run of the same message", "msg-multi", {
      owner,
      runKey: "1",
    });
  });
  assert(calls === 2, "two distinct run keys under one owner each parse once");
  assert(runA !== runB, "distinct run keys render distinct HTML, not one clobbering the other");
  assert(runA.includes("first run"), "run 0's cached entry keeps its own text");
  assert(runB.includes("second run"), "run 1's cached entry keeps its own text");

  withCallCounting(() => {
    const runA2 = renderMarkdownToSafeHTML("first run of the message", "msg-multi", {
      owner,
      runKey: "0",
    });
    const runB2 = renderMarkdownToSafeHTML("second run of the same message", "msg-multi", {
      owner,
      runKey: "1",
    });
    assert(runA2 === runA, "remounting run 0 hits its own cache entry");
    assert(runB2 === runB, "remounting run 1 hits its own cache entry");
  });
  assert(calls === 0, "remounting both runs is served entirely from cache");
}

// ---- Distinct owners: two different Message objects must not share cache
// entries, even under the same runKey — e.g. reopening an old conversation
// (oldest-first) must not collide with a never-evicted newer one. ----

{
  const ownerA = {};
  const ownerB = {};
  const img = "![alt](pic.png)";
  const rA = renderMarkdownToSafeHTML(img, "msg-A", { owner: ownerA, runKey: "0" });
  const rB = renderMarkdownToSafeHTML(img, "msg-B", { owner: ownerB, runKey: "0" });
  assert(
    rA.includes("/api/message/msg-A/file") && !rA.includes("msg-B"),
    "owner A's cached render points at message A's file endpoint",
  );
  assert(
    rB.includes("/api/message/msg-B/file") && !rB.includes("msg-A"),
    "owner B's cached render points at message B's file endpoint (not A's, stale)",
  );

  withCallCounting(() => {
    renderMarkdownToSafeHTML(img, "msg-A", { owner: ownerA, runKey: "0" });
    renderMarkdownToSafeHTML(img, "msg-B", { owner: ownerB, runKey: "0" });
  });
  assert(calls === 0, "both owners' entries are independently cached and both hit");
}

// ---- No owner: never cached, by design. The streaming preview and
// distillation preview call without a cacheKey, so identical text across
// calls must still re-render (it may not be immutable). ----

{
  withCallCounting(() => {
    renderMarkdownToSafeHTML("streaming so far");
    renderMarkdownToSafeHTML("streaming so far");
  });
  assert(calls === 2, "calls without a cacheKey are never cached (each one re-parses)");
}

// ---- endsInOpenFence: the parser tells us when live text ends inside an
// unterminated fence (the streaming case), without a fence-count heuristic. ----

{
  const info = { endsInOpenFence: false };
  const endsOpen = (text: string): boolean => {
    info.endsInOpenFence = false;
    renderMarkdownToSafeHTML(text, undefined, undefined, undefined, info);
    return info.endsInOpenFence;
  };

  assert(endsOpen("```go\nfmt.Println(1)\n```\n") === false, "closed fence is not open");
  assert(endsOpen("```go\nfmt.Println(1)") === true, "unterminated fence is open");
  assert(endsOpen("~~~py\nx = 1") === true, "unterminated tilde fence is open");
  assert(endsOpen("~~~py\nx = 1\n~~~\n") === false, "closed tilde fence is not open");
  assert(
    endsOpen("`````go\nfoo\n```\nbar") === true,
    "a short ``` line inside a 5-backtick fence does not close it",
  );
  assert(
    endsOpen("`````go\nfoo\n`````\n") === false,
    "a same-length backtick line closes a 5-backtick fence",
  );
  assert(
    endsOpen("```go\nfoo\n~~~~\n") === true,
    "a tilde line does not close a backtick fence",
  );
  assert(
    endsOpen("```go\nfmt.Println(1)\n```\nmore prose") === false,
    "text after a closed fence is not open",
  );
  assert(endsOpen("plain prose, no fences") === false, "no fences is not open");
  assert(endsOpen("    indented code\n    more") === false, "indented code is not open");
  assert(
    endsOpen("```\nunlabeled unterminated") === true,
    "unlabeled unterminated fence is still an open fenced block",
  );
  assert(
    endsOpen("> quote\n>\n> ```js\n> x\n> ```\n") === false,
    "closed fence inside a blockquote is not open",
  );
  assert(endsOpen("> ```js\n> x") === true, "unterminated fence inside a blockquote is open");
}

// ---- File references (`path:line` inline code -> clickable anchor) ----

{
  // parseFileRef: accepted shapes.
  const single = parseFileRef("ui/src/App.vue:100");
  assert(single?.path === "ui/src/App.vue" && single?.line === 100 && single?.endLine === 100,
    "path:line parses");
  const range = parseFileRef("ui/src/App.vue:100-140");
  assert(range?.line === 100 && range?.endLine === 140, "path:start-end parses");
  assert(parseFileRef("src/app.ts")?.path === "src/app.ts" && parseFileRef("src/app.ts")?.line === undefined,
    "bare path parses without a line");
  assert(parseFileRef("./.gitignore")?.path === "./.gitignore", "explicitly relative dotfile parses");
  assert(parseFileRef("./Makefile:10")?.path === "./Makefile", "explicitly relative extensionless name parses");
  assert(parseFileRef("cmd/server")?.path === "cmd/server", "directory-shaped path parses");
  assert(parseFileRef("~/.config/shelley/AGENTS.md")?.path === "~/.config/shelley/AGENTS.md",
    "home-relative path parses");
  assert(parseFileRef("~/.config/shelley/AGENTS.md:12-20")?.endLine === 20,
    "home-relative path with range parses");
  assert(parseFileRef("~tim/x:2") === null, "~user path is not a reference");
  assert(parseFileRef("a~b/c") === null, "mid-path tilde is not a reference");
  assert(parseFileRef("src/app.ts:" + "9".repeat(400)) === null,
    "overflow line number is not a reference");

  // parseFileRef: rejections. The single shape rule is "path contains a
  // slash" (the prompt says to write top-level files as ./name), which
  // rejects every slash-less lookalike in one stroke.
  assert(parseFileRef("npm run build") === null, "whitespace is not a reference");
  assert(parseFileRef("golang") === null, "bare identifier is not a reference");
  assert(parseFileRef("127.0.0.1:8080") === null, "IP:port is not a reference");
  assert(parseFileRef("localhost:3000") === null, "host:port is not a reference");
  assert(parseFileRef("example.com:8080") === null, "domain:port is not a reference");
  assert(parseFileRef("v1.2.3") === null, "dotted version is not a reference");
  assert(parseFileRef("12:30") === null, "time is not a reference");
  assert(parseFileRef("data[10:20]") === null, "slice is not a reference");
  assert(parseFileRef("Makefile") === null, "slash-less top-level name is not a reference");
  assert(parseFileRef("AGENTS.md") === null, "slash-less top-level file is not a reference");
  assert(parseFileRef("ui/src/") === null, "trailing slash (directory) is not a reference");
  assert(parseFileRef("~") === null, "bare tilde is not a reference");
  assert(parseFileRef("~/") === null, "bare tilde-slash is not a reference");
  assert(parseFileRef("src/app.ts:0") === null, "non-positive line is not a reference");
  assert(parseFileRef("src/app.ts:5-0") === null, "non-positive range end is not a reference");

  // Rendering: refs become anchors with data attributes.
  const singleHtml = renderMarkdownToSafeHTML("See `ui/src/App.vue:100` here.");
  assert(singleHtml.includes('class="file-ref"'), "reference renders as a file-ref anchor");
  assert(singleHtml.includes('data-file-path="ui/src/App.vue"'), "reference carries the path");
  assert(singleHtml.includes('data-line="100"'), "reference carries the line");
  assert(!singleHtml.includes("data-end-line"), "single-line reference has no end line");
  assert(singleHtml.includes('ui/src/App.vue:100</a>'), "reference label is the original span text");
  assert(!singleHtml.includes('target="_blank"'), "file references are not new-tab links");

  const rangeHtml = renderMarkdownToSafeHTML("`ui/src/App.vue:100-140`");
  assert(rangeHtml.includes('data-line="100"') && rangeHtml.includes('data-end-line="140"'),
    "range reference carries both ends");

  const bareHtml = renderMarkdownToSafeHTML("`src/app.ts`");
  assert(bareHtml.includes('data-file-path="src/app.ts"') && !bareHtml.includes("data-line"),
    "bare path is a reference without line attributes");

  // Non-references keep the default code-span rendering (and links keep
  // their new-tab behavior).
  assert(renderMarkdownToSafeHTML("`npm run build`").includes("<code>npm run build</code>"),
    "non-reference spans stay plain code");
  assert(renderMarkdownToSafeHTML("`v1.2.3`").includes("<code>v1.2.3</code>"),
    "dotted versions stay plain code");
  const linkHtml = renderMarkdownToSafeHTML("[x](https://example.com)");
  assert(linkHtml.includes('target="_blank"'), "ordinary links still open in new tabs");

  // Forged anchors: raw HTML claiming the file-ref affordance is removed
  // unless its attributes form a well-formed reference (the renderer emits
  // nothing else, so any survivor must parse).
  const forged = renderMarkdownToSafeHTML(
    '<a href="#" data-file-path="localhost:3000" data-line="1">pwn</a>',
  );
  assert(!forged.includes("data-file-path"), "forged anchor with non-reference path is removed");
  const forgedZero = renderMarkdownToSafeHTML(
    '<a href="#" data-file-path="ui/src/App.vue" data-line="0">pwn</a>',
  );
  assert(!forgedZero.includes("data-file-path"), "forged anchor with zero line is removed");
  const forgedPadded = renderMarkdownToSafeHTML(
    '<a href="#" data-file-path="ui/src/App.vue" data-line="007">pwn</a>',
  );
  assert(!forgedPadded.includes("data-file-path"), "forged anchor with padded line is removed");
  const forgedSuffix = renderMarkdownToSafeHTML(
    '<a href="#" data-file-path="a/b:3" data-line="999" data-end-line="1">pwn</a>',
  );
  assert(!forgedSuffix.includes("data-file-path"),
    "forged anchor whose path attr carries a :line suffix is removed");
  const overflow = renderMarkdownToSafeHTML("`src/app.ts:" + "9".repeat(400) + "`");
  assert(overflow.includes("<code>src/app.ts:"), "overflow line number stays plain code");
  const forgedLine = renderMarkdownToSafeHTML(
    '<a href="#" data-file-path="ui/src/App.vue" data-line="abc">pwn</a>',
  );
  assert(!forgedLine.includes("data-file-path"), "forged anchor with non-numeric line is removed");
  const legit = renderMarkdownToSafeHTML(
    '<a href="#" class="file-ref" data-file-path="ui/src/App.vue" data-line="5">ui/src/App.vue:5</a>',
  );
  assert(legit.includes('data-file-path="ui/src/App.vue"') && legit.includes('data-line="5"'),
    "well-formed ref anchor survives sanitization");
  const hrefHijack = renderMarkdownToSafeHTML(
    '<a href="https://evil.com" target="_blank" data-file-path="ui/src/App.vue" data-line="5">ui/src/App.vue:5</a>',
  );
  assert(hrefHijack.includes('data-file-path="ui/src/App.vue"') && !hrefHijack.includes('evil.com')
    && !hrefHijack.includes('target="_blank"') && hrefHijack.includes('href="#"'),
    "contract-valid forged anchor gets a neutral href, no target");
  const lyingLabel = renderMarkdownToSafeHTML(
    '<a href="#" data-file-path="ui/src/App.vue" data-line="5">ui/src/App.vue:999</a>',
  );
  assert(!lyingLabel.includes("data-file-path"),
    "anchor whose target disagrees with its displayed reference is removed");
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
