// Pure logic for the context composition view: reconstructs what each LLM
// call's context was made of, per category.
//
// Provider-reported numbers are exact; byte counts only set ratios:
//   - The prompt size at call i is input + cache_creation + cache_read (the
//     cache split is page-quantized but the sum is not).
//   - The assistant message generated at call i-1 costs exactly output_tokens
//     when it enters the context at call i; reported reasoning_tokens splits
//     thinking from text+args.
//   - Everything else that entered between calls (user messages, tool
//     results) totals promptDelta - output_{i-1}, split by byte ratio.
//   - Fixed request overhead invisible to messages (tool definitions, base
//     framing) is measured once per generation — at its first call — as the
//     residual, and folded into the system band.
// chars/4 byte estimates only fill in where reported numbers are missing.
// Each message's contribution is determined once, when it enters the
// context, and never rescaled: later points only add.
import type { LLMContent, Message } from "../types";

export type Composition = Record<string, number>;
export type ToolBreakdown = Record<string, Record<string, number>>;

export interface CompositionPoint {
  /** Provider-reported context: prompt plus this call's output. */
  total: number;
  generation: number;
  /** Category -> tokens; sums to total when attribution is possible. */
  parts: Composition;
  /** Tool-argument tokens per tool category (subset of parts). */
  args: Composition;
  toolBreakdown: ToolBreakdown;
  model?: string;
}

const TYPE_TEXT = 2;
const TYPE_THINKING = 3;
const TYPE_TOOL_USE = 5;
const TYPE_TOOL_RESULT = 6;
const TYPE_WEB_SEARCH_TOOL_RESULT = 8;

// Tool-argument bytes are recorded under this suffix during the walk, then
// split out into CompositionPoint.args so the breakdown table can show args
// vs. output per tool category.
const ARGS_SUFFIX = "\u0000args";

// Storage strips image bytes from llm_data, so byte-proportional splits would
// give images ~1 token. Seed each image with a rough mid-size screenshot
// allowance; the group total stays exact either way, only the split moves.
const IMAGE_SEED_BYTES = 6000;

const TYPE_EXCLUDED = ["gitinfo", "modelchange", "error"];

function contributesToContext(message: Message) {
  return !!message.llm_data && !TYPE_EXCLUDED.includes(message.type);
}

interface Usage {
  input_tokens?: number;
  cache_creation_input_tokens?: number;
  cache_read_input_tokens?: number;
  output_tokens?: number;
  reasoning_tokens?: number;
}

function parseUsage(message: Message): Usage | null {
  if (!message.usage_data) return null;
  try {
    return typeof message.usage_data === "string"
      ? JSON.parse(message.usage_data)
      : message.usage_data;
  } catch {
    return null;
  }
}

/** Reported prompt size: the cache split is page-quantized, the sum is not. */
function promptSize(usage: Usage) {
  return (
    (usage.input_tokens || 0) +
    (usage.cache_creation_input_tokens || 0) +
    (usage.cache_read_input_tokens || 0)
  );
}

/** Category -> bytes. Categories match the rendered bands; tool-argument
 *  bytes land under `<category>${ARGS_SUFFIX}`. */
type Group = Record<string, number>;
/** Tool category -> tool name -> bytes, for the breakdown table. */
type Breakdown = Record<string, Record<string, number>>;

function isToolCategory(key: string) {
  return key.startsWith("bash:") || key.startsWith("tool:") || key.startsWith("repo/");
}

function addGroupBytes(group: Group, key: string, n: number) {
  group[key] = (group[key] || 0) + n;
}

function addBreakdownBytes(breakdown: Breakdown, key: string, detail: string, n: number) {
  if (!isToolCategory(key)) return;
  const details = (breakdown[key] ||= {});
  details[detail] = (details[detail] || 0) + n;
}

const bashCommandIntent = (name: string | undefined, input: unknown): { key: string; detail: string } => {
  if (name !== "bash") {
    let key: string;
    switch (name) {
      case "browser":
      case "web_search":
      case "keyword_search":
      case "WebSearch":
      case "WebFetch":
        key = "tool:browser/web";
        break;
      case "apply_patch":
      case "patch":
      case "write_file":
        key = "repo/edit";
        break;
      default:
        key = "tool:other";
    }
    return { key, detail: name || "other" };
  }
  const intent = bashCommandIntentDetails(commandFromInput(input));
  return {
    key: intent.family.startsWith("repo/") ? intent.family : `bash:${intent.family}`,
    detail: intent.command,
  };
};

function commandFromInput(input: unknown) {
  if (typeof input === "object" && input && "command" in input && typeof input.command === "string") {
    return input.command;
  }
  if (typeof input !== "string") return "other";
  try {
    const parsed = JSON.parse(input);
    return typeof parsed?.command === "string" ? parsed.command : input;
  } catch {
    return input;
  }
}

function bashCommandIntentDetails(command: string): { family: string; command: string } {
  for (const invocation of bashCommandInvocations(command)) {
    if (invocation.name === "git") {
      const subcommand = gitSubcommand(invocation.args);
      return {
        family: isReadOnlyGitCommand(subcommand, invocation.args) ? "repo/read" : "repo/edit",
        command: subcommand ? `git ${subcommand}` : "git",
      };
    }
    const family = bashCommandFamily(invocation.name);
    if (family) return { family, command: invocation.name };
  }
  return { family: "other", command: bashCommandInvocations(command)[0]?.name || "shell" };
}

function bashCommandFamily(name: string): string | null {
  if (["rm", "mkdir", "gofmt", "chmod", "mv", "cp", "touch", "ln", "install", "patch"].includes(name))
    return "repo/edit";
  if (["rg", "grep", "find", "fd", "ag", "ack"].includes(name)) return "code search";
  if (
    [
      "cat", "sed", "head", "tail", "awk", "ls", "pwd", "less", "more", "tree", "stat", "file",
      "readlink", "realpath", "wc", "cut", "sort", "uniq", "column", "diff", "strings",
    ].includes(name)
  )
    return "file read";
  if (
    [
      "go", "pnpm", "npm", "yarn", "make", "cargo", "pytest", "jest", "vitest", "bun", "uv",
      "ruff", "mypy", "eslint", "tsc", "biome", "gradle", "mvn",
    ].includes(name)
  )
    return "build/test";
  if (["python", "python3", "node", "ruby", "perl", "sqlite3", "psql", "mysql", "jq", "yq"].includes(name))
    return "script/query";
  if (
    [
      "tmux", "curl", "wget", "df", "du", "ss", "systemctl", "journalctl", "ps", "pgrep",
      "pkill", "kill", "lsof", "ip", "netstat", "ping", "dig", "nslookup", "hostname", "uname",
      "whoami", "date", "uptime", "free", "which", "whereis",
    ].includes(name)
  )
    return "system";
  return null;
}

interface CommandInvocation {
  name: string;
  args: string[];
}

function bashCommandInvocations(command: string): CommandInvocation[] {
  return command
    .trim()
    .split(/&&|\|\||;|\n/)
    .flatMap((segment) => {
      const words = segment.trim().split(/\s+/).filter(Boolean);
      const invocation = executableInvocation(words);
      return invocation ? [invocation] : [];
    });
}

function executableInvocation(words: string[]): CommandInvocation | null {
  let index = 0;
  while (index < words.length) {
    const word = commandBasename(words[index]);
    if (!word || /^[A-Za-z_][A-Za-z0-9_]*=/.test(word) || /^[0-9]*[<>]/.test(word)) {
      index++;
      continue;
    }
    if (["cd", "export", "set", "true", ":", ".", "source", "if", "then", "fi", "for", "do", "done", "while", "case", "esac", "{", "}"].includes(word)) return null;
    if (!["env", "command", "exec", "timeout", "time", "nice", "nohup", "sudo"].includes(word)) {
      return { name: word, args: words.slice(index + 1) };
    }
    index = skipWrapper(words, index + 1, word);
  }
  return null;
}

function gitSubcommand(args: string[]) {
  let index = 0;
  while (index < args.length) {
    const arg = args[index].replace(/^['"]|['"]$/g, "");
    if (!arg.startsWith("-")) return arg;
    if (!arg.includes("=") && ["-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env"].includes(arg)) {
      index = skipShellArgument(args, index + 1);
    } else {
      index++;
    }
  }
  return "";
}

function skipShellArgument(words: string[], index: number) {
  let singleQuoted = false;
  let doubleQuoted = false;
  for (; index < words.length; index++) {
    const word = words[index];
    for (let i = 0; i < word.length; i++) {
      if (word[i] === "'" && !doubleQuoted) singleQuoted = !singleQuoted;
      else if (word[i] === '"' && !singleQuoted && word[i - 1] !== "\\") doubleQuoted = !doubleQuoted;
    }
    if (!singleQuoted && !doubleQuoted) return index + 1;
  }
  return index;
}

function isReadOnlyGitCommand(subcommand: string, args: string[]) {
  if (
    [
      "status", "diff", "show", "log", "grep", "blame", "shortlog", "describe", "rev-parse",
      "rev-list", "ls-files", "ls-tree", "cat-file", "name-rev", "for-each-ref", "show-ref",
      "reflog",
    ].includes(subcommand)
  )
    return true;
  if (subcommand === "branch")
    return args.some((arg) =>
      ["--show-current", "--list", "--contains", "--no-contains", "--merged", "--no-merged"].includes(arg),
    );
  if (subcommand === "remote")
    return args.some((arg) => ["-v", "--verbose", "show", "get-url"].includes(arg));
  if (subcommand === "config")
    return args.some((arg) => ["--get", "--get-all", "--get-regexp", "--list", "-l"].includes(arg));
  if (subcommand === "tag") return args.some((arg) => ["--list", "-l"].includes(arg));
  return false;
}

function commandBasename(word: string) {
  return word.replace(/^['"]|['"]$/g, "").split("/").at(-1) || "";
}

function skipWrapper(words: string[], index: number, wrapper: string) {
  while (index < words.length) {
    const word = words[index];
    if (word === "--") return index + 1;
    if (wrapper === "env" && /^[A-Za-z_][A-Za-z0-9_]*=/.test(word)) {
      index++;
      continue;
    }
    if (word.startsWith("-")) {
      index++;
      if (!word.includes("=") && wrapperOptionNeedsValue(wrapper, word)) index++;
      continue;
    }
    if (wrapper === "timeout") return index + 1;
    return index;
  }
  return index;
}

function wrapperOptionNeedsValue(wrapper: string, option: string) {
  if (wrapper === "sudo")
    return ["-u", "-g", "-h", "-C", "-r", "-t", "--user", "--group", "--host", "--close-from", "--role", "--type", "--chdir"].includes(option);
  if (wrapper === "env") return ["-u", "-C", "--unset", "--chdir"].includes(option);
  if (wrapper === "timeout") return ["-k", "--kill-after"].includes(option);
  if (wrapper === "nice") return ["-n", "--adjustment"].includes(option);
  return wrapper === "time" && ["-f", "-o"].includes(option);
}

const enc = new TextEncoder();
const byteLength = (value: string) => (value ? enc.encode(value).length : 0);
const stringifyValue = (value: unknown) =>
  typeof value === "string" ? value : (JSON.stringify(value) ?? "");

/** Accumulate one content block's bytes into a group. Tool_use IDs map to
 *  their category so the following tool_result lands on the same band. */
function addContentBytes(
  group: Group,
  breakdown: Breakdown,
  toolKeys: Map<string, { key: string; detail: string }>,
  content: LLMContent,
  fallback: { key: string; detail?: string },
): void {
  if (content.MediaType || content.DisplayImageURL || content.Data) {
    addGroupBytes(group, "images", IMAGE_SEED_BYTES);
    return;
  }
  switch (content.Type) {
    case TYPE_TOOL_USE: {
      const attribution = bashCommandIntent(content.ToolName, content.ToolInput);
      toolKeys.set(content.ID, attribution);
      const n =
        byteLength(content.ToolName || "") +
        byteLength(stringifyValue(content.ToolInput));
      addGroupBytes(group, attribution.key + ARGS_SUFFIX, n);
      addBreakdownBytes(breakdown, attribution.key, attribution.detail, n);
      return;
    }
    case TYPE_TOOL_RESULT:
    case TYPE_WEB_SEARCH_TOOL_RESULT: {
      const attribution =
        toolKeys.get(content.ToolUseID || "") ||
        (content.Type === TYPE_WEB_SEARCH_TOOL_RESULT
          ? { key: "tool:browser/web", detail: "web_search" }
          : { key: "tool:other", detail: "other" });
      for (const result of content.ToolResult || [])
        addContentBytes(group, breakdown, toolKeys, result, attribution);
      return;
    }
    case TYPE_TEXT: {
      const n = byteLength(content.Text || content.Thinking || "");
      addGroupBytes(group, fallback.key, n);
      addBreakdownBytes(breakdown, fallback.key, fallback.detail || "", n);
      return;
    }
    case TYPE_THINKING: {
      const n = byteLength(content.Text || content.Thinking || "");
      // Signed blocks are the ones requests drop once a newer assistant turn
      // exists; the attribution loop tracks that bucket for the strip
      // correction and for replace-instead-of-accumulate semantics.
      if (content.Signature) {
        addGroupBytes(group, "signedThinking", n);
      } else {
        const key = isToolCategory(fallback.key) ? fallback.key : "reasoning";
        addGroupBytes(group, key, n);
        addBreakdownBytes(breakdown, key, isToolCategory(fallback.key) ? fallback.detail || "" : "reasoning", n);
      }
      return;
    }
    default: {
      const n = byteLength(content.Text || "");
      addGroupBytes(group, fallback.key, n);
      addBreakdownBytes(breakdown, fallback.key, fallback.detail || "", n);
    }
  }
}

interface TurnInfo {
  prompt: number;
  output: number;
  /** Reported reasoning tokens for this call's own output. */
  reasoning: number;
  /** Thinking blocks carry signatures: the provider drops this turn's
   *  thinking from the request once a newer assistant turn exists. */
  signed: boolean;
}

const groupBytes = (group: Group) => Object.values(group).reduce((sum, n) => sum + n, 0);

/** Distribute a group's token total over its byte buckets. `tokens` is the
 *  exact total when reported numbers anchor it, else buckets fall back to
 *  chars/4. */
function attributeGroup(
  running: Composition,
  runningBreakdown: ToolBreakdown,
  group: Group,
  breakdown: Breakdown,
  tokens: number | null,
): void {
  const totalBytes = groupBytes(group);
  const rate = tokens != null && totalBytes > 0 ? tokens / totalBytes : 1 / 4;
  for (const [key, bytes] of Object.entries(group)) {
    const n = tokens != null ? bytes * rate : Math.ceil(bytes / 4);
    addTokens(running, key, n);
    const details = breakdown[key];
    if (details) {
      const target = (runningBreakdown[key] ||= {});
      for (const [name, detailBytes] of Object.entries(details)) {
        target[name] = (target[name] || 0) + (tokens != null ? detailBytes * rate : Math.ceil(detailBytes / 4));
      }
    }
  }
}

/** Attribute an assistant turn's exact size: reported reasoning tokens split
 *  thinking from text+args exactly; the rest divides by byte ratio. Returns
 *  the tokens given to the signed-thinking bucket (it leaves the request at
 *  the next call once a newer assistant turn exists). */
function attributeAssistantGroup(
  running: Composition,
  runningBreakdown: ToolBreakdown,
  group: Group,
  breakdown: Breakdown,
  exactTotal: number,
  reasoningExact: number | null,
): number {
  const thinkKey = group.signedThinking != null ? "signedThinking" : "reasoning";
  const thinkBytes = group[thinkKey] || 0;
  if (thinkBytes <= 0 || reasoningExact == null || reasoningExact > exactTotal) {
    attributeGroup(running, runningBreakdown, group, breakdown, exactTotal);
    return 0;
  }
  addTokens(running, thinkKey, reasoningExact);
  const restGroup: Group = { ...group };
  delete restGroup[thinkKey];
  const restBreakdown: Breakdown = {};
  for (const [key, details] of Object.entries(breakdown)) if (key !== thinkKey) restBreakdown[key] = details;
  attributeGroup(running, runningBreakdown, restGroup, restBreakdown, exactTotal - reasoningExact);
  return thinkKey === "signedThinking" ? reasoningExact : 0;
}

function addTokens(running: Composition, key: string, tokens: number) {
  running[key] = (running[key] || 0) + tokens;
}

/** Fold `<category>ARGS_SUFFIX` buckets into their category totals and
 *  surface the argument shares as the args breakdown. */
function mergeArgs(parts: Composition): { parts: Composition; args: Composition } {
  const args: Composition = {};
  const merged: Composition = {};
  for (const [key, value] of Object.entries(parts)) {
    if (key.endsWith(ARGS_SUFFIX)) {
      const base = key.slice(0, -ARGS_SUFFIX.length);
      args[base] = (args[base] || 0) + value;
      merged[base] = (merged[base] || 0) + value;
    } else {
      merged[key] = (merged[key] || 0) + value;
    }
  }
  const finalParts: Composition = {};
  for (const [key, value] of Object.entries(merged)) if (value > 0) finalParts[key] = Math.round(value);
  const finalArgs: Composition = {};
  for (const [key, value] of Object.entries(args)) if (value > 0) finalArgs[key] = Math.round(value);
  return { parts: finalParts, args: finalArgs };
}

function finalizeBreakdown(breakdown: ToolBreakdown): ToolBreakdown {
  const out: ToolBreakdown = {};
  for (const [key, details] of Object.entries(breakdown)) {
    const rounded: Record<string, number> = {};
    for (const [name, tokens] of Object.entries(details)) if (tokens > 0) rounded[name] = Math.round(tokens);
    if (Object.keys(rounded).length > 0) out[key] = rounded;
  }
  return out;
}

/** Build the per-call composition points for a conversation.
/** Build the per-call composition points for a conversation.
 *
 * Within a generation the walk is purely additive: each call attributes the
 * content that entered the context since the previous call — the previous
 * assistant message at its exact reported output size, everything else at
 * the exact prompt delta minus that output — and earlier bands never move.
 * Fixed request overhead (tool definitions, framing) is measured once, at
 * the generation's first call, and folded into the system band. Compactions
 * reset the reconstruction (a new generation legitimately drops history);
 * model switches keep it and only skip the cross-model delta, since the new
 * provider re-tokenizes the whole prompt. */
export function buildCompositionPoints(messages: Message[]): CompositionPoint[] {
  const points: CompositionPoint[] = [];
  let running: Composition = {};
  let runningBreakdown: ToolBreakdown = {};
  const toolKeys = new Map<string, { key: string; detail: string }>();
  let lastSystemMessage: Message | null = null;
  let generation: number | undefined;
  let fold: number | null = null;
  let systemBytes = 0;
  let asstGroup: Group = {};
  let asstBreakdown: Breakdown = {};
  let asstSigned = false;
  let userGroup: Group = {};
  let userBreakdown: Breakdown = {};
  let prevCall: TurnInfo | null = null;
  let turnN2: { reasoning: number; signed: boolean } | null = null;
  let pendingSignedReasoning = 0;
  let prevModel: string | undefined;

  const addMessageBytes = (group: Group, breakdown: Breakdown, message: Message) => {
    if (!contributesToContext(message)) return;
    try {
      const llm =
        typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
      const fallback = {
        key: message.type === "user" ? "user" : message.type === "system" ? "system" : "assistant",
      };
      for (const content of (llm?.Content || []) as LLMContent[])
        addContentBytes(group, breakdown, toolKeys, content, fallback);
    } catch {
      // Malformed historic payloads stay visible but cannot contribute.
    }
  };

  // Compaction: history legitimately drops, so restart the reconstruction —
  // including the system prompt, which is re-sent on every call but stored
  // only in the conversation's first generation. Its bytes are held aside
  // from step attribution and sized at chars/4 plus the fixed-overhead fold.
  const resetForGeneration = () => {
    running = {};
    runningBreakdown = {};
    toolKeys.clear();
    fold = null;
    systemBytes = 0;
    asstGroup = {};
    asstBreakdown = {};
    asstSigned = false;
    userGroup = {};
    userBreakdown = {};
    prevCall = null;
    turnN2 = null;
    pendingSignedReasoning = 0;
    if (lastSystemMessage) {
      const group: Group = {};
      addMessageBytes(group, {}, lastSystemMessage);
      systemBytes = group.system || 0;
    }
  };

  for (const message of messages) {
    if (message.type === "system") lastSystemMessage = message;
    if (generation !== undefined && message.generation !== generation) resetForGeneration();
    generation = message.generation;

    if (message.type !== "agent") {
      addMessageBytes(userGroup, userBreakdown, message);
      continue;
    }

    // Agent message: a call. Parse its own content first — it is the group
    // entering the context at the NEXT call, and its reported output anchors
    // this point's provisional newest-assistant attribution.
    const usage = parseUsage(message);
    const nextGroup: Group = {};
    const nextBreakdown: Breakdown = {};
    addMessageBytes(nextGroup, nextBreakdown, message);
    const nextSigned = (nextGroup.signedThinking || 0) > 0;

    const prompt = usage ? promptSize(usage) : 0;
    const output = usage ? usage.output_tokens || 0 : 0;
    const total = prompt + output;
    if (!usage || total <= 0) {
      // No usable numbers for this call: its content still enters the
      // context, so carry the bytes into the next step's entering group.
      for (const [key, bytes] of Object.entries(nextGroup)) addGroupBytes(userGroup, key, bytes);
      for (const [key, details] of Object.entries(nextBreakdown))
        for (const [name, n] of Object.entries(details))
          addBreakdownBytes(userBreakdown, key, name, n);
      continue;
    }

    const model = message.model_name || undefined;
    const modelChanged = !!model && !!prevModel && model !== prevModel;
    const reasoningExact =
      (usage.reasoning_tokens || 0) > 0 && (usage.reasoning_tokens || 0) <= output
        ? (usage.reasoning_tokens || 0)
        : null;
    const signedBefore = running.signedThinking || 0;

    if (prevCall && !modelChanged) {
      // Signed thinking of the turn before the previous one leaves the
      // request at this call.
      running.signedThinking = Math.max(0, (running.signedThinking || 0) - pendingSignedReasoning);
      pendingSignedReasoning = 0;

      const strip = turnN2?.signed ? turnN2.reasoning : 0;
      const delta = prompt > 0 && prevCall.prompt > 0 ? prompt - prevCall.prompt : null;
      const asstTotal = prevCall.output > 0 ? prevCall.output : null;
      const userTotal =
        delta != null && asstTotal != null ? Math.max(0, delta - asstTotal + strip) : null;
      const combined = asstTotal == null && delta != null ? delta : null;

      if (combined != null) {
        // Output size unreported: one shared rate for everything entering.
        const allGroup: Group = { ...asstGroup };
        for (const [key, bytes] of Object.entries(userGroup)) addGroupBytes(allGroup, key, bytes);
        const allBreakdown: Breakdown = {};
        for (const source of [asstBreakdown, userBreakdown])
          for (const [key, details] of Object.entries(source))
            for (const [name, n] of Object.entries(details))
              addBreakdownBytes(allBreakdown, key, name, n);
        attributeGroup(running, runningBreakdown, allGroup, allBreakdown, combined);
      } else {
        attributeAssistantGroup(
          running,
          runningBreakdown,
          asstGroup,
          asstBreakdown,
          asstTotal!,
          prevCall.reasoning > 0 ? prevCall.reasoning : null,
        );
        attributeGroup(running, runningBreakdown, userGroup, userBreakdown, userTotal);
      }
    } else if (fold == null) {
      // First usable call of the generation: everything not estimated at
      // chars/4 is fixed request overhead — tool definitions and framing —
      // measured once and folded into the system band.
      fold = Math.max(0, prompt - (groupBytes(userGroup) + systemBytes) / 4);
      attributeGroup(running, runningBreakdown, userGroup, userBreakdown, null);
    } else {
      // Model switch: the new provider re-tokenizes the whole prompt, so the
      // cross-model delta is meaningless; fall back to chars/4 for this step
      // and let the overhead band carry the re-tokenization shift.
      attributeGroup(running, runningBreakdown, asstGroup, asstBreakdown, null);
      attributeGroup(running, runningBreakdown, userGroup, userBreakdown, null);
    }
    pendingSignedReasoning = (running.signedThinking || 0) - signedBefore;

    const parts: Composition = { ...running };
    // The signed-thinking bucket is the newest turn's share the request
    // still carries; it renders as the reasoning band.
    parts.reasoning = (parts.reasoning || 0) + (running.signedThinking || 0);
    delete parts.signedThinking;
    parts.system = (parts.system || 0) + systemBytes / 4 + (fold || 0);

    // Provisional newest-assistant attribution: this call's output enters
    // the prompt at the next call with exactly these tokens.
    if (output > 0) {
      const thinkBytes = nextGroup.signedThinking || nextGroup.reasoning || 0;
      const reasoning =
        reasoningExact ??
        (groupBytes(nextGroup) > 0 ? output * (thinkBytes / groupBytes(nextGroup)) : 0);
      const rest = output - reasoning;
      const restBytes = groupBytes(nextGroup) - thinkBytes;
      if (restBytes > 0) {
        const rate = restBytes > 0 ? rest / restBytes : 0;
        for (const [key, bytes] of Object.entries(nextGroup)) {
          if (key === "signedThinking" || key === "reasoning") continue;
          parts[key] = (parts[key] || 0) + bytes * rate;
        }
      } else {
        parts.assistant = (parts.assistant || 0) + rest;
      }
      parts.reasoning = (parts.reasoning || 0) + reasoning;
    }

    // The bands should sum to the reported total; anything left over (broken
    // usage, provider quirks) is the per-point overhead.
    const sum = Object.values(parts).reduce((s, v) => s + v, 0);
    const overhead = total - sum;
    if (overhead > 0) parts.overhead = overhead;

    const merged = mergeArgs(parts);
    points.push({
      total,
      generation: message.generation,
      parts: merged.parts,
      args: merged.args,
      toolBreakdown: finalizeBreakdown(runningBreakdown),
      model,
    });

    turnN2 = { reasoning: prevCall?.reasoning ?? 0, signed: asstSigned };
    prevCall = { prompt, output, reasoning: reasoningExact || 0, signed: nextSigned };
    asstGroup = nextGroup;
    asstBreakdown = nextBreakdown;
    asstSigned = nextSigned;
    userGroup = {};
    userBreakdown = {};
    prevModel = model;
  }
  return points;
}
