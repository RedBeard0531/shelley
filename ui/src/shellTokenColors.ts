// Custom shell palette for bash tool cards and bash fenced blocks, applied in
// the highlight worker. The grammar still decides *what* each token is; these
// rules only re-map its colors, in priority order, so bash reads consistently
// across the app while every other language keeps the stock GitHub theme
// untouched.
//
// The palette reuses theme colors where possible: command purple is the
// theme's entity name color, muted gray is the theme's comment color, and
// variable orange is the theme's generic variable color (shellscript pins
// variables to foreground, which is why they need the override). Only two
// hues diverge deliberately: quoted strings/heredocs go green (the stock
// theme lumps them with unquoted args) and &&/;/|| joiners go amber (a
// custom accent).
import type { ThemedTokenWithVariants } from "@shikijs/core";

export type ShellToken = ThemedTokenWithVariants;

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
export function applyShellScopeColors(token: ShellToken): void {
  const scopes = shellTokenScopes(token);
  // Guard against misuse outside the bash grammar: `variable`, `string`,
  // `keyword.operator` etc. are generic scopes other grammars use too, and
  // this palette is only meaningful for shell tokens. (The worker additionally
  // gates the call on the canonical "shellscript" language.)
  if (!scopes.includes("source.shell")) return;
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
