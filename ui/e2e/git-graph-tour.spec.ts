import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createConversationViaAPI, withTempDir } from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

test("git graph shows and opens an attached commit tour", async ({ page, request }) => {
  await withTempDir("shelley-git-graph-tour-", async (tempDir) => {
    const repo = join(tempDir, "repo");
    mkdirSync(repo);
    git(repo, "init");
    git(repo, "config", "user.name", "Tour Test");
    git(repo, "config", "user.email", "tour@example.com");

    writeFileSync(join(repo, "example.txt"), "before\n");
    git(repo, "add", "example.txt");
    git(repo, "commit", "-m", "Base commit", "-m", "Prompt: graph tour base");
    writeFileSync(join(repo, "example.txt"), "after\n");
    git(repo, "add", "example.txt");
    git(repo, "commit", "-m", "Tour from graph", "-m", "Prompt: graph tour commit");
    const hash = git(repo, "rev-parse", "HEAD");
    writeFileSync(join(repo, "tip.txt"), "tip\n");
    git(repo, "add", "tip.txt");
    git(repo, "commit", "-m", "Tip commit", "-m", "Prompt: graph tour tip");

    const scaffold = JSON.parse(
      execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, hash], { encoding: "utf8" }),
    );
    const tourPath = join(tempDir, "tour.json");
    writeFileSync(
      tourPath,
      JSON.stringify({
        ...scaffold,
        title: "Tour from graph",
        intro: "Open this tour directly from the git graph.",
        chunks: scaffold.chunks.map((chunk: { ref: number }) => ({
          ...chunk,
          comment: "The guided change.",
        })),
      }),
    );
    execFileSync(shelleyBin, ["tour", "attach", "-C", repo, hash, tourPath]);

    const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
    await page.locator(".overflow-menu-item", { hasText: /git graph/i }).click();

    const graph = page.locator(".git-graph-container");
    await expect(graph).toBeVisible({ timeout: 30_000 });
    const tourRow = graph.locator(".git-graph-row", { hasText: "Tour from graph" });
    const baseRow = graph.locator(".git-graph-row", { hasText: "Base commit" });
    const tipRow = graph.locator(".git-graph-row", { hasText: "Tip commit" });
    const tourLink = tourRow.getByTestId("git-graph-tour-link");
    await expect(tourLink).toHaveText("tour");
    await expect(tourLink).toHaveAttribute("href", new RegExp(`[?&]diff=${hash}`));
    await expect(baseRow.getByTestId("git-graph-tour-link")).toHaveCount(0);
    await expect(tipRow.getByTestId("git-graph-tour-link")).toHaveCount(0);
    await expect(graph.locator(".git-graph-row", { hasText: "Notes added by" })).toHaveCount(0);
    await expect(graph.getByRole("link", { name: "Open diff" })).toBeVisible();

    await graph.getByRole("button", { name: "Current branch" }).focus();
    await page.keyboard.press("ArrowDown");
    await expect(tourRow).toHaveClass(/git-graph-row-selected/);
    await expect(graph.getByRole("link", { name: "Open tour" })).toBeVisible();
    await page.keyboard.press("ArrowUp");
    await expect(tipRow).toHaveClass(/git-graph-row-selected/);

    await page.setViewportSize({ width: 390, height: 844 });
    await graph.getByRole("button", { name: "Close commit details" }).click();
    await expect(graph.locator(".git-graph-detail-sheet-open")).toHaveCount(0);
    await tourLink.focus();
    await page.keyboard.press("Enter");
    const overlay = page.locator(".diff-viewer-overlay").last();
    await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Tour", {
      timeout: 30_000,
    });
    await expect(overlay.locator(".commit-tour-introduction h1")).toHaveText("Tour from graph");
    await page.keyboard.press("ArrowDown");
    await expect(overlay.locator(".commit-tour-introduction h1")).toHaveText("Tour from graph");

    await overlay.getByRole("button", { name: "Close (Esc)" }).click();
    await expect(tourRow).toHaveClass(/git-graph-row-selected/);
    await expect(graph.locator(".git-graph-detail-sheet-open")).toHaveCount(0);
  });
});
