import type { GitTour } from "../../services/api";
import { buildTourContents, buildTourContentsLayout } from "./commitTourContents";

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(
      `${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`,
    );
  }
}

function run(name: string, fn: () => void): void {
  fn();
  console.log(`✓ ${name}`);
}

const patch = (start: number, path = "src/example.ts") =>
  [
    `diff --git a/${path} b/${path}`,
    "index 1111111..2222222 100644",
    `--- a/${path}`,
    `+++ b/${path}`,
    `@@ -${start},1 +${start},1 @@`,
    "-old",
    "+new",
  ].join("\n");

run("tour contents follows narrative order with unique anchors", () => {
  const tour: GitTour = {
    version: 1,
    title: "Example tour",
    intro: "Start here.",
    chunks: [
      { header: "## Core behavior" },
      { patch: patch(2), comment: "First change." },
      { patch: patch(18), comment: "Second change." },
    ],
  };

  const contents = buildTourContents(tour, true);
  assertEqual(
    contents,
    [
      { anchor: "tour-overview", label: "Overview", kind: "overview" },
      { anchor: "tour-entry-0", label: "Core behavior", kind: "section" },
      {
        anchor: "tour-entry-1",
        label: "src/example.ts · 2–2 +1 −1",
        kind: "change",
        treePath: ["src", "example.ts"],
        decoration: "L2",
        decorationTitle: "2–2",
        additions: 1,
        deletions: 1,
      },
      {
        anchor: "tour-entry-2",
        label: "src/example.ts · 18–18 +1 −1",
        kind: "change",
        treePath: ["src", "example.ts"],
        decoration: "L18",
        decorationTitle: "18–18",
        additions: 1,
        deletions: 1,
      },
    ],
    "contents",
  );
  assertEqual(
    buildTourContentsLayout(contents).groups[0]?.rows.map((row) =>
      row.kind === "directory" ? `directory:${row.label}` : `change:${row.item.anchor}`,
    ),
    ["directory:src", "change:tour-entry-1", "change:tour-entry-2"],
    "same-directory rows",
  );
});

run("tour filename trees preserve narrative order and file extensions", () => {
  const items = buildTourContents(
    {
      version: 1,
      chunks: [
        { header: "## Ordered changes" },
        { patch: patch(2, "zeta/really-long-name.test.ts") },
        { patch: patch(4, "alpha/first.go") },
        { patch: patch(8, "zeta/again.ts") },
      ],
    },
    false,
  );
  const layout = buildTourContentsLayout(items);
  assertEqual(
    layout.groups[0]?.rows.map((row) =>
      row.kind === "directory"
        ? `directory:${row.label}`
        : `change:${row.filenameStem}|${row.filenameSuffix}|${row.item.anchor}`,
    ),
    [
      "directory:zeta",
      "change:really-long-name|.test.ts|tour-entry-1",
      "directory:alpha",
      "change:first|.go|tour-entry-2",
      "directory:zeta",
      "change:again|.ts|tour-entry-3",
    ],
    "ordered rows",
  );
});

run("a tour without overview content starts at its first change", () => {
  const tour: GitTour = { version: 1, chunks: [{ patch: patch(7), trivial: true }] };
  assertEqual(
    buildTourContents(tour, false),
    [
      {
        anchor: "tour-entry-0",
        label: "src/example.ts · 7–7 +1 −1",
        kind: "change",
        treePath: ["src", "example.ts"],
        decoration: "L7",
        decorationTitle: "7–7",
        additions: 1,
        deletions: 1,
      },
    ],
    "contents",
  );
});

run("tour contents shows per-chunk counts for additions, deletions, and metadata changes", () => {
  const contents = buildTourContents(
    {
      version: 1,
      chunks: [
        {
          patch: ["--- /dev/null", "+++ b/added.ts", "@@ -0,0 +1,2 @@", "+first", "+second"].join(
            "\n",
          ),
        },
        {
          patch: ["--- a/deleted.ts", "+++ /dev/null", "@@ -1,2 +0,0 @@", "-first", "-second"].join(
            "\n",
          ),
        },
        {
          patch: ["diff --git a/script.sh b/script.sh", "old mode 100644", "new mode 100755"].join(
            "\n",
          ),
        },
      ],
    },
    false,
  );
  assertEqual(
    contents.map((item) => {
      if (item.kind !== "change") throw new Error("expected a change");
      return [item.label, item.decoration, item.additions, item.deletions];
    }),
    [
      ["added.ts · 1–2 +2", "L1–2", 2, 0],
      ["deleted.ts · 1–2 −2", "L1–2", 0, 2],
      ["script.sh", undefined, 0, 0],
    ],
    "change counts and compact labels",
  );
});
