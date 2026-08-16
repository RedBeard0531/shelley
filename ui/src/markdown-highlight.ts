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
import { createHighlighterCore, type HighlighterCore } from "@shikijs/core";
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
    // Drop trailing blank lines: marked's default code renderer validates to
    // one trailing newline, and codeToHtml would otherwise emit a phantom
    // empty .line span that adds bottom padding to the block.
    return highlighter.codeToHtml(code.replace(/\n+$/, ""), highlightingOptions(langName));
  } catch {
    return null;
  }
}
