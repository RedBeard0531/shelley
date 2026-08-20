// Real-grammar tests for the shell palette (shellTokenColors.ts). Unlike the
// service-level markdownHighlight tests, this tokenizes with the actual bash
// grammar and exercises applyShellScopeColors over genuine scope output, so
// regressions in scope matching or rule ordering are caught here.
import { createHighlighterCore } from "@shikijs/core";
import { createJavaScriptRegexEngine } from "@shikijs/engine-javascript";
import bash from "@shikijs/langs/bash";
import javascript from "@shikijs/langs/javascript";
import githubDark from "@shikijs/themes/github-dark";
import githubLight from "@shikijs/themes/github-light";
import { applyShellScopeColors } from "./shellTokenColors";

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

const highlighter = await createHighlighterCore({
  langs: [bash, javascript],
  themes: [githubLight, githubDark],
  engine: createJavaScriptRegexEngine(),
});

function shellColors(code: string): Array<{ content: string; light?: string; dark?: string }> {
  const lines = highlighter.codeToTokensWithThemes(code, {
    lang: "bash",
    themes: { light: "github-light", dark: "github-dark" },
    includeExplanation: "scopeName",
  });
  const out: Array<{ content: string; light?: string; dark?: string }> = [];
  for (const line of lines) {
    for (const token of line) {
      applyShellScopeColors(token);
      out.push({ content: token.content, light: token.variants.light?.color, dark: token.variants.dark?.color });
    }
  }
  return out;
}

function hasLight(tokens: Array<{ content: string; light?: string; dark?: string }>, color: string): boolean {
  return tokens.some((t) => t.light === color);
}

function hasDark(tokens: Array<{ content: string; light?: string; dark?: string }>, color: string): boolean {
  return tokens.some((t) => t.dark === color);
}

function colorOf(
  tokens: Array<{ content: string; light?: string; dark?: string }>,
  content: string,
): { light?: string; dark?: string } | undefined {
  // The shell grammar merges whitespace into operator tokens (e.g. " && ").
  return tokens.find((t) => t.content.trim() === content.trim());
}

function lightOf(tokens: Array<{ content: string; light?: string; dark?: string }>, content: string): string | undefined {
  return colorOf(tokens, content)?.light;
}

// ---- Commands, builtins, args, options ----

const basic = shellColors("cd /tmp && ls -la | grep 'foo bar'");
assert(
  hasLight(basic, "#6F42C1") && hasDark(basic, "#B392F0"),
  "commands are purple in both themes (cd/ls/grep)",
);
// builtins join commands rather than staying theme-blue
assert(lightOf(basic, "grep") === "#6F42C1", "grep (support.function.builtin) is purple");
assert(lightOf(basic, "-la") === "#005CC5", "options keep the theme blue (-la)");
assert(basic.some((t) => t.light === "#22863A" && t.dark === "#85E89D"), "quoted strings are green in both themes");
assert(lightOf(basic, "cd") !== "#22863A", "command name is not a string");

// ---- Variables: orange, and they WIN inside double-quoted strings ----

const varDouble = shellColors('echo "$USER"');
assert(
  lightOf(varDouble, "$USER") === "#E36209" && colorOf(varDouble, "$USER")?.dark === "#FFAB70",
  "double-quoted variable is orange in both themes",
);

const varSingle = shellColors("echo '$USER'");
assert(
  lightOf(varSingle, "'$USER'") !== "#E36209",
  "single-quoted text stays a string, not a variable",
);
assert(lightOf(varSingle, "'$USER'") === "#22863A", "single-quoted variable is green");

const escaped = shellColors('echo "a\\"b"');
assert(lightOf(escaped, '\\"') === "#005CC5", "escape keeps the theme blue");
assert(lightOf(escaped, '"a') === "#22863A", "string parts around the escape stay green");

// ---- Heredocs ----

const quotedHeredoc = shellColors("cat <<'EOF'\necho $HOME\nEOF");
assert(
  quotedHeredoc.some((t) => t.content.includes("$HOME") && t.light === "#22863A"),
  "quoted heredoc body does not variable-expand (stays string green)",
);
assert(
  !quotedHeredoc.some((t) => t.light === "#E36209"),
  "quoted heredoc body has no variable-colored tokens",
);

const unquotedHeredoc = shellColors("cat <<EOF\necho $HOME\nEOF");
assert(
  lightOf(unquotedHeredoc, "$HOME") === "#E36209",
  "unquoted heredoc body keeps variables orange",
);

// ---- Separators and joins ----

const joins = shellColors("a && b || c ; d & e");
assert(
  lightOf(joins, "&&") === "#9A6700" &&
    lightOf(joins, "||") === "#9A6700" &&
    lightOf(joins, ";") === "#9A6700",
  "&&, ||, and ; are amber",
);
assert(
  colorOf(joins, "||")?.dark === "#E3B341",
  "|| is amber in dark too",
);
assert(
  lightOf(joins, "&&") !== "#D73A49",
  "&& is no longer operator-red",
);
assert(
  lightOf(joins, "&") === "#6A737D",
  "background & is muted gray",
);

const pipeVsOr = shellColors("a | b || c");
assert(
  lightOf(pipeVsOr, "|") === "#D73A49" && lightOf(pipeVsOr, "||") === "#9A6700",
  "single | stays red while || becomes amber",
);

const caseTerm = shellColors("case x in a) echo a ;; esac");
assert(
  lightOf(caseTerm, ";;") === "#6A737D",
  "case terminator ;; is muted gray",
);

const fallthrough = shellColors("case x in a) echo a ;;& b) echo b ;; esac");
assert(lightOf(fallthrough, ";;") === "#6A737D", "case fallthrough ;; is muted gray");

const fnDef = shellColors("foo() { local v=1; }");
assert(
  lightOf(fnDef, "foo") === "#6F42C1",
  "function definition name is purple",
);
assert(
  lightOf(fnDef, "local") === "#6F42C1",
  "storage modifier (local) joins command purple instead of keyword red",
);

// ---- Non-shell tokens are untouched ----

const jsTokens = highlighter.codeToTokensWithThemes("a && b", {
  lang: "javascript",
  themes: { light: "github-light", dark: "github-dark" },
  includeExplanation: "scopeName",
});
const jsBefore = jsTokens.map((line) =>
  line.map((t) => ({ light: t.variants.light?.color, dark: t.variants.dark?.color })),
);
for (const line of jsTokens) for (const token of line) applyShellScopeColors(token);
const jsAfter = jsTokens.map((line) =>
  line.map((t) => ({ light: t.variants.light?.color, dark: t.variants.dark?.color })),
);
assert(
  JSON.stringify(jsAfter) === JSON.stringify(jsBefore),
  "applyShellScopeColors is a no-op outside the bash grammar (no shell scopes)",
);

console.log(`\nshellTokenColors: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  console.error(`FAILED: ${failed} assertions in shellTokenColors.test.ts`);
  process.exit(1);
}
