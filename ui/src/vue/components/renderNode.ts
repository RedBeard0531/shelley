// Shared render-model types for ChatInterface.vue and MessageRenderNode.vue.
import type { BtwExchange, Message } from "../../types";
import type { CoalescedItem } from "./coalesce";

export type RenderNode =
  | { kind: "day-separator"; key: string; label: string }
  | { kind: "timestamp"; key: string; createdAt: string }
  | { kind: "token-marker"; key: string; label: string; ctx: number }
  | { kind: "message"; key: string; item: CoalescedItem }
  | {
      kind: "tool-call";
      key: string;
      item: CoalescedItem;
      /** Position within a run of tool calls from one assistant message. */
      turnGroup?: "start" | "mid" | "end";
    }
  | { kind: "btw"; key: string; exchanges: BtwExchange[] }
  | { kind: "carried-band"; key: string; count: number; children: RenderNode[] };

/**
 * Mark runs of consecutive tool-call nodes whose items came from the same
 * assistant message (same sourceSequenceID), so the UI can draw a connector
 * rail linking them. Runs are computed over the final node stream — after
 * timestamps, token markers, and btw anchors have been interleaved — so an
 * interleaved node honestly splits a run. Runs inside carried bands are
 * marked too. Solo calls (run of one) get no marker.
 */
export function markToolTurnRuns(nodes: RenderNode[]): void {
  let i = 0;
  while (i < nodes.length) {
    const node = nodes[i];
    if (node.kind === "carried-band") {
      markToolTurnRuns(node.children);
      i++;
      continue;
    }
    if (node.kind !== "tool-call") {
      i++;
      continue;
    }
    const start = i;
    const source = node.item.sourceSequenceID;
    while (i + 1 < nodes.length) {
      const next = nodes[i + 1];
      if (next.kind !== "tool-call" || next.item.sourceSequenceID !== source) break;
      i++;
    }
    const end = nodes[i];
    if (i > start && end.kind === "tool-call") {
      node.turnGroup = "start";
      end.turnGroup = "end";
      for (let j = start + 1; j < i; j++) {
        const mid = nodes[j];
        if (mid.kind === "tool-call") mid.turnGroup = "mid";
      }
    }
    i++;
  }
}

// A run of consecutive render nodes wrapped in one content-visibility:auto
// element. Granularity matters in WebKit: one giant container (the whole
// generation) can never be skipped and costs 100ms+ per frame just managing
// the containment of its ~50k-node subtree, while per-row containment
// (thousands of elements) makes every frame re-check thousands of
// viewport-relevancy candidates. Chunks of a few dozen rows hit the sweet
// spot: off-screen chunks skip layout/paint entirely and the per-frame
// bookkeeping stays trivial.
export interface RenderChunk {
  key: string;
  nodes: RenderNode[];
  // Trailing chunks render without content-visibility (see LIVE_TAIL_CHUNKS):
  // real layout from birth, so their heights are never estimates.
  live?: boolean;
  // Position in the conversation-wide chunk sequence (across generation
  // blocks). Drives tail-first mounting: chunks below the mount floor render
  // as fixed-height placeholders until the background sweep (or a
  // near-viewport reveal) reaches them. Positional rather than key-based:
  // history chunks are stable under appends, and the rare mid-history
  // restructure (generation flip, view-mode change) resets the floor anyway.
  globalIndex: number;
}

export interface GenerationBlock {
  generation: number;
  divider?: { from: number; to: number };
  sectionClass: string;
  modelBar: { key: string; model?: string | null; modelsUsed: string[] };
  systemPrompts: { key: string; message: Message }[];
  chunks: RenderChunk[];
}
