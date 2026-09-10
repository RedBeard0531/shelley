import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createConversationViaAPIWithDetails, withTempDir } from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

test("git info shows and opens an attached commit tour", async ({ page, request }) => {
  await withTempDir("shelley-gitinfo-tour-", async (tempDir) => {
    const repo = join(tempDir, "repo");
    mkdirSync(repo);
    git(repo, "init");
    git(repo, "config", "user.name", "Tour Test");
    git(repo, "config", "user.email", "tour@example.com");

    writeFileSync(join(repo, "example.txt"), "before\n");
    git(repo, "add", "example.txt");
    git(repo, "commit", "-m", "Base commit", "-m", "Prompt: prepare the gitinfo tour fixture");
    git(repo, "branch", "upstream");
    git(repo, "switch", "-c", "feature");
    git(repo, "branch", "--set-upstream-to=upstream", "feature");

    const { slug } = await createConversationViaAPIWithDetails(
      request,
      "bash: printf 'after\\n' > example.txt && git add example.txt && git commit --no-verify -m 'Tour this commit' -m 'Prompt: create a commit with a guided tour'",
      { cwd: repo },
    );
    const hash = git(repo, "rev-parse", "HEAD");
    const shortHash = hash.slice(0, 7);

    const scaffold = JSON.parse(
      execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, hash], { encoding: "utf8" }),
    );
    const tourPath = join(tempDir, "tour.json");
    writeFileSync(
      tourPath,
      JSON.stringify({
        ...scaffold,
        title: "Tour this commit",
        intro: "Follow the commit directly from its chat status line.",
        chunks: scaffold.chunks.map((chunk: { ref: number }) => ({
          ...chunk,
          comment: "The guided change.",
        })),
      }),
    );

    await page.setViewportSize({ width: 1280, height: 900 });
    const missingProbe = page.waitForResponse(
      (response) =>
        response.request().method() === "HEAD" &&
        response.url().includes("/api/git/tour?") &&
        response.status() === 404,
    );
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await missingProbe;

    const gitInfo = page.getByTestId("message-gitinfo").filter({ hasText: shortHash });
    const tourLink = gitInfo.getByRole("link", { name: "tour", exact: true });
    await expect(tourLink).toHaveCount(0);

    execFileSync(shelleyBin, ["tour", "attach", "-C", repo, hash, tourPath]);
    git(repo, "reset", "--hard", "upstream");
    await gitInfo.hover();
    await expect(tourLink).toBeVisible();
    await expect(tourLink).toHaveAttribute("href", /[?&]diff=/);

    await tourLink.click();
    const overlay = page.locator(".diff-viewer-overlay");
    await expect(overlay).toBeVisible({ timeout: 30_000 });
    await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Tour");
    await expect(overlay.locator(".commit-tour-introduction h1")).toHaveText("Tour this commit");
  });
});
