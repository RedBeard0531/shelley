<!-- Reconstructed context composition per LLM call. The top line is the
     provider-reported context size; colored stacked areas estimate which
     visible history categories made it up. -->
<template>
  <div class="context-composition-graph">
    <template v-if="points.length > 0 && props.maxContextTokens > 0">
      <div class="token-cost-controls context-composition-graph-header">
        <span>estimated composition</span>
        <span class="token-cost-controls-spacer" />
        <slot name="mode-controls" />
      </div>
      <svg
        :viewBox="`0 0 ${W} ${H}`"
        class="context-composition-graph-svg"
        role="img"
        :aria-label="`Context composition across ${points.length} LLM calls`"
        @mousemove="onMove"
        @mouseleave="clearHover"
      >
        <path
          v-for="(category, index) in categories"
          :key="category.key"
          :d="areaPath(index)"
          :fill="category.color"
          class="context-composition-area"
        />
        <line
          v-for="index in compactionStarts"
          :key="`compaction-${index}`"
          :x1="xAt(index)"
          :y1="PADT"
          :x2="xAt(index)"
          :y2="H - PADB"
          class="token-cost-gen-line"
        />
        <line
          v-for="index in modelChangePoints"
          :key="`model-change-${index}`"
          :x1="xAt(index)"
          :y1="PADT"
          :x2="xAt(index)"
          :y2="H - PADB"
          class="context-composition-model-line"
        />
        <line :x1="PADL" :y1="H - PADB" :x2="W - PADR" :y2="H - PADB" class="token-cost-axis" />
        <line
          v-if="hoverX !== null"
          :x1="hoverX"
          :y1="PADT"
          :x2="hoverX"
          :y2="H - PADB"
          class="token-cost-hover-line"
        />
        <text
          v-for="tick in yTicks"
          :key="tick"
          :x="PADL - 4"
          :y="yAtTokens(tick) + 3"
          text-anchor="end"
          class="token-cost-label"
        >{{ formatTokenCount(tick) }}</text>
        <text :x="PADL" :y="H - 4" class="token-cost-label">1</text>
        <text :x="W - PADR" :y="H - 4" text-anchor="end" class="token-cost-label">{{ points.length }}</text>
      </svg>
      <div
        class="token-cost-hover-readout context-composition-readout"
      >
        <template v-if="hoverPoint">
          call {{ hoverIndex! + 1 }} of {{ points.length }} ·
          <b>{{ formatTokenCount(hoverPoint.total) }}</b> · {{ (hoverPoint.total / props.maxContextTokens * 100).toFixed(1) }}%
        </template>
        <template v-else>
          current <b>{{ formatTokenCount(points.at(-1)!.total) }}</b> · {{ (points.at(-1)!.total / props.maxContextTokens * 100).toFixed(1) }}%
        </template>
      </div>
      <div class="context-composition-legend" role="table" aria-label="Estimated context composition">
        <div class="context-composition-legend-row context-composition-legend-head">
          <span class="context-composition-legend-label">category</span>
          <span class="context-composition-legend-tokens">args</span>
          <span class="context-composition-legend-tokens">output</span>
          <span class="context-composition-legend-total">total</span>
          <span class="context-composition-legend-pct" />
        </div>
        <div
          v-for="row in breakdownRows"
          :key="row.key"
          v-tooltip.top="row.hint"
          role="row"
          :aria-label="`${row.label}: ${row.hint}`"
          class="context-composition-legend-row"
        >
          <span class="context-composition-legend-label">
            <i :style="{ background: row.color }" />{{ row.label }}
          </span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="row.args !== null" :tokens="row.args" />
          </span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="row.output !== null" :tokens="row.output" />
          </span>
          <span class="context-composition-legend-total">
            <TokenCount :tokens="row.total" />
          </span>
          <span class="context-composition-legend-pct">
            {{ breakdownTotal > 0 ? ((row.total / breakdownTotal) * 100).toFixed(0) + "%" : "" }}
          </span>
        </div>
        <div class="context-composition-legend-row context-composition-total-row">
          <span class="context-composition-legend-label">total</span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="breakdownArgsTotal" :tokens="breakdownArgsTotal" />
          </span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="breakdownOutputTotal" :tokens="breakdownOutputTotal" />
          </span>
          <span class="context-composition-legend-total">
            <TokenCount :tokens="breakdownTotal" />
          </span>
          <span class="context-composition-legend-pct">100%</span>
        </div>
      </div>
      <div v-if="compactionStarts.length" class="context-composition-compaction-note">
        Dashed lines mark compactions.
      </div>
    </template>
    <div v-else class="token-cost-hover-readout context-composition-readout">No context data yet.</div>
  </div>
</template>

<script setup lang="ts">
import { computed, h, ref, watch } from "vue";
import type { LLMContent, Message, Usage } from "../../types";
import { formatTokenCount } from "../../utils/tokenCostGraph";

const props = defineProps<{
  messages: Message[];
  maxContextTokens: number;
}>();

const TYPE_TEXT = 2;
const TYPE_THINKING = 3;
const TYPE_TOOL_USE = 5;
const TYPE_TOOL_RESULT = 6;
const TYPE_WEB_SEARCH_TOOL_RESULT = 8;
const W = 280;
const H = 150;
const PADL = 32;
const PADR = 6;
const PADT = 6;
const PADB = 18;
const plotWidth = W - PADL - PADR;
const plotHeight = H - PADT - PADB;

type Composition = Record<string, number>;
type ToolBreakdown = Record<string, Composition>;
type Point = {
  total: number;
  generation: number;
  parts: Composition;
  args: Composition;
  toolBreakdown: ToolBreakdown;
  model?: string;
};
type Category = { key: string; label: string; color: string; isTool?: boolean };
type Attribution = { key: string; detail?: string };
type CommandInvocation = { name: string; args: string[] };

// Tool-call tokens (name + input) are recorded under this suffix during the
// walk, then split out into Point.args at mapping time so the breakdown
// table can show args vs. output per tool category.
const ARGS_SUFFIX = "\u0000args";

const BASH_CATEGORIES = [
  "bash:code search",
  "bash:file read",
  "bash:build/test",
  "bash:script/query",
  "bash:system",
  "bash:other",
] as const;

const TOOL_CATEGORIES = [
  "repo/read",
  "repo/edit",
  "tool:browser/web",
  "tool:other",
] as const;

// Displayed as separate bands rather than one "text" lump.
const TEXT_CATEGORIES = ["system", "user", "assistant", "reasoning"] as const;

const CATEGORY_LABELS: Record<string, string> = {
  system: "system",
  user: "user",
  assistant: "assistant",
  reasoning: "reasoning",
  "bash:code search": "bash · code search",
  "bash:file read": "bash · file read",
  "bash:build/test": "bash · build/test",
  "bash:script/query": "bash · script/query",
  "bash:system": "bash · system",
  "repo/read": "repo/read",
  "repo/edit": "repo/edit",
  "bash:other": "bash · general",
  "tool:browser/web": "browser/web",
  "tool:other": "other tools",
};

const CATEGORY_COLORS: Record<string, string> = {
  // Cost graph-adjacent blue, purple, teal, and orange hues, with spaced
  // shades for neighboring context bands.
  system: "hsl(210 30% 55%)",
  user: "hsl(160 64% 48%)",
  assistant: "hsl(140 55% 45%)",
  reasoning: "hsl(184 60% 44%)",
  "bash:code search": "hsl(199 92% 56%)",
  "bash:file read": "hsl(199 68% 66%)",
  "bash:build/test": "hsl(234 75% 59%)",
  "bash:script/query": "hsl(350 66% 56%)",
  "bash:system": "hsl(27 96% 57%)",
  "repo/read": "hsl(190 55% 50%)",
  "repo/edit": "hsl(45 80% 54%)",
  "bash:other": "hsl(0 0% 54%)",
  "tool:browser/web": "hsl(270 58% 56%)",
  "tool:other": "hsl(213 15% 53%)",
};

const points = computed<Point[]>(() => {
  const running: Composition = {};
  const runningToolBreakdown: ToolBreakdown = {};
  const toolKeys = new Map<string, Attribution>();
  const raw: {
    total: number;
    generation: number;
    parts: Composition;
    toolBreakdown: ToolBreakdown;
    model?: string;
  }[] = [];
  let generation: number | undefined;
  let generationHasMedia = false;

  for (const message of props.messages) {
    if (generation !== undefined && message.generation !== generation) {
      for (const key of Object.keys(running)) delete running[key];
      for (const key of Object.keys(runningToolBreakdown)) delete runningToolBreakdown[key];
      toolKeys.clear();
      generationHasMedia = false;
    }
    generation = message.generation;
    generationHasMedia =
      addMessage(running, runningToolBreakdown, toolKeys, message) || generationHasMedia;
    if (message.type !== "agent") continue;
    const usage = parseUsage(message);
    const total = usage ? contextWindowUsed(usage) : 0;
    if (total === 0) continue;

    const estimated = Object.values(running).reduce((sum, tokens) => sum + tokens, 0);
    raw.push({
      total,
      generation: message.generation,
      parts: estimated > 0 ? { ...running } : generationHasMedia ? {} : { assistant: total },
      toolBreakdown: copyToolBreakdown(runningToolBreakdown),
      model: message.model_name || undefined,
    });
  }

  // A per-call scale made old text appear to shrink whenever a large tool
  // result changed the estimate/provider ratio. Calibrate once at the last
  // call in each generation instead: within a generation, reconstructed
  // context is cumulative and must only grow. A compaction starts a new
  // generation and is the one legitimate reset.
  const scaleByGeneration = new Map<number, number>();
  for (const point of raw) {
    const estimated = Object.values(point.parts).reduce((sum, tokens) => sum + tokens, 0);
    scaleByGeneration.set(point.generation, estimated > 0 ? point.total / estimated : 1);
  }
  return raw.map((point) => {
    const scale = scaleByGeneration.get(point.generation) || 1;
    const parts: Composition = {};
    const args: Composition = {};
    for (const [key, tokens] of Object.entries(point.parts)) {
      if (key.endsWith(ARGS_SUFFIX)) {
        const base = key.slice(0, -ARGS_SUFFIX.length);
        args[base] = Math.round((args[base] || 0) + tokens * scale);
        parts[base] = (parts[base] || 0) + tokens * scale;
      } else {
        parts[key] = Math.round(tokens * scale);
      }
    }
    const toolBreakdown = Object.fromEntries(
      Object.entries(point.toolBreakdown).map(([key, details]) => [
        key,
        Object.fromEntries(
          Object.entries(details).map(([detail, tokens]) => [detail, Math.round(tokens * scale)]),
        ),
      ]),
    );
    return {
      total: point.total,
      generation: point.generation,
      parts,
      args,
      toolBreakdown,
      model: point.model,
    };
  });
});

const categories = computed<Category[]>(() => {
  const keys = new Set<string>();
  for (const point of points.value) {
    for (const key of Object.keys(point.parts)) keys.add(key);
  }
  return [
    ...TEXT_CATEGORIES.filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
    })),
    ...BASH_CATEGORIES.filter((key) => key !== "bash:other" && keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
      isTool: true,
    })),
    ...TOOL_CATEGORIES.slice(0, 2).filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
      isTool: true,
    })),
    ...(keys.has("bash:other")
      ? [{ key: "bash:other", label: CATEGORY_LABELS["bash:other"], color: CATEGORY_COLORS["bash:other"], isTool: true }]
      : []),
    ...TOOL_CATEGORIES.slice(2).filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
      isTool: true,
    })),
  ];
});

const chartMax = computed(() => Math.max(0, ...points.value.map(plottedTotal)));
const yTicks = computed(() => {
  const max = chartMax.value;
  return max > 0 ? [...new Set([0, Math.round(max / 2), max])] : [0];
});
const compactionStarts = computed(() =>
  points.value.flatMap((point, index) =>
    index > 0 && point.generation !== points.value[index - 1].generation ? [index] : [],
  ),
);

// Red vertical lines at mid-generation model changes: switching the model
// rebuilds the context, so the user's action causes a cache miss there.
const modelChangePoints = computed<Set<number>>(() => {
  const set = new Set<number>();
  for (let i = 1; i < points.value.length; i++) {
    if (points.value[i].generation !== points.value[i - 1].generation) continue;
    if (
      points.value[i].model &&
      points.value[i - 1].model &&
      points.value[i].model !== points.value[i - 1].model
    )
      set.add(i);
  }
  return set;
});

const hoverIndex = ref<number | null>(null);
const hoverX = ref<number | null>(null);
const hoverPoint = computed(() =>
  hoverIndex.value === null ? null : points.value[hoverIndex.value] || null,
);

/** Rendered token count with a bold+italic unit suffix (k/M/B) in the
 *  breakdown table. */
const TokenCount = (props: { tokens: number }) => {
  const text = formatTokenCount(props.tokens);
  const match = text.match(/^([\d,.]+)([kMB]?)$/)!;
  return match[2]
    ? [match[1], h("i", { class: "context-composition-unit" }, match[2])]
    : text;
};
TokenCount.props = ["tokens"];

// The breakdown table reflects the hovered call when hovering the graph,
// otherwise the current (last) point.
const lastPoint = computed(() => points.value.at(-1) || null);
const breakdownPoint = computed(() => hoverPoint.value || lastPoint.value);
const breakdownRows = computed(() => {
  const point = breakdownPoint.value;
  if (!point) return [];
  return categories.value.map((category) => {
    const total = point.parts[category.key] || 0;
    const args = point.args[category.key] || 0;
    return {
      ...category,
      args: category.isTool ? args : null,
      output: category.isTool ? total - args : null,
      total,
      hint: CATEGORY_HINTS[category.key] || categoryHint(category.key, point),
    };
  });
});
const breakdownTotal = computed(() => breakdownRows.value.reduce((sum, row) => sum + row.total, 0));
const breakdownArgsTotal = computed(() => breakdownRows.value.reduce((sum, row) => sum + (row.args || 0), 0));
const breakdownOutputTotal = computed(() => breakdownRows.value.reduce((sum, row) => sum + (row.output || 0), 0));

// A shrinking or replaced message list (conversation switch) invalidates the
// stale hover point.
watch(
  () => points.value.length,
  () => {
    hoverIndex.value = null;
    hoverX.value = null;
  },
);

function areaPath(categoryIndex: number) {
  if (points.value.length === 0) return "";
  const upper = points.value.map((point, index) => {
    const sum = categories.value
      .slice(0, categoryIndex + 1)
      .reduce((total, category) => total + categoryTokens(point, category.key), 0);
    return `${xAt(index)},${yAtTokens(sum)}`;
  });
  const lower = points.value
    .map((point, index) => {
      const sum = categories.value
        .slice(0, categoryIndex)
        .reduce((total, category) => total + categoryTokens(point, category.key), 0);
      return `${xAt(index)},${yAtTokens(sum)}`;
    })
    .reverse();
  return `M${upper.join(" L")} L${lower.join(" L")} Z`;
}

function xAt(index: number) {
  if (points.value.length <= 1) return PADL + plotWidth / 2;
  return PADL + (index / (points.value.length - 1)) * plotWidth;
}

function yAtTokens(tokens: number) {
  const max = chartMax.value;
  return max > 0 ? PADT + (1 - tokens / max) * plotHeight : H - PADB;
}

function onMove(event: MouseEvent) {
  if (points.value.length === 0) return;
  const rect = (event.currentTarget as SVGElement).getBoundingClientRect();
  const pointerX = ((event.clientX - rect.left) / rect.width) * W;
  hoverX.value = Math.min(W - PADR, Math.max(PADL, pointerX));
  let nearest = 0;
  let distance = Infinity;
  for (let index = 0; index < points.value.length; index++) {
    const nextDistance = Math.abs(xAt(index) - hoverX.value);
    if (nextDistance < distance) {
      nearest = index;
      distance = nextDistance;
    }
  }
  hoverIndex.value = nearest;
}

function clearHover() {
  hoverX.value = null;
  hoverIndex.value = null;
}

function addMessage(
  running: Composition,
  runningToolBreakdown: ToolBreakdown,
  toolKeys: Map<string, Attribution>,
  message: Message,
): boolean {
  // gitinfo/modelchange/error messages are user-visible only — the server
  // never sends them to the LLM, so they are not part of the context the
  // graph reconstructs.
  if (!message.llm_data || ["gitinfo", "modelchange", "error"].includes(message.type))
    return false;
  try {
    const llm = typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    const fallback = {
      key:
        message.type === "user" ? "user" : message.type === "system" ? "system" : "assistant",
    };
    let hasMedia = false;
    for (const content of (llm?.Content || []) as LLMContent[]) {
      hasMedia =
        addContent(running, runningToolBreakdown, toolKeys, content, fallback) || hasMedia;
    }
    return hasMedia;
  } catch {
    // A malformed historic payload stays visible in the conversation but
    // cannot contribute to a reconstructed graph.
    return false;
  }
}

function addContent(
  running: Composition,
  runningToolBreakdown: ToolBreakdown,
  toolKeys: Map<string, Attribution>,
  content: LLMContent,
  fallback: Attribution,
): boolean {
  if (content.MediaType || content.DisplayImageURL || content.Data) {
    return true;
  }
  switch (content.Type) {
    case TYPE_TOOL_USE: {
      const attribution = toolAttribution(content.ToolName, content.ToolInput);
      toolKeys.set(content.ID, attribution);
      addAttributedTokens(
        running,
        runningToolBreakdown,
        attribution,
        estimateTokens(content.ToolName || "") + estimateTokens(stringify(content.ToolInput)),
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
          addContent(running, runningToolBreakdown, toolKeys, result, attribution) || hasMedia;
      }
      return hasMedia;
    }
    case TYPE_TEXT:
      addAttributedTokens(
        running,
        runningToolBreakdown,
        fallback,
        estimateTokens(content.Text || content.Thinking || ""),
      );
      return false;
    case TYPE_THINKING:
      addAttributedTokens(
        running,
        runningToolBreakdown,
        isToolCategory(fallback.key) ? fallback : { key: "reasoning" },
        estimateTokens(content.Text || content.Thinking || ""),
      );
      return false;
    default:
      addAttributedTokens(
        running,
        runningToolBreakdown,
        fallback,
        estimateTokens(content.Text || ""),
      );
      return false;
  }
}

function categoryTokens(point: Point, key: string) {
  return point.parts[key] || 0;
}

function plottedTotal(point: Point) {
  return categories.value.reduce((sum, category) => sum + categoryTokens(point, category.key), 0);
}

const CATEGORY_HINTS: Record<string, string> = {
  system: "System prompt injected before the first user message",
  user: "Text typed by the user, plus mid-conversation injections (e.g. subagent-done pokes)",
  assistant: "Assistant text output",
  reasoning: "Assistant thinking blocks",
};

function categoryHint(key: string, point: Point) {
  if (CATEGORY_HINTS[key]) return CATEGORY_HINTS[key];
  const breakdown = point.toolBreakdown[key];
  if (!breakdown) return "Tool output";
  const details = Object.entries(breakdown)
    .filter(([, tokens]) => tokens > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([name, tokens]) => `${name} ${formatTokenCount(tokens)}`)
    .join(" · ");
  return details || "Tool output";
}

function isToolCategory(key: string) {
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
  isArgs = false,
) {
  addTokens(running, isArgs ? attribution.key + ARGS_SUFFIX : attribution.key, tokens);
  if (!attribution.detail || !isToolCategory(attribution.key)) return;
  const breakdown = (runningToolBreakdown[attribution.key] ||= {});
  breakdown[attribution.detail] = (breakdown[attribution.detail] || 0) + tokens;
}

function copyToolBreakdown(source: ToolBreakdown): ToolBreakdown {
  return Object.fromEntries(
    Object.entries(source).map(([key, details]) => [key, { ...details }]),
  );
}

function toolAttribution(name: string | undefined, input?: unknown): Attribution {
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
  const intent = bashCommandIntent(commandFromInput(input));
  return {
    key: intent.family.startsWith("repo/") ? intent.family : `bash:${intent.family}`,
    detail: intent.command,
  };
}

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
    ["rm", "mkdir", "gofmt", "chmod", "mv", "cp", "touch", "ln", "install", "patch"].includes(
      name,
    )
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
  if (["python", "python3", "node", "ruby", "perl", "sqlite3", "psql", "mysql", "jq", "yq"].includes(name))
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
      ["--show-current", "--list", "--contains", "--no-contains", "--merged", "--no-merged"].includes(
        arg,
      ),
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
  if (wrapper === "sudo") return ["-u", "-g", "-h", "-C", "-r", "-t", "--user", "--group", "--host", "--close-from", "--role", "--type", "--chdir"].includes(option);
  if (wrapper === "env") return ["-u", "-C", "--unset", "--chdir"].includes(option);
  if (wrapper === "timeout") return ["-k", "--kill-after"].includes(option);
  if (wrapper === "nice") return ["-n", "--adjustment"].includes(option);
  return wrapper === "time" && ["-f", "-o"].includes(option);
}

function parseUsage(message: Message): Usage | null {
  if (!message.usage_data) return null;
  try {
    return typeof message.usage_data === "string" ? JSON.parse(message.usage_data) : message.usage_data;
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

</script>
