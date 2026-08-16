// Syntax highlighting for chat code blocks, built as a separate, lazily-fetched
// chunk.
//
// Markdown rendering is on the critical path of every message, and shiki's
// textmate grammars are large, so this module is deliberately NOT part of the
// main bundle. scripts/build.js emits it as dist/markdown-highlight.js;
// markdownRender.ts dynamic-imports "/markdown-highlight.js" the first time a
// message contains a fenced code block. The one-time fetch/init cost is paid
// then, not on every page load, and subsequent renders are synchronous.
//
// The fine-grained @shikijs/core APIs are used instead of the umbrella `shiki`
// package: that package statically references every bundled grammar (≈1.6MB
// gzipped once bundled), whereas core + explicit imports bundle only the
// languages fenced blocks actually name (≈300KB gzipped for the set below).
import {
  createHighlighterCore,
  flatTokenVariants,
  stringifyTokenStyle,
  type HighlighterCore,
} from "@shikijs/core";
import { createJavaScriptRegexEngine } from "@shikijs/engine-javascript";

import bash from "@shikijs/langs/bash";
import c from "@shikijs/langs/c";
import cpp from "@shikijs/langs/cpp";
import csharp from "@shikijs/langs/csharp";
import css from "@shikijs/langs/css";
import diff from "@shikijs/langs/diff";
import dockerfile from "@shikijs/langs/dockerfile";
import go from "@shikijs/langs/go";
import html from "@shikijs/langs/html";
import ini from "@shikijs/langs/ini";
import java from "@shikijs/langs/java";
import javascript from "@shikijs/langs/javascript";
import jsx from "@shikijs/langs/jsx";
import json from "@shikijs/langs/json";
import kotlin from "@shikijs/langs/kotlin";
import lua from "@shikijs/langs/lua";
import makefile from "@shikijs/langs/makefile";
import markdown from "@shikijs/langs/markdown";
import objectivec from "@shikijs/langs/objective-c";
import php from "@shikijs/langs/php";
import powershell from "@shikijs/langs/powershell";
import python from "@shikijs/langs/python";
import ruby from "@shikijs/langs/ruby";
import rust from "@shikijs/langs/rust";
import shellscript from "@shikijs/langs/shellscript";
import sql from "@shikijs/langs/sql";
import swift from "@shikijs/langs/swift";
import svelte from "@shikijs/langs/svelte";
import toml from "@shikijs/langs/toml";
import tsx from "@shikijs/langs/tsx";
import typescript from "@shikijs/langs/typescript";
import vue from "@shikijs/langs/vue";
import xml from "@shikijs/langs/xml";
import yaml from "@shikijs/langs/yaml";

import githubLight from "@shikijs/themes/github-light";
import githubDark from "@shikijs/themes/github-dark";

// Languages available to chat code fences, in the order the grammars were
// listed above. Loading a language is one per-process cost; the grammars
// themselves are valid for the whole session, so there is no per-render fee.
const LANGS = [
  bash,
  c,
  cpp,
  csharp,
  css,
  diff,
  dockerfile,
  go,
  html,
  ini,
  java,
  javascript,
  jsx,
  json,
  kotlin,
  lua,
  makefile,
  markdown,
  objectivec,
  php,
  powershell,
  python,
  ruby,
  rust,
  shellscript,
  sql,
  swift,
  svelte,
  toml,
  tsx,
  typescript,
  vue,
  xml,
  yaml,
];

const THEMES = [githubLight, githubDark];

// Fences sometimes use a name the grammars don't recognize. Shiki resolves
// many aliases natively from grammar registrations ("js", "sh", "ts", "yml",
// …); this map covers the remaining common spellings that would otherwise
// fall back to plain rendering.
const LANG_ALIASES: Record<string, string> = {
  "c++": "cpp",
  "objective-c": "objectivec",
  jsonc: "json",
  json5: "json",
  md: "markdown",
  py: "python",
  rb: "ruby",
  text: "plaintext",
  txt: "plaintext",
};

// Dual-theme output: `codeToHtml` emits only CSS custom properties
// (--shiki-light / --shiki-dark, plus -bg for the block background),
// resolved against the app's light/.dark mode by styles.css. Using
// defaultColor: false keeps the emitted HTML theme-agnostic, so a cached
// render stays correct when the user switches themes.
function highlightingOptions(lang: string) {
  return {
    lang,
    themes: { light: "github-light", dark: "github-dark" },
    // Dual-theme output: tokens carry only --shiki-* custom properties, which
    // styles.css resolves against the app's light/.dark mode. A cached render
    // therefore stays correct across theme switches.
    defaultColor: false as const,
  };
}

const highlighter: HighlighterCore = await createHighlighterCore({
  langs: LANGS,
  themes: THEMES,
  engine: createJavaScriptRegexEngine(),
});

// Shell-language fence aliases that share the custom shell-card palette.
// Shiki's shellscript grammar registers bash/sh/shell/zsh natively; keep this
// set aligned with that grammar instead of duplicating the alias table.
const SHELL_CODE_LANGUAGES = new Set(["bash", "sh", "shell", "shellscript", "zsh"]);

function isShellCodeLanguage(lang: string): boolean {
  return SHELL_CODE_LANGUAGES.has(lang);
}

/**
 * Highlights one fenced code block for the chat renderer.
 *
 * Returns highlighted `<pre class="shiki ...">` HTML, or null when the
 * language is unknown/unavailable — the caller keeps its plain
 * `<pre><code>` rendering in that case (the code text is unchanged either
 * way; highlighting is purely cosmetic). Note that `text`/`txt` resolve to
 * shiki's plaintext grammar, so literal-language blocks style consistently
 * instead of falling back; truly unknown names return null.
 */
export function highlightCodeBlock(code: string, lang: string): string | null {
  if (!lang) return null;
  const langName = LANG_ALIASES[lang] ?? lang;
  try {
    // Shell-family fences share the tool-card palette so `bash` blocks and
    // executed shell commands read identically.
    if (isShellCodeLanguage(langName)) {
      return highlightShellCodeBlock(code);
    }
    // Drop trailing blank lines: marked's default code renderer validates to
    // one trailing newline, and codeToHtml would otherwise emit a phantom
    // empty .line span that adds bottom padding to the block.
    return highlighter.codeToHtml(code.replace(/\n+$/, ""), highlightingOptions(langName));
  } catch {
    return null;
  }
}

// Inline-token escaping for shell command highlighting. Token `content`
// arrives from the bash grammar; like the block path, it must never smuggle
// tags into the rendered DOM. Style values are also escaped before being
// interpolated into the style attribute (they only ever contain theme hex
// colors/CSS keywords, but the escape keeps the contract explicit).
const INLINE_HTML_ESCAPES: Record<string, string> = {
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  '"': "&quot;",
  "'": "&#39;",
};

function escapeInlineHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => INLINE_HTML_ESCAPES[c]!);
}

type ShellTokenLines = ReturnType<typeof highlighter.codeToTokensWithThemes>;

function tokenizeShell(code: string): ShellTokenLines {
  return highlighter.codeToTokensWithThemes(code, {
    lang: "bash",
    themes: { light: "github-light", dark: "github-dark" },
    includeExplanation: "scopeName",
  });
}

// Light/dark colors for the custom shell-scope overrides below. They mirror
// the GitHub palette the rest of the app already uses; only quoted strings
// diverge from the bundled theme (which lumps them in with unquoted args).
const SHELL_COMMAND_LIGHT = "#6F42C1";
const SHELL_COMMAND_DARK = "#B392F0";
const SHELL_STRING_LIGHT = "#22863A";
const SHELL_STRING_DARK = "#85E89D";
const SHELL_VARIABLE_LIGHT = "#E36209";
const SHELL_VARIABLE_DARK = "#FFAB70";
const SHELL_SEPARATOR_LIGHT = "#9A6700";
const SHELL_SEPARATOR_DARK = "#E3B341";
const SHELL_PUNCTUATION_LIGHT = "#6A737D";
const SHELL_PUNCTUATION_DARK = "#8B949E";

type ShellToken = Parameters<typeof flatTokenVariants>[0];

function shellTokenScopes(token: ShellToken): string[] {
  const scopes: string[] = [];
  for (const explanation of token.explanation ?? []) {
    for (const scope of explanation.scopes) scopes.push(scope.scopeName);
  }
  return scopes;
}

function hasScope(scopes: string[], ...needles: string[]): boolean {
  return scopes.some((scope) =>
    needles.some((needle) => scope === needle || scope.startsWith(`${needle}.`)),
  );
}

function isShellStorageModifier(scope: string): boolean {
  return /^storage\.modifier(\.[^.]+)?\.shell$/.test(scope);
}

function setShellTokenColor(token: ShellToken, light: string, dark: string): void {
  (token.variants.light ||= {}).color = light;
  (token.variants.dark ||= {}).color = dark;
}

/**
 * Rescope the GitHub light/dark theme to the palette the shell tool cards
 * want, while the grammar still decides *what* each token is. Order matters:
 * variables win inside double-quoted strings, escapes keep their built-in
 * color, and only then do strings fall through to green.
 */
function applyShellScopeColors(token: ShellToken): void {
  const scopes = shellTokenScopes(token);
  const text = token.content.trim();

  if (hasScope(scopes, "variable")) {
    setShellTokenColor(token, SHELL_VARIABLE_LIGHT, SHELL_VARIABLE_DARK);
    return;
  }

  if (
    hasScope(
      scopes,
      "entity.name.command",
      "entity.name.function",
      "support.function.builtin",
      "meta.statement.command.name",
    ) ||
    scopes.some(isShellStorageModifier)
  ) {
    setShellTokenColor(token, SHELL_COMMAND_LIGHT, SHELL_COMMAND_DARK);
    return;
  }

  // `;&` is a case fallthrough form. The grammar gives it both the `;`
  // terminator and `&` background scopes, so handle it here before the
  // generic `;` and `&` branches below. (`;;&` tokenizes as `;;` + `&`
  // elsewhere; the `;;` half is handled by the case-terminator branch.)
  if (hasScope(scopes, "punctuation.separator.statement.background") && text.includes(";")) {
    setShellTokenColor(token, SHELL_PUNCTUATION_LIGHT, SHELL_PUNCTUATION_DARK);
    return;
  }

  // `&&` and `;` are conditional/sequential joins. `||` shares the pipe
  // grammar scope with `|`, so only the two-character token moves to amber.
  if (
    hasScope(scopes, "punctuation.separator.statement.and") ||
    hasScope(scopes, "punctuation.terminator.statement.semicolon") ||
    (hasScope(scopes, "keyword.operator.pipe") && text === "||")
  ) {
    setShellTokenColor(token, SHELL_SEPARATOR_LIGHT, SHELL_SEPARATOR_DARK);
    return;
  }

  // `;;` and plain `&` are punctuation, kept visually quieter than the joins.
  if (
    hasScope(scopes, "punctuation.terminator.statement.case") ||
    hasScope(scopes, "punctuation.separator.statement.background")
  ) {
    setShellTokenColor(token, SHELL_PUNCTUATION_LIGHT, SHELL_PUNCTUATION_DARK);
    return;
  }

  if (hasScope(scopes, "constant.character.escape")) return;

  if (
    hasScope(
      scopes,
      "string.quoted.single.shell",
      "string.quoted.double.shell",
      "string.quoted.single.dollar.shell",
      "string.quoted.heredoc",
      "string.unquoted.heredoc",
    )
  ) {
    setShellTokenColor(token, SHELL_STRING_LIGHT, SHELL_STRING_DARK);
  }
}

function renderShellTokenLines(lines: ShellTokenLines): string {
  return lines
    .map((line) =>
      line
        .map((token) => {
          applyShellScopeColors(token);
          // Emit only --shiki-* custom properties (no inline color) so the
          // same HTML resolves in both light and dark app themes, matching
          // highlightCodeBlock's dual-theme contract.
          const flat = flatTokenVariants(token, ["light", "dark"], "--shiki-", false, "css-vars");
          const style = stringifyTokenStyle(flat.htmlStyle ?? {});
          const content = escapeInlineHtml(flat.content);
          return style
            ? `<span class="shiki-token" style="${escapeInlineHtml(style)}">${content}</span>`
            : `<span class="shiki-token">${content}</span>`;
        })
        .join(""),
    )
    .join("\n");
}

/**
 * Highlights a shell command as inline token markup (no <pre>/line wrapper)
 * for the tool cards. Returns null on tokenization failure so callers keep
 * their plain-text rendering.
 */
export function highlightShellCommand(code: string): string | null {
  try {
    // Match fenced shell blocks: trailing blank lines are stripped so a
    // command ending in newline doesn't produce an extra phantom line.
    return renderShellTokenLines(tokenizeShell(code.replace(/\n+$/, "")));
  } catch {
    return null;
  }
}

/**
 * Highlights a fenced shell block with the same palette/token rules as the
 * bash tool cards, but wrapped in the same shape codeToHtml emits for other
 * languages so .markdown-content .shiki styles apply unchanged. That wrapper
 * is deliberately kept byte-parallel with shiki's generic output; if shiki
 * changes its `<pre>`/`<code>` shape this needs the same update.
 */
export function highlightShellCodeBlock(code: string): string | null {
  try {
    const inline = renderShellTokenLines(tokenizeShell(code.replace(/\n+$/, "")));
    const lineHtml = inline
      .split("\n")
      .map((line) => `<span class="line">${line}</span>`)
      .join("\n");
    return (
      `<pre class="shiki shiki-themes github-light github-dark" ` +
      `style="--shiki-light:#24292e;--shiki-dark:#e1e4e8;--shiki-light-bg:#fff;--shiki-dark-bg:#24292e" ` +
      `tabindex="0"><code>${lineHtml}</code></pre>`
    );
  } catch {
    return null;
  }
}
