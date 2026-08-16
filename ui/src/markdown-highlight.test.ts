// Tests for the lazily-loaded syntax-highlighting chunk (markdown-highlight.ts).
// Unlike markdownRender.test.ts (which substitutes a stub chunk to test the
// pipeline), this exercises the REAL shiki grammar set, so regressions in
// language loading or alias resolution are caught here. Run via `pnpm test`.
import { highlightCodeBlock, highlightShellCommand } from "./markdown-highlight";

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

// ---- Shell command highlighting for tool cards ----

const shell = highlightShellCommand("cd /tmp && ls -la | grep 'foo bar'");
assert(shell !== null, "shell command highlights");
assert(shell?.includes("--shiki-light:#6F42C1") ?? false, "command tokens use purple in light");
assert(shell?.includes("--shiki-dark:#B392F0") ?? false, "command tokens use purple in dark");
assert(shell?.includes("--shiki-light:#22863A") ?? false, "quoted strings use green in light");
assert(shell?.includes("--shiki-dark:#85E89D") ?? false, "quoted strings use green in dark");
assert(shell?.includes("--shiki-light:#005CC5") ?? false, "options keep their blue in light");
assert(shell?.includes("cd") ?? false, "command text is preserved");
assert(shell?.includes("<span") ?? false, "shell command renders inline token spans");
assert(!(shell?.includes("<pre") ?? false), "shell command has no <pre> wrapper");

const varDouble = highlightShellCommand('echo "$USER"');
assert(
  varDouble?.includes("--shiki-light:#E36209") ?? false,
  "double-quoted variables use muted orange in light",
);
assert(
  varDouble?.includes("--shiki-dark:#FFAB70") ?? false,
  "double-quoted variables use muted orange in dark",
);

const varSingle = highlightShellCommand("echo '$USER'");
assert(
  varSingle !== null && !varSingle.includes("--shiki-light:#E36209"),
  "single-quoted variable stays a string, not a variable",
);
assert(
  varSingle?.includes("--shiki-light:#22863A") ?? false,
  "single-quoted string still gets string green",
);

const escaped = highlightShellCommand('echo "a\\"b"');
assert(
  escaped !== null &&
    escaped.includes("--shiki-light:#005CC5") &&
    escaped.includes("--shiki-light:#22863A"),
  "escaped quotes keep escape blue while surrounding string parts are green",
);

const quotedHeredoc = highlightShellCommand("cat <<'EOF'\necho $HOME\nEOF");
assert(
  quotedHeredoc?.includes("--shiki-light:#22863A") ?? false,
  "quoted heredoc body uses string green in light",
);
assert(
  quotedHeredoc !== null && !quotedHeredoc.includes("--shiki-light:#E36209"),
  "quoted heredoc body does not variable-expand",
);

const unquotedHeredoc = highlightShellCommand("cat <<EOF\necho $HOME\nEOF");
assert(
  unquotedHeredoc?.includes("--shiki-light:#22863A") ?? false,
  "unquoted heredoc body text uses string green in light",
);
assert(
  unquotedHeredoc?.includes("--shiki-light:#E36209") ?? false,
  "unquoted heredoc body keeps variables orange",
);

const joinSeparators = highlightShellCommand("a && b || c ; d ; e & f");
assert(
  joinSeparators?.includes("--shiki-light:#9A6700") ?? false,
  "&&, ||, and ; use amber in light",
);
assert(
  joinSeparators?.includes("--shiki-dark:#E3B341") ?? false,
  "&&, ||, and ; use amber in dark",
);
assert(
  joinSeparators !== null && !joinSeparators.includes("--shiki-light:#D73A49"),
  "&& and || no longer share the pipe-red operator color",
);
assert(
  joinSeparators?.includes("--shiki-light:#6A737D") ?? false,
  "background & uses muted punctuation in light",
);
assert(
  joinSeparators?.includes("--shiki-dark:#8B949E") ?? false,
  "background & uses muted punctuation in dark",
);

const pipeVsOr = highlightShellCommand("a | b || c");
assert(
  (pipeVsOr?.includes("--shiki-light:#D73A49") ?? false) &&
    (pipeVsOr?.includes("--shiki-light:#9A6700") ?? false),
  "single | stays red while || becomes amber",
);

const caseTerminator = highlightShellCommand("case x in a) echo a ;; esac");
assert(
  caseTerminator?.includes("--shiki-light:#6A737D") ?? false,
  "case terminator ;; uses muted punctuation",
);

const shellEscaped = highlightShellCommand("printf '<script>alert(1)</script>'");
assert(
  shellEscaped !== null && !shellEscaped.includes("<script") && shellEscaped.includes("&lt;script"),
  "shell command text is escaped, so highlighted HTML cannot smuggle tags",
);

// ---- Shell-family fenced code blocks share the shell-card palette ----

const shellBlock = highlightCodeBlock("a && b || c ; d & e\necho done", "bash");
assert(shellBlock !== null, "bash fence highlights");
assert(shellBlock?.includes('class="shiki') ?? false, "bash fence carries the shiki class");
assert(shellBlock?.includes('<span class="line">') ?? false, "bash fence keeps line wrappers");
assert(shellBlock?.includes("--shiki-light:#9A6700") ?? false, "bash fence separators use amber");
assert(shellBlock?.includes("--shiki-light:#6A737D") ?? false, "bash fence background & is muted");
assert(shellBlock?.includes("--shiki-light:#6F42C1") ?? false, "bash fence commands stay purple");

const shAliasBlock = highlightCodeBlock("echo ok", "sh");
assert(shAliasBlock?.includes("--shiki-light:#6F42C1") ?? false, "sh fence uses shell palette");

const nonShellBlock = highlightCodeBlock("a && b || c", "javascript");
assert(
  nonShellBlock !== null && !nonShellBlock.includes("--shiki-light:#9A6700"),
  "non-shell fence keeps the generic code-block path",
);

console.log(`\nmarkdown-highlight: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  console.error(`FAILED: ${failed} assertions in markdown-highlight.test.ts`);
  process.exit(1);
}
