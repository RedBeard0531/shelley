// Tests for the lazily-loaded syntax-highlighting chunk (markdown-highlight.ts).
// Unlike markdownRender.test.ts (which substitutes a stub chunk to test the
// pipeline), this exercises the REAL shiki grammar set, so regressions in
// language loading or alias resolution are caught here. Run via `pnpm test`.
import { highlightCodeBlock } from "./markdown-highlight";

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

// ---- Happy path: real grammars highlight into shiki's dual-theme markup ----

const js = highlightCodeBlock("const x = 'hi';\n// comment", "javascript");
assert(js !== null, "javascript block highlights");
assert(js?.includes('class="shiki') ?? false, "highlighted block carries the shiki class");
assert(js?.includes("--shiki-light") ?? false, "light-theme token color emitted");
assert(js?.includes("--shiki-dark") ?? false, "dark-theme token color emitted");
assert(
  (js?.includes("--shiki-light-bg:#fff") && js.includes("--shiki-dark-bg")) ?? false,
  "block background colors emitted for both themes",
);
assert(js?.includes("const") ?? false, "code text is preserved");
assert(
  js?.includes("'hi'") ?? false,
  "code text preserved verbatim (shiki only escapes &, < and quotes it must)",
);

// ---- Aliases (native grammar aliases + our supplemental map) ----

for (const lang of ["js", "sh", "yml", "c++", "py", "md", "ts"]) {
  const out = highlightCodeBlock("x = 1", lang);
  assert((out !== null && out.includes('class="shiki')) ?? false, `alias '${lang}' resolves`);
}

// ---- Behavior contract with the renderer ----

assert(highlightCodeBlock("x", "") === null, "empty language renders plain");
assert(
  highlightCodeBlock("x", "definitely-not-a-language") === null,
  "unknown language renders plain",
);
const scripty = highlightCodeBlock("let s = '<script>alert(1)</script>';", "js");
assert(
  scripty !== null && !scripty.includes("<script") && scripty.includes("&#x3C;script"),
  "code text is escaped, so highlighted HTML cannot smuggle tags",
);

// Trailing blank lines in the fenced body must not produce a stray empty line.
const trimmed = highlightCodeBlock("a\nb\n\n\n", "go");
assert(
  (trimmed?.match(/class="line"/g) ?? []).length === 2,
  "trailing newlines are stripped before highlighting (no phantom empty line)",
);

console.log(`\nmarkdown-highlight: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  console.error(`FAILED: ${failed} assertions in markdown-highlight.test.ts`);
  process.exit(1);
}
