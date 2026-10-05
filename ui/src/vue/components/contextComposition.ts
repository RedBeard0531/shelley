// Reconstructs, per LLM call, which categories of history made up the
// context the provider reported. In-place compaction records (trims, squishes,
// hides) are replayed, so the estimate follows what the model actually saw.
import type { InPlaceCompaction } from "../../generated-types";
import type { LLMContent, Message, Usage } from "../../types";

const TYPE_TEXT = 2;
const TYPE_THINKING = 3;
const TYPE_TOOL_USE = 5;
const TYPE_TOOL_RESULT = 6;
const TYPE_WEB_SEARCH_TOOL_RESULT = 8;

// Tokens attributed per image block, matching the server's pi estimator
// (piImageTokens); the base64 data itself is stripped from stored llm_data.
const imageTokens = 1200;

export type Composition = Record<string, number>;
export type ToolBreakdown = Record<string, Composition>;
export type Point = {
  total: number;
  // segment changes at every compaction: a new generation or an in-place
  // compaction record.
  segment: number;
  parts: Composition;
  toolBreakdown: ToolBreakdown;
  /** Tool-argument tokens per tool category (subset of parts). */
  args: Composition;
};
type Attribution = { key: string; detail?: string };
type CommandInvocation = { name: string; args: string[] };
type AddTokens = (toolUseID: string, attribution: Attribution, tokens: number, isArgs?: boolean) => void;

// An entry is what one message contributed for one tool_use_id ("" for the
// rest); compaction records remove entries.
type Entry = {
  from: number;
  to: number;
  toolUseID: string;
  parts: Composition;
  args: Composition;
  toolBreakdown: ToolBreakdown;
};

class Context {
  entries = new Map<string, Entry>();
  parts: Composition = {};
  args: Composition = {};
  toolBreakdown: ToolBreakdown = {};
  toolKeys = new Map<string, Attribution>();

  add(
    from: number,
    to: number,
    toolUseID: string,
    attribution: Attribution,
    tokens: number,
    isArgs = false,
  ) {
    const key = `${from}-${to}|${toolUseID}`;
    let entry = this.entries.get(key);
    if (!entry) {
      entry = { from, to, toolUseID, parts: {}, args: {}, toolBreakdown: {} };
      this.entries.set(key, entry);
    }
    addAttributedTokens(entry.parts, entry.toolBreakdown, attribution, tokens);
    addAttributedTokens(this.parts, this.toolBreakdown, attribution, tokens);
    if (isArgs) {
      addTokens(entry.args, attribution.key, tokens);
      addTokens(this.args, attribution.key, tokens);
    }
  }

  remove(match: (entry: Entry) => boolean) {
    for (const [key, entry] of this.entries) {
      if (!match(entry)) continue;
      this.entries.delete(key);
      for (const [part, tokens] of Object.entries(entry.parts)) this.parts[part] -= tokens;
      for (const [part, tokens] of Object.entries(entry.args)) this.args[part] -= tokens;
      for (const [part, details] of Object.entries(entry.toolBreakdown)) {
        for (const [detail, tokens] of Object.entries(details)) {
          this.toolBreakdown[part][detail] -= tokens;
        }
      }
    }
  }

  // apply mirrors applyInPlaceCompaction on the server: trims, then squishes,
  // then hides.
  apply(record: InPlaceCompaction) {
    for (const trim of record.trims || []) {
      const seq = trim.sequence_id;
      this.remove((e) => e.from === seq && e.toolUseID === trim.tool_use_id);
      this.add(
        seq,
        seq,
        trim.tool_use_id,
        this.toolKeys.get(trim.tool_use_id) || { key: "tool:other" },
        estimateTokens(`[Tool output compacted: conversation_id cXXXXXX sequence_id ${seq}]`),
      );
    }
    for (const squish of record.squishes || []) {
      const { from_sequence_id: from, to_sequence_id: to } = squish;
      this.remove((e) => e.from >= from && e.to <= to);
      this.add(
        from,
        to,
        "",
        { key: "user" },
        estimateTokens(
          `[Messages compacted: conversation_id cXXXXXX sequence_id ${from}-${to}. Summary:]\n${squish.summary}`,
        ),
      );
    }
    const seqs = new Set(record.hidden_sequence_ids || []);
    const ids = new Set(record.hidden_tool_use_ids || []);
    this.remove((e) => (e.from === e.to && seqs.has(e.from)) || ids.has(e.toolUseID));
  }
}

export function contextCompositionPoints(messages: Message[]): Point[] {
  const raw: Point[] = [];
  let context = new Context();
  let generation: number | undefined;
  let segment = 0;
  let generationHasMedia = false;
  // The first call of a generation measures the fixed request overhead the
  // walk cannot see (tool definitions, framing): reported prompt size minus
  // the estimated message bytes so far, folded into the system band once.
  let measuredSystem = false;

  for (const message of messages) {
    if (generation !== undefined && message.generation !== generation) {
      context = new Context();
      segment++;
      generationHasMedia = false;
      measuredSystem = false;
    }
    generation = message.generation;
    if (message.type === "inplacecompaction") {
      context.apply(parseJSON<InPlaceCompaction>(message.user_data) || {});
      segment++;
      continue;
    }
    const seq = message.sequence_id;
    const before =
      message.type === "agent"
        ? Object.values(context.parts).reduce((sum, tokens) => sum + tokens, 0)
        : 0;
    generationHasMedia =
      addMessage(context.toolKeys, message, (toolUseID, attribution, tokens, isArgs) =>
        context.add(seq, seq, toolUseID, attribution, tokens, isArgs),
      ) || generationHasMedia;
    if (message.type !== "agent") continue;
    const usage = parseJSON<Usage>(message.usage_data);
    const total = usage ? contextWindowUsed(usage) : 0;
    if (total === 0) continue;

    if (!measuredSystem) {
      measuredSystem = true;
      const prompt = total - (usage?.output_tokens || 0);
      const residual = prompt - before;
      if (residual > 0) context.add(seq, seq, "", { key: "system" }, residual);
    }

    const estimated = Object.values(context.parts).reduce((sum, tokens) => sum + tokens, 0);
    raw.push({
      total,
      segment,
      parts: estimated > 0 ? { ...context.parts } : generationHasMedia ? {} : { assistant: total },
      toolBreakdown: copyToolBreakdown(context.toolBreakdown),
      args: { ...context.args },
    });
  }

  // A per-call scale made old text appear to shrink whenever a large tool
  // result changed the estimate/provider ratio. Calibrate once at the last
  // call in each segment instead: within a segment, reconstructed context is
  // cumulative and must only grow. A compaction starts a new segment and is
  // the one legitimate reset.
  const scaleBySegment = new Map<number, number>();
  for (const point of raw) {
    const estimated = Object.values(point.parts).reduce((sum, tokens) => sum + tokens, 0);
    scaleBySegment.set(point.segment, estimated > 0 ? point.total / estimated : 1);
  }
  return raw.map((point) => {
    const scale = scaleBySegment.get(point.segment) || 1;
    const parts = Object.fromEntries(
      Object.entries(point.parts).map(([key, tokens]) => [key, Math.round(tokens * scale)]),
    );
    const args = Object.fromEntries(
      Object.entries(point.args).map(([key, tokens]) => [key, Math.round(tokens * scale)]),
    );
    const toolBreakdown = Object.fromEntries(
      Object.entries(point.toolBreakdown).map(([key, details]) => [
        key,
        Object.fromEntries(
          Object.entries(details).map(([detail, tokens]) => [detail, Math.round(tokens * scale)]),
        ),
      ]),
    );
    return { ...point, parts, args, toolBreakdown };
  });
}

function addMessage(toolKeys: Map<string, Attribution>, message: Message, add: AddTokens): boolean {
  if (!message.llm_data) return false;
  try {
    const llm =
      typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    const fallback = {
      key: message.type === "user" ? "user" : message.type === "system" ? "system" : "assistant",
    };
    // Reported output/reasoning tokens are exact: scale this message's block
    // estimates so thinking takes exactly reasoning_tokens out of
    // output_tokens, and the remaining output splits by byte ratio across
    // text and tool arguments. Falls back to raw byte estimates when the
    // provider reported nothing.
    const usage = parseJSON<Usage>(message.usage_data);
    const reportedReasoning = usage?.reasoning_tokens || 0;
    const outputTokens = usage?.output_tokens || 0;
    const blocks = (llm?.Content || []) as LLMContent[];
    const estimateOf = (c: LLMContent) => estimateTokens(c.Text || c.Thinking || "");
    const thinkingEstimate = blocks
      .filter((c) => c.Type === TYPE_THINKING)
      .reduce((sum, c) => sum + estimateOf(c), 0);
    // Tool arguments are part of the assistant's output, so the output-side
    // estimate must include their bytes — scaling args by a factor computed
    // without them would inflate args on every call whose visible text is
    // smaller than its non-reasoning output.
    const outputEstimate = blocks
      .filter((c) => c.Type === TYPE_TEXT || c.Type === TYPE_TOOL_USE)
      .reduce(
        (sum, c) =>
          sum +
          estimateOf(c) +
          (c.Type === TYPE_TOOL_USE
            ? estimateTokens(c.ToolName || "") + estimateTokens(stringify(c.ToolInput))
            : 0),
        0,
      );
    const factors = {
      thinking:
        reportedReasoning > 0 && thinkingEstimate > 0
          ? reportedReasoning / thinkingEstimate
          : 1,
      output:
        reportedReasoning > 0 && outputTokens > reportedReasoning && outputEstimate > 0
          ? (outputTokens - reportedReasoning) / outputEstimate
          : 1,
    };
    let hasMedia = false;
    for (const content of blocks) {
      hasMedia = addContent(toolKeys, content, fallback, "", add, factors) || hasMedia;
    }
    return hasMedia;
  } catch {
    // A malformed historic payload stays visible in the conversation but
    // cannot contribute to a reconstructed graph.
    return false;
  }
}

function addContent(
  toolKeys: Map<string, Attribution>,
  content: LLMContent,
  fallback: Attribution,
  toolUseID: string,
  add: AddTokens,
  factors: { thinking: number; output: number } = { thinking: 1, output: 1 },
): boolean {
  if (content.MediaType || content.DisplayImageURL || content.Data) {
    // Storage strips image bytes from llm_data, so byte estimates would give
    // images ~1 token. Attribute a fixed allowance instead, matching the
    // server's pi estimator (1280x720 screenshot ≈ 1.2k tokens). The
    // provider-reported total stays exact either way; only the split moves.
    add(toolUseID, { key: "images" }, imageTokens);
    return true;
  }
  switch (content.Type) {
    case TYPE_TOOL_USE: {
      const attribution = toolAttribution(content.ToolName, content.ToolInput);
      toolKeys.set(content.ID, attribution);
      add(
        content.ID,
        attribution,
        (estimateTokens(content.ToolName || "") + estimateTokens(stringify(content.ToolInput))) *
          factors.output,
        true,
      );
      return false;
    }
    case TYPE_TOOL_RESULT:
    case TYPE_WEB_SEARCH_TOOL_RESULT: {
      const attribution =
        toolKeys.get(content.ToolUseID || "") ||
        toolAttribution(content.Type === TYPE_WEB_SEARCH_TOOL_RESULT ? "web_search" : "other");
      let hasMedia = false;
      for (const result of content.ToolResult || []) {
        hasMedia =
          addContent(toolKeys, result, attribution, content.ToolUseID || "", add, factors) ||
          hasMedia;
      }
      return hasMedia;
    }
    case TYPE_TEXT:
      add(toolUseID, fallback, estimateTokens(content.Text || content.Thinking || "") * factors.output);
      return false;
    case TYPE_THINKING:
      add(
        toolUseID,
        isToolCategory(fallback.key) ? fallback : { key: "reasoning" },
        estimateTokens(content.Text || content.Thinking || "") * factors.thinking,
      );
      return false;
    default:
      add(toolUseID, fallback, estimateTokens(content.Text || "") * factors.output);
      return false;
  }
}

export function isToolCategory(key: string) {
  return key.startsWith("bash:") || key.startsWith("tool:") || key.startsWith("repo/");
}

function addTokens(running: Composition, key: string, tokens: number) {
  running[key] = (running[key] || 0) + tokens;
}

function addAttributedTokens(
  running: Composition,
  runningToolBreakdown: ToolBreakdown,
  attribution: Attribution,
  tokens: number,
) {
  addTokens(running, attribution.key, tokens);
  if (!attribution.detail || !isToolCategory(attribution.key)) return;
  const breakdown = (runningToolBreakdown[attribution.key] ||= {});
  breakdown[attribution.detail] = (breakdown[attribution.detail] || 0) + tokens;
}

function copyToolBreakdown(source: ToolBreakdown): ToolBreakdown {
  return Object.fromEntries(Object.entries(source).map(([key, details]) => [key, { ...details }]));
}

function toolAttribution(name: string | undefined, input?: unknown): Attribution {
  if (name !== "bash") {
    let key: string;
    switch (name) {
      case "browser":
      case "web_search":
      case "keyword_search":
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
  const intent = bashCommandIntent(commandFromInput(input));
  return {
    key: intent.family.startsWith("repo/") ? intent.family : `bash:${intent.family}`,
    detail: intent.command,
  };
}

function commandFromInput(input: unknown) {
  if (
    typeof input === "object" &&
    input &&
    "command" in input &&
    typeof input.command === "string"
  ) {
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

function bashCommandIntent(command: string): { family: string; command: string } {
  const invocations = bashCommandInvocations(command);
  for (const invocation of invocations) {
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
  return { family: "other", command: invocations[0]?.name || "shell" };
}

function bashCommandFamily(name: string): string | null {
  if (
    ["rm", "mkdir", "gofmt", "chmod", "mv", "cp", "touch", "ln", "install", "patch"].includes(name)
  )
    return "repo/edit";
  if (["rg", "grep", "find", "fd", "ag", "ack"].includes(name)) return "code search";
  if (
    [
      "cat",
      "sed",
      "head",
      "tail",
      "awk",
      "ls",
      "pwd",
      "less",
      "more",
      "tree",
      "stat",
      "file",
      "readlink",
      "realpath",
      "wc",
      "cut",
      "sort",
      "uniq",
      "column",
      "diff",
      "strings",
    ].includes(name)
  )
    return "file read";
  if (
    [
      "go",
      "pnpm",
      "npm",
      "yarn",
      "make",
      "cargo",
      "pytest",
      "jest",
      "vitest",
      "bun",
      "uv",
      "ruff",
      "mypy",
      "eslint",
      "tsc",
      "biome",
      "gradle",
      "mvn",
    ].includes(name)
  )
    return "build/test";
  if (
    ["python", "python3", "node", "ruby", "perl", "sqlite3", "psql", "mysql", "jq", "yq"].includes(
      name,
    )
  )
    return "script/query";
  if (
    [
      "tmux",
      "curl",
      "wget",
      "df",
      "du",
      "ss",
      "systemctl",
      "journalctl",
      "ps",
      "pgrep",
      "pkill",
      "kill",
      "lsof",
      "ip",
      "netstat",
      "ping",
      "dig",
      "nslookup",
      "hostname",
      "uname",
      "whoami",
      "date",
      "uptime",
      "free",
      "which",
      "whereis",
    ].includes(name)
  )
    return "system";
  return null;
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
    if (
      [
        "cd",
        "export",
        "set",
        "true",
        ":",
        ".",
        "source",
        "if",
        "then",
        "fi",
        "for",
        "do",
        "done",
        "while",
        "case",
        "esac",
        "{",
        "}",
      ].includes(word)
    )
      return null;
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
    if (
      !arg.includes("=") &&
      ["-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env"].includes(arg)
    ) {
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
      else if (word[i] === '"' && !singleQuoted && word[i - 1] !== "\\")
        doubleQuoted = !doubleQuoted;
    }
    if (!singleQuoted && !doubleQuoted) return index + 1;
  }
  return index;
}

function isReadOnlyGitCommand(subcommand: string, args: string[]) {
  if (
    [
      "status",
      "diff",
      "show",
      "log",
      "grep",
      "blame",
      "shortlog",
      "describe",
      "rev-parse",
      "rev-list",
      "ls-files",
      "ls-tree",
      "cat-file",
      "name-rev",
      "for-each-ref",
      "show-ref",
      "reflog",
    ].includes(subcommand)
  )
    return true;
  if (subcommand === "branch")
    return args.some((arg) =>
      [
        "--show-current",
        "--list",
        "--contains",
        "--no-contains",
        "--merged",
        "--no-merged",
      ].includes(arg),
    );
  if (subcommand === "remote")
    return args.some((arg) => ["-v", "--verbose", "show", "get-url"].includes(arg));
  if (subcommand === "config")
    return args.some((arg) => ["--get", "--get-all", "--get-regexp", "--list", "-l"].includes(arg));
  if (subcommand === "tag") return args.some((arg) => ["--list", "-l"].includes(arg));
  return false;
}

function commandBasename(word: string) {
  return (
    word
      .replace(/^['"]|['"]$/g, "")
      .split("/")
      .at(-1) || ""
  );
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
    return [
      "-u",
      "-g",
      "-h",
      "-C",
      "-r",
      "-t",
      "--user",
      "--group",
      "--host",
      "--close-from",
      "--role",
      "--type",
      "--chdir",
    ].includes(option);
  if (wrapper === "env") return ["-u", "-C", "--unset", "--chdir"].includes(option);
  if (wrapper === "timeout") return ["-k", "--kill-after"].includes(option);
  if (wrapper === "nice") return ["-n", "--adjustment"].includes(option);
  return wrapper === "time" && ["-f", "-o"].includes(option);
}

function parseJSON<T>(value: unknown): T | null {
  if (!value) return null;
  try {
    return typeof value === "string" ? JSON.parse(value) : (value as T);
  } catch {
    return null;
  }
}

function contextWindowUsed(usage: Usage) {
  return (
    (usage.input_tokens || 0) +
    (usage.cache_creation_input_tokens || 0) +
    (usage.cache_read_input_tokens || 0) +
    (usage.output_tokens || 0)
  );
}

function estimateTokens(value: string) {
  return value ? Math.ceil(new TextEncoder().encode(value).length / 4) : 0;
}

function stringify(value: unknown) {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value) || "";
  } catch {
    return "";
  }
}
