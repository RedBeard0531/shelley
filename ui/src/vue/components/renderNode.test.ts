// Unit tests for markToolTurnRuns (connector-rail run marking).
// Run with: tsx src/vue/components/renderNode.test.ts
import { markToolTurnRuns, type RenderNode } from "./renderNode";
import type { CoalescedItem } from "./coalesce";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function check(name: string, cond: boolean, detail?: unknown) {
  if (cond) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}${detail !== undefined ? `\n   ${JSON.stringify(detail)}` : ""}`);
  }
}

function toolNode(sequenceID: number, key: string): RenderNode {
  return {
    kind: "tool-call",
    key,
    item: { type: "tool", generation: 1, sourceSequenceID: sequenceID, anchorKey: `tool:${key}` } as CoalescedItem,
  };
}
const groupsOf = (nodes: RenderNode[]) =>
  nodes.map((n) => (n.kind === "tool-call" ? (n.turnGroup ?? "-") : n.kind));

// Three calls from one message, then one call from the next message.
{
  const nodes: RenderNode[] = [
    toolNode(1, "a"),
    toolNode(1, "b"),
    toolNode(1, "c"),
    toolNode(2, "d"),
  ];
  markToolTurnRuns(nodes);
  check("run of three gets start/mid/end; solo gets none", groupsOf(nodes).join(",") === "start,mid,end,-", groupsOf(nodes));
}

// An interleaved non-tool node splits the run — each side stands alone.
{
  const nodes: RenderNode[] = [
    toolNode(1, "a"),
    { kind: "btw", key: "btw1", exchanges: [] },
    toolNode(1, "b"),
  ];
  markToolTurnRuns(nodes);
  check("interleaved btw splits a same-message run into solos", groupsOf(nodes).join(",") === "-,btw,-", groupsOf(nodes));
}

// Runs spanning a message boundary are not joined even without interleaving.
{
  const nodes: RenderNode[] = [toolNode(1, "a"), toolNode(2, "b"), toolNode(1, "c")];
  markToolTurnRuns(nodes);
  check("alternating messages -> no runs", groupsOf(nodes).join(",") === "-,-,-", groupsOf(nodes));
}

// Runs inside carried bands are marked.
{
  const children: RenderNode[] = [toolNode(7, "a"), toolNode(7, "b")];
  const nodes: RenderNode[] = [{ kind: "carried-band", key: "band", count: 1, children }];
  markToolTurnRuns(nodes);
  check("carried-band children form their own run", groupsOf(children).join(",") === "start,end", groupsOf(children));
}

// Marking is idempotent (renderModel recomputes over cached coalesce items).
{
  const nodes: RenderNode[] = [toolNode(1, "a"), toolNode(1, "b")];
  markToolTurnRuns(nodes);
  markToolTurnRuns(nodes);
  check("re-marking over cached items is stable", groupsOf(nodes).join(",") === "start,end", groupsOf(nodes));
}

// A solo tool call inside a carried band gets no marker.
{
  const children: RenderNode[] = [toolNode(7, "a")];
  const nodes: RenderNode[] = [{ kind: "carried-band", key: "band", count: 1, children }];
  markToolTurnRuns(nodes);
  check("solo call in carried band gets no marker", groupsOf(children).join(",") === "-", groupsOf(children));
}

console.log(`\nmarkToolTurnRuns Tests: ${passed} passed, ${failed} failed\n`);
if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("All tests passed!");
process.exit(0);
