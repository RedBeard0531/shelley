import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createConversationViaAPI, withTempDir } from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

async function openDiffViewer(page: Page, slug: string) {
  await page.setViewportSize({ width: 1800, height: 900 });
  await page.addInitScript(() => localStorage.setItem("diff-viewer-layout", "sidebar"));
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
  await page.locator(".overflow-menu-item", { hasText: /diffs/i }).click();
  const overlay = page.locator(".diff-viewer-overlay");
  await expect(overlay).toBeVisible({ timeout: 30000 });
  return overlay;
}

test.describe("Changes view", () => {
  test("shows every hunk on one page and default-collapses mechanical changes", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-changes-view-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(repo);
      git(repo, "init");
      git(repo, "config", "user.name", "Changes Test");
      git(repo, "config", "user.email", "changes@example.com");

      const appPath = join(repo, "src", "app.ts");
      mkdirSync(join(repo, "src"), { recursive: true });
      const lines = Array.from({ length: 20 }, (_, index) => `export const v${index} = ${index};`);
      writeFileSync(appPath, lines.join("\n") + "\n");
      git(repo, "add", "src/app.ts");
      git(repo, "commit", "-m", "Base commit\n\nPrompt: changes view base");

      // Two separated hunks in one file, plus a lock file.
      lines[1] = "export const v1 = 11;";
      lines[14] = "export const v14 = 77;";
      writeFileSync(appPath, lines.join("\n") + "\n");
      writeFileSync(join(repo, "go.sum"), "example.com/mod v1.0.0 h1:abc=\n");
      git(repo, "add", "src/app.ts", "go.sum");
      git(repo, "commit", "-m", "Update app and lock\n\nPrompt: changes view commit");

      // Leave the working tree dirty so the viewer opens on working changes.
      lines[2] = "export const v2 = 22;";
      writeFileSync(appPath, lines.join("\n") + "\n");

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug);

      // The viewer opens on dirty working changes, with Changes as the
      // default view. No tour is attached anywhere, so the switcher has
      // Changes and Files only.
      await expect(overlay.locator(".diff-viewer-view-tabs button")).toHaveText([
        "Changes",
        "Files",
      ]);
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText(
        "Changes",
      );

      // The working diff is a single hunk of app.ts, shown expanded.
      const tourView = overlay.locator(".commit-tour-view");
      await expect(tourView.locator(".commit-tour-chunk")).toHaveCount(1);
      await expect(tourView.locator(".commit-tour-chunk-header code")).toHaveText(/src\/app\.ts/);
      await expect(tourView.locator(".commit-tour-chunk-body")).toBeVisible();

      // Selecting the commit re-fetches without leaving the changes view.
      // The list binds commit..working by default; switch to the commit's own
      // diff to see both hunks.
      await overlay
        .locator(".diff-viewer-commit-list button", { hasText: "Update app and lock" })
        .click();
      await overlay.getByRole("radio", { name: "Single commit" }).click();
      await expect(tourView.locator(".commit-tour-chunk")).toHaveCount(2);

      // The real edit is expanded; the lock file's chunk is mechanical and
      // starts collapsed behind a "trivial" toggle.
      const chunks = tourView.locator(".commit-tour-chunk");
      const appChunk = chunks.filter({ hasText: "src/app.ts" });
      const trivialChunk = chunks.filter({ hasText: "go.sum" });
      await expect(appChunk).toHaveCount(1);
      await expect(trivialChunk).toHaveCount(1);
      await expect(appChunk.locator(".commit-tour-chunk-header code")).toHaveText("src/app.ts");
      await expect(appChunk.locator(".commit-tour-chunk-body")).toBeVisible();
      await expect(trivialChunk.locator(".tour-trivial-label")).toHaveText("generated");
      await expect(trivialChunk.locator(".commit-tour-chunk-body")).toHaveCount(0);

      // The sidebar table of contents lists both files under their directory.
      const contents = overlay.getByRole("navigation", { name: "Tour contents" });
      await expect(
        contents.locator(".tour-file-tree-directory .diff-tree-label").first(),
      ).toHaveText("src");
      await expect(contents.locator(".tour-file-heading", { hasText: "go.sum" })).toBeVisible();

      // Expanding the trivial chunk reveals its diff.
      await trivialChunk.locator(".commit-tour-chunk-toggle").click();
      await expect(trivialChunk.locator(".commit-tour-chunk-body")).toBeVisible();

      // Switching to Files and back keeps the changes view working.
      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Files" }).click();
      await expect(overlay.locator(".diff-tree")).toBeVisible();
      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Changes" }).click();
      await expect(tourView.locator(".commit-tour-chunk")).toHaveCount(2);
      await expect(trivialChunk.locator(".commit-tour-chunk-body")).toBeVisible();
    });
  });

  test("labels renames from → to, collapsed when pure and expanded when edited", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-changes-rename-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(repo);
      git(repo, "init");
      git(repo, "config", "user.name", "Rename Test");
      git(repo, "config", "user.email", "rename@example.com");

      const appPath = join(repo, "src", "app.ts");
      mkdirSync(join(repo, "src"), { recursive: true });
      // Enough lines that a one-line edit still reads as a rename to git.
      const lines = Array.from({ length: 20 }, (_, index) => `export const v${index} = ${index};`);
      writeFileSync(appPath, lines.join("\n") + "\n");
      git(repo, "add", "src/app.ts");
      git(repo, "commit", "-m", "Base commit\n\nPrompt: rename base");

      // A pure rename: one hunkless fragment, mechanically trivial.
      git(repo, "mv", "src/app.ts", "src/renamed.ts");
      git(repo, "commit", "-am", "Pure rename\n\nPrompt: rename pure");

      // A rename with a small edit: not mechanical, so it stays expanded —
      // but the header must still show the from → to move.
      git(repo, "mv", "src/renamed.ts", "src/moved.ts");
      lines[1] = "export const v1 = 11;";
      writeFileSync(join(repo, "src", "moved.ts"), lines.join("\n") + "\n");
      git(repo, "commit", "-am", "Rename and edit\n\nPrompt: rename edit");

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug);

      // The viewer opens on HEAD (clean working tree): the rename-and-edit
      // commit, expanded with the from → to header and the rendered diff.
      const tourView = overlay.locator(".commit-tour-view");
      await expect(tourView.locator(".commit-tour-chunk")).toHaveCount(1);
      const editedChunk = tourView.locator(".commit-tour-chunk");
      await expect(editedChunk.locator(".commit-tour-chunk-header code")).toHaveText(
        /src\/renamed\.ts → src\/moved\.ts · \d+–\d+/,
      );
      await expect(editedChunk.locator(".tour-trivial-label")).toHaveCount(0);
      await expect(editedChunk.locator(".commit-tour-chunk-body")).toBeVisible();

      // The pure rename commit shows a collapsed chunk labeled from → to,
      // with a "renamed" chip and no +0 −0 stat noise.
      await overlay.locator(".diff-viewer-commit-list button", { hasText: "Pure rename" }).click();
      await overlay.getByRole("radio", { name: "Single commit" }).click();
      await expect(tourView.locator(".commit-tour-chunk")).toHaveCount(1);
      const pureChunk = tourView.locator(".commit-tour-chunk");
      await expect(pureChunk.locator(".commit-tour-chunk-header code")).toHaveText(
        "src/app.ts → src/renamed.ts",
      );
      await expect(pureChunk.locator(".tour-trivial-label")).toHaveText("renamed");
      await expect(pureChunk.locator(".commit-tour-chunk-stats")).toHaveCount(0);
      await expect(pureChunk.locator(".commit-tour-chunk-body")).toHaveCount(0);

      // Expanding reveals the raw rename metadata.
      await pureChunk.locator(".commit-tour-chunk-toggle").click();
      await expect(pureChunk.locator(".commit-tour-chunk-body")).toBeVisible();
      await expect(pureChunk.locator(".commit-tour-chunk-body pre")).toContainText(
        "rename from src/app.ts",
      );
    });
  });

  test("layout toggles live in the switcher row and chunk headers stick", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-changes-toolbar-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(repo);
      git(repo, "init");
      git(repo, "config", "user.name", "Toolbar Test");
      git(repo, "config", "user.email", "toolbar@example.com");

      const lines = Array.from({ length: 120 }, (_, index) => `line ${index + 1}`);
      const appPath = join(repo, "src", "app.ts");
      mkdirSync(join(repo, "src"), { recursive: true });
      writeFileSync(appPath, lines.join("\n") + "\n");
      git(repo, "add", "src/app.ts");
      git(repo, "commit", "-m", "Base commit\n\nPrompt: toolbar base");

      // Two hunks far apart: the card is taller than a shrunk viewport, so its
      // header has a body to stick over.
      lines[20] = "line 21 updated";
      lines[100] = "line 101 updated";
      writeFileSync(appPath, lines.join("\n") + "\n");
      git(repo, "commit", "-am", "Update lines\n\nPrompt: toolbar commit");

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug);

      // The layout toggles share the fixed switcher row, costing no extra
      // vertical space, and the side-by-side toggle is desktop-only.
      const switcher = overlay.locator(".diff-viewer-view-switcher");
      const overflowToggle = switcher.getByRole("button", { name: /long lines/ });
      await expect(overflowToggle).toHaveText("Scroll");
      await expect(switcher.getByRole("button", { name: /diffs/ })).toHaveText("Side-by-side");

      // Shrink the viewport until the single page overflows, then scroll into
      // the diff card's body: its header must stay pinned to the pane's top
      // rather than scrolling away with the diff.
      await page.setViewportSize({ width: 1200, height: 300 });
      const view = overlay.locator(".commit-tour-view");
      const header = overlay.locator(".commit-tour-chunk-header");
      await expect
        .poll(() => view.evaluate((element) => element.scrollHeight > element.clientHeight))
        .toBe(true);
      const { requested, headerOffset } = await view.evaluate((element) => {
        const headerEl = element.querySelector<HTMLElement>(".commit-tour-chunk-header")!;
        const requested =
          element.scrollTop +
          headerEl.getBoundingClientRect().top -
          element.getBoundingClientRect().top +
          40;
        element.scrollTop = requested;
        return {
          requested,
          headerOffset: Math.round(
            headerEl.getBoundingClientRect().top - element.getBoundingClientRect().top,
          ),
        };
      });
      // The scroll must land inside the card rather than clamping short, so
      // that without sticky positioning the header would sit 40px above the
      // pane's top and this check would fail.
      expect(requested).toBeLessThanOrEqual(
        await view.evaluate((element) => element.scrollHeight - element.clientHeight),
      );
      expect(headerOffset).toBeGreaterThanOrEqual(-1);
      expect(headerOffset).toBeLessThanOrEqual(1);
      await expect(header).toBeVisible();

      // The toggles live in the fixed chrome, so they stay visible while
      // scrolled. Toggling switches to wrapped lines.
      await expect(overflowToggle).toBeVisible();
      await overflowToggle.click();
      await expect(overflowToggle).toHaveText("Wrap");
    });
  });
});
