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

// ---- Standalone inline-code URLs are links, not commands or code blocks ----

for (const url of [
  "https://phil-dev.exe.xyz:3979/",
  "http://localhost:8000/path?q=one&other=two#fragment",
  "https://example.com/path_(with_parentheses)?q=a,b!",
  'https://example.com/?literal=&amp;&quote="value"',
  "https://example.com/?q=<script>alert(1)</script>",
  "HTTPS://EXAMPLE.COM/path",
  "http://[::1]:8000/",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(`Preview: \`${url}\``));
  const link = root.querySelector("a");
  assert(link?.getAttribute("href") === url, `inline-code URL is linked intact: ${url}`);
  assert(
    link?.querySelector("code")?.textContent === url,
    "linked URL keeps code markup and literal text",
  );
  assert(
    link?.getAttribute("target") === "_blank" &&
      link?.getAttribute("rel") === "noopener noreferrer",
    "inline-code link has the same new-tab protections as other links",
  );
  assert(!root.querySelector("script"), "URL content cannot inject HTML");
}

for (const code of [
  "const answer = 42;",
  "curl https://example.com",
  "https://example.com --flag",
  "https://one.example https://two.example",
  "javascript:alert(1)",
  "data:text/html,<script>alert(1)</script>",
  "file:///tmp/example.txt",
  "ftp://example.com/file",
  "//example.com/path",
  "https://",
  "https://example.com:invalid/",
  "https://[invalid]/",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(`\`${code}\``));
  assert(!root.querySelector("a"), `non-URL code is not linked: ${code}`);
  assert(root.querySelector("code")?.textContent === code, "non-URL code is unchanged");
}

for (const markdown of [
  "```\nhttps://example.com/fenced\n```",
  "```text\nhttps://example.com/fenced\n```",
  "    https://example.com/indented",
  "<pre><code>https://example.com/raw-block</code></pre>",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(markdown));
  assert(!!root.querySelector("pre > code"), "code block is preserved");
  assert(!root.querySelector("a"), "code block URLs are not linked");
}

for (const markdown of [
  "[`https://example.com/label`](https://example.com/destination)",
  "[**`https://example.com/label`**](https://example.com/destination)",
  '<a href="https://example.com/destination">`https://example.com/label`</a>',
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(markdown));
  assert(
    root.querySelectorAll("a").length === 1,
    "code inside an existing link gets no nested link",
  );
  assert(
    root.querySelector("a")?.getAttribute("href") === "https://example.com/destination" &&
      root.querySelector("a code")?.textContent === "https://example.com/label",
    "explicit link destination and code label are preserved",
  );
}

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
  localURLCode.includes('href="https://demo.exe.xyz:3000/inline"') &&
    localURLCode.includes("<code>https://demo.exe.xyz:3000/inline</code>") &&
    localURLCode.includes("<pre><code>http://localhost:3000/fenced\n</code></pre>"),
  "standalone inline-code URLs use the opt-in localhost rewrite, but fenced code is unchanged",
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

// ---- File references (`📄path:line` inline code -> clickable chip) ----

// The marker that makes an inline-code span a file reference.
const M = "📄";

// References are opt-in per render (agent-authored text in a host with an
// editor), so the tests say so explicitly, as MarkdownContent does.
const render = (text: string) =>
  renderMarkdownToSafeHTML(text, undefined, undefined, undefined, undefined, true);

{
  // parseFileRef: accepted shapes. Every reference is marked, and the parser is
  // also what reads a chip's label back, so these shapes are the contract.
  const single = parseFileRef(`${M}ui/src/App.vue:100`);
  assert(single?.path === "ui/src/App.vue" && single?.line === 100 && single?.endLine === 100,
    "marked path:line parses");
  const range = parseFileRef(`${M}ui/src/App.vue:100-140`);
  assert(range?.line === 100 && range?.endLine === 140, "marked path:start-end parses");
  assert(parseFileRef(`${M}src/app.ts`)?.path === "src/app.ts"
    && parseFileRef(`${M}src/app.ts`)?.line === undefined,
    "marked path parses without a line");
  assert(parseFileRef(`${M}./.gitignore`)?.path === "./.gitignore", "marked dotfile parses");
  assert(parseFileRef(`${M}Makefile:10`)?.path === "Makefile",
    "marked slash-less name parses: the marker, not the slash, is what makes it a reference");
  assert(parseFileRef(`${M}~/.config/shelley/AGENTS.md:12-20`)?.endLine === 20,
    "marked home-relative path with range parses");
  assert(parseFileRef(`${M}src/app.ts:140-100`)?.line === 100, "reversed range is normalized");
  assert(parseFileRef(`${M}~tim/x:2`) === null, "~user path is not a reference");
  assert(parseFileRef(`${M}a~b/c`) === null, "mid-path tilde is not a reference");
  assert(parseFileRef(`${M}src/app.ts:` + "9".repeat(400)) === null,
    "overflow line number is not a reference");

  // The canonical form has nothing between the marker and the path, but a space
  // (or trailing whitespace) is tolerated.
  assert(parseFileRef(`${M} src/app.ts:7`)?.line === 7, "space after the marker is tolerated");
  assert(parseFileRef(`${M}src/app.ts:7 `)?.line === 7, "trailing space is tolerated");
  assert(parseFileRef(`${M}\uFE0Fsrc/app.ts:5`)?.path === "src/app.ts",
    "a variation selector after the marker is the same marker");

  // parseFileRef: rejections. Unmarked spans are never references, however
  // path-shaped they look — that is the entire point of the marker.
  assert(parseFileRef("ui/src/App.vue:100") === null, "unmarked path:line is not a reference");
  assert(parseFileRef("ui/src/App.vue") === null, "unmarked path is not a reference");
  assert(parseFileRef("npm run build") === null, "whitespace is not a reference");
  assert(parseFileRef("localhost:3000") === null, "host:port is not a reference");

  // ...and a marked span must still be path-shaped.
  assert(parseFileRef(`${M}`) === null, "a bare marker is not a reference");
  assert(parseFileRef(`${M} `) === null, "marker plus whitespace is not a reference");
  assert(parseFileRef(`${M}npm run build`) === null, "marked command is not a reference");
  assert(parseFileRef(`${M}src/app.ts:0`) === null, "non-positive line is not a reference");
  assert(parseFileRef(`${M}ui/src/`) === null, "trailing slash (directory) is not a reference");
  assert(parseFileRef(` ${M}src/app.ts`) === null, "marker must open the span");
  assert(parseFileRef(`📎src/app.ts`) === null, "only the marker emoji marks a reference");

  // Paths are a plain allowlist: letters in any script and the punctuation real
  // filenames use. Everything else — spaces, quotes, invisible runes — ends the
  // reference, which is what keeps a chip's text equal to the path it opens.
  for (const [span, path] of [
    [`${M}node_modules/@types/node/index.d.ts:5`, "node_modules/@types/node/index.d.ts"],
    [`${M}a+b.ts:5`, "a+b.ts"],
    [`${M}café.ts`, "café.ts"],
    [`${M}cafe\u0301.ts`, "cafe\u0301.ts"],
    [`${M}दस्तावेज़.md`, "दस्तावेज़.md"],
    [`${M}[id].vue`, "[id].vue"],
    [`${M}file(1).ts`, "file(1).ts"],
    [`${M}./x:1-2`, "./x"],
    [`${M}src/with-hyphen_and.dot`, "src/with-hyphen_and.dot"],
  ] as const) {
    assert(parseFileRef(span)?.path === path, `path parses: ${span}`);
  }
  for (const span of [`${M}src/with space.ts:3`, `${M}a\u200Bb.ts:2`, `${M}a\u202Eb.txt:2`,
                      `${M}a'b.ts`, `${M}a&b.ts`, `${M}a:b.ts`, `${M}${M}a.ts`] as const) {
    assert(parseFileRef(span) === null, `not a reference: ${span}`);
  }

  // A `..` segment may lead, where the reader sees it, but never sit inside the
  // path: a chip's text is meant to name what it opens, and a traversal hidden
  // behind a prefix the reader can see is the one way to break that.
  assert(parseFileRef(`${M}../shared/x.ts`)?.path === "../shared/x.ts", "a leading .. is allowed");
  assert(parseFileRef(`${M}docs/../x.ts`) === null, "an interior .. is refused");
  assert(parseFileRef(`${M}docs/../../etc/hostname:1`) === null, "an interior traversal is refused");
  assert(parseFileRef(`${M}..`) === null, "`..` alone is a directory, not a reference");
  assert(parseFileRef(`${M}x/.`) === null, "a trailing /. is a directory, not a reference");

  // Rendering: a marked span becomes a chip whose text is the reference.
  const singleHtml = render(`See \`${M}ui/src/App.vue:100\` here.`);
  assert(singleHtml.includes('class="file-ref"'), "reference renders as a chip");
  assert(singleHtml.includes(`>${M}ui/src/App.vue:100</a>`),
    "the chip carries the reference in its own text");
  assert(!singleHtml.includes("data-file-path"), "the chip carries no reference attributes");
  assert(!singleHtml.includes('target="_blank"'), "chips are not new-tab links");
  assert(singleHtml.includes('href="#"'), "the chip href is inert");

  const rangeHtml = render(`\`${M}ui/src/App.vue:100-140\``);
  assert(rangeHtml.includes(`>${M}ui/src/App.vue:100-140</a>`), "a range renders as written");

  const bareHtml = render(`\`${M}src/app.ts\``);
  assert(bareHtml.includes(`>${M}src/app.ts</a>`), "a bare path renders without a line");

  // A reference is displayed in canonical form, matching what clicking it opens.
  assert(render(`\`${M}src/app.ts:007\``).includes(`>${M}src/app.ts:7</a>`),
    "a padded line number displays canonically");
  assert(render(`\`${M}src/app.ts:140-100\``).includes(`>${M}src/app.ts:100-140</a>`),
    "a reversed range displays in the order it opens");

  // Unmarked spans keep the default code-span rendering, whatever they look
  // like — the false-positive regression that motivated the marker.
  for (const code of ["ui/src/App.vue:100", "src/app.ts", "npm run build", "v1.2.3",
                      "localhost:3000", "data[10:20]", "read/write"]) {
    const html = render(`\`${code}\``);
    assert(html.includes(`<code>${code}</code>`), `unmarked ${code} stays plain code`);
    assert(!html.includes("file-ref"), `unmarked ${code} is not a chip`);
  }
  assert(render("`📎src/app.ts`").includes("<code>"), "another emoji does not mark a reference");

  // A host that does not render references (tool output, a fetched page, the
  // export preview) gets plain code — which is also the default.
  const noRefs = renderMarkdownToSafeHTML(`\`${M}src/app.ts:5\``);
  assert(!noRefs.includes("file-ref") && noRefs.includes(`<code>${M}src/app.ts:5</code>`),
    "without refs, a marked span stays plain code");

  // Raw HTML can produce a chip (the class is not a secret) and that is fine:
  // the handler reads the label, so a chip's text is its target and it cannot
  // point somewhere else. What it cannot do is navigate — the href is inert.
  const forged = render(`<a class="file-ref" href="https://evil.example">${M}/etc/passwd:1</a>`);
  assert(forged.includes(`>${M}/etc/passwd:1</a>`), "a forged chip keeps its own label");
  assert(forged.includes('href="#"') && !forged.includes("evil.example"), "its href is inert");
  assert(!forged.includes('target="_blank"'), "and it gets no new-tab target");
  const linked = render(`[\`${M}src/app.ts:5\`](https://example.com)`);
  assert(!/<a[^>]*><\/a>/.test(linked), "a link whose label was a reference leaves no empty anchor");
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
