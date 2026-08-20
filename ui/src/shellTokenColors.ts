// Custom shell palette for bash tool cards and bash fenced blocks, applied in
// the highlight worker. The grammar still decides *what* each token is; these
// rules only re-map its colors, in priority order, so bash reads consistently
// across the app while every other language keeps the stock GitHub theme
// untouched.
//
// The palette reuses theme colors where possible: command purple is the
// theme's entity name color, muted gray is the theme's comment color, and
// variable orange is the theme's generic variable color (shellscript pins
// variables to foreground, which is why they need the override). Only three
// hues diverge deliberately: quoted strings/heredocs go green (the stock
// theme lumps them with unquoted args), &&/;/|| joiners go amber (a custom
// accent), and command-substitution delimiters use the theme's muted
// invalid/error pink rather than disappearing into punctuation gray.
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
const SHELL_SUBSHELL_LIGHT = "#B31D28";
const SHELL_SUBSHELL_DARK = "#FDAEB7";

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

function innermostShellScopes(scopes: string[]): string[] {
  const subshell = scopes.lastIndexOf("meta.scope.subshell");
  return subshell === -1 ? scopes : scopes.slice(subshell);
}

function applyShellScopeColorsToScopes(token: ShellToken, scopes: string[], text: string): void {
  if (!scopes.includes("source.shell")) return;
  scopes = innermostShellScopes(scopes);

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

  // Command-substitution delimiters are punctuation. This must be handled
  // independently for tokens such as `);`, whose grammar explanation has a
  // subshell scope for `)` and a semicolon scope for `;`. The scope list has
  // already been trimmed at the innermost subshell, so an outer string cannot
  // override the delimiter or the syntax inside it.
  if (hasScope(scopes, "punctuation.definition.subshell")) {
    setShellTokenColor(token, SHELL_SUBSHELL_LIGHT, SHELL_SUBSHELL_DARK);
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

/**
 * Rescope the GitHub light/dark theme to the palette the shell tool cards
 * want, while the grammar still decides *what* each token is. Order matters:
 * variables win inside double-quoted strings, escapes keep their built-in
 * color, and only then do strings fall through to green.
 */
export function applyShellScopeColors(token: ShellToken): void {
  applyShellScopeColorsToScopes(token, shellTokenScopes(token), token.content.trim());
}

type ShellExplanation = NonNullable<ShellToken["explanation"]>[number];

function sameScopeNames(left: ShellExplanation, right: ShellExplanation): boolean {
  return (
    left.scopes.length === right.scopes.length &&
    left.scopes.every((scope, index) => scope.scopeName === right.scopes[index]?.scopeName)
  );
}

/**
 * Split a Shiki token at scope transitions before applying the shell palette.
 * Shiki can merge adjacent characters into one token while its explanation
 * still records their individual scopes; using those explanation segments
 * keeps `)` gray and `;` amber in a combined `);` token.
 */
export function shellTokenFragments(token: ShellToken): ShellToken[] {
  const explanations: ShellExplanation[] = [];
  for (const explanation of token.explanation ?? []) {
    const previous = explanations[explanations.length - 1];
    if (previous && sameScopeNames(previous, explanation)) {
      previous.content += explanation.content;
    } else {
      explanations.push({ ...explanation });
    }
  }

  if (explanations.length <= 1) {
    applyShellScopeColors(token);
    return [token];
  }

  const fragments: ShellToken[] = [];
  for (const explanation of explanations) {
    const fragment: ShellToken = {
      ...token,
      content: explanation.content,
      variants: {
        light: { ...token.variants.light },
        dark: { ...token.variants.dark },
      },
      explanation: [explanation],
    };
    applyShellScopeColorsToScopes(
      fragment,
      explanation.scopes.map((scope) => scope.scopeName),
      explanation.content.trim(),
    );

    const previous = fragments[fragments.length - 1];
    if (
      previous &&
      previous.variants.light?.color === fragment.variants.light?.color &&
      previous.variants.light?.fontStyle === fragment.variants.light?.fontStyle &&
      previous.variants.dark?.color === fragment.variants.dark?.color &&
      previous.variants.dark?.fontStyle === fragment.variants.dark?.fontStyle
    ) {
      previous.content += fragment.content;
      previous.explanation = [...(previous.explanation ?? []), explanation];
    } else {
      fragments.push(fragment);
    }
  }
  return fragments;
}
