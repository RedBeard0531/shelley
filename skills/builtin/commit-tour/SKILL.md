---
name: commit-tour
description: Use when the user or project guidance (AGENTS.md) asks for a "commit tour", annotated commits, or commit annotations. Produces a narrative walkthrough of a commit's diff, stored as a git note and rendered in Shelley's diff UI.
---

A commit guided tour is a JSON annotation of a commit's diff, stored in the git
note ref `shelley-tour`. Shelley's diff UI shows a badge on annotated commits
and renders the tour: important changes first with commentary, trivial changes
collapsed.

## Workflow

Annotate a commit right after making it. Delegate to a subagent when the commit
is large; pass it the commit hash, the repo directory, and this skill.

1. Get suggested patch fragments:

   ```
   shelley tour chunks [-C <repo-dir>] <commit>
   ```

   This prints the full `hash` and a `chunks` array of `{"patch":"..."}`
   objects. Each suggestion is a self-contained, git-apply-able fragment: its
   file headers plus one hunk, or the header block alone for a hunkless binary,
   rename, or mode change.

2. Write the tour JSON to a temp file:

   ```json
   {
     "version": 1,
     "title": "Short plain-text title",
     "intro": "Markdown intro: what this commit does and why.",
     "chunks": [
       {"header": "## The data model"},
       {"patch": "diff --git a/server/model.go b/server/model.go\n...", "comment": "Why this shape matters."},
       {"header": "## Supporting changes"},
       {"patch": "diff --git a/go.sum b/go.sum\n...", "trivial": true}
     ]
   }
   ```

   Every entry has exactly one non-empty `header` or `patch`. Patch entries may
   also have `comment` and `trivial`.

3. Verify, then attach:

   ```
   shelley tour verify [-C <repo-dir>] <commit> tour.json
   shelley tour attach [-C <repo-dir>] <commit> tour.json
   ```

   `verify` applies all patch entries to the commit's parent tree and requires
   the exact commit tree. Gaps, overlaps, edited lines, or bad headers fail with
   Git's error text. `attach` verifies and writes the note (re-running
   overwrites). `shelley tour show <commit>` prints an existing tour.

## Writing a good tour

- **Data model first.** Open with changed types, schemas, formats, or APIs so
  readers understand the shapes involved.
- **Important bits first.** After the data model, order entries by importance, not file
  order: core logic and decisions, tests, then generated files, lock files,
  imports, boilerplate, and renames.
- **Cover the whole diff.** Verification reconstructs the commit tree from the
  parent, so every change must appear exactly once.
- Split large suggested hunks into logical patch entries by copying the file
  header block and rewriting each slice's `@@` header. Old starts use parent
  coordinates; new starts use final-file coordinates. Zero context is fine.
  Keep each `\ No newline at end of file` marker with its hunk's last line.
- Entries may be reordered freely, including slices from the same original
  hunk.
- Mark boring patch entries `"trivial": true`; they render collapsed and need
  no comment.
- Use `{"header": "## ..."}` entries as markdown section headings.
- Comments are markdown. Explain why and what to notice rather than restating
  the diff; one to three sentences is usually enough.
- `intro` should state the problem, the approach, and the map of the tour.
- Amending changes the commit hash; re-attach the tour afterward.
