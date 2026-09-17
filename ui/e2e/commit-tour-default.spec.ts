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

test.describe("Commit tour defaults", () => {
  test("a clean HEAD tour opens by default with tour contents in the sidebar", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-tour-viewer-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(repo);
      git(repo, "init");
      git(repo, "config", "user.name", "Tour Test");
      git(repo, "config", "user.email", "tour@example.com");

      const lines = Array.from({ length: 220 }, (_, index) => `line ${index + 1}`);
      const examplePath = join(repo, "src", "nested", "example.spec.ts");
      mkdirSync(join(repo, "src", "nested"), { recursive: true });
      writeFileSync(examplePath, lines.join("\n") + "\n");
      git(repo, "add", "src/nested/example.spec.ts");
      git(repo, "commit", "-m", "Base commit\n\nPrompt: tour viewer base");
      git(repo, "branch", "upstream");
      git(repo, "switch", "-c", "feature");
      git(repo, "branch", "--set-upstream-to=upstream", "feature");

      lines[1] = "updated near the top";
      lines[198] = "updated near the bottom";
      writeFileSync(examplePath, lines.join("\n") + "\n");
      git(repo, "add", "src/nested/example.spec.ts");
      git(repo, "commit", "-m", "Tour the top commit\n\nPrompt: tour viewer feature");

      const scaffold = JSON.parse(
        execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, "HEAD"], {
          encoding: "utf8",
        }),
      );
      expect(scaffold.chunks).toHaveLength(2);
      const tour = {
        ...scaffold,
        title: "Tour the top commit",
        intro: Array.from(
          { length: 24 },
          (_, index) => `Overview paragraph ${index + 1} explains the guided change.`,
        ).join("\n\n"),
        chunks: [
          { header: "## Core behavior" },
          ...Array.from({ length: 12 }, (_, index) => ({
            header: `### Detail ${index + 1}`,
          })),
          ...scaffold.chunks.map((chunk: { ref: number }, index: number) => ({
            ...chunk,
            comment: `Guided change ${index + 1}.`,
          })),
        ],
      };
      const tourPath = join(tempDir, "tour.json");
      writeFileSync(tourPath, JSON.stringify(tour));
      execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", tourPath]);

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      await page.setViewportSize({ width: 1800, height: 900 });
      await page.addInitScript(() => localStorage.setItem("diff-viewer-layout", "sidebar"));
      await page.goto(`/c/${slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
      await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
      await page.locator(".overflow-menu-item", { hasText: /diffs/i }).click();

      const overlay = page.locator(".diff-viewer-overlay");
      await expect(overlay).toBeVisible({ timeout: 30000 });
      await expect(overlay.getByRole("radio", { name: "Single commit" })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Tour");
      await expect(overlay.locator(".commit-tour-introduction h1")).toHaveText(
        "Tour the top commit",
      );

      const workingRow = overlay.locator(".diff-viewer-commit-list-item.working");
      await expect(workingRow.locator(".working-changes-status")).toHaveText("Clean");
      await expect(workingRow).toHaveAttribute("title", "No working changes");

      await expect(overlay.locator(".diff-viewer-toast-hint")).toHaveCount(0);
      await expect(overlay.getByText("Table of Contents", { exact: true })).toBeVisible();
      const contents = overlay.getByRole("navigation", { name: "Tour contents" });
      const tourContentsScroll = overlay.locator(".diff-viewer-sidebar-tour-scroll");
      const tourView = overlay.locator(".commit-tour-view");
      const overview = contents.getByRole("button", { name: "Overview" });
      await expect(overview).toBeVisible();
      await page.setViewportSize({ width: 1800, height: 6000 });
      await expect
        .poll(() => tourView.evaluate((element) => element.scrollHeight <= element.clientHeight))
        .toBe(true);
      await expect(overview).toHaveAttribute("aria-current", "location");
      await expect
        .poll(() => overview.evaluate((element) => getComputedStyle(element).boxShadow))
        .toBe("none");
      await page.setViewportSize({ width: 1800, height: 900 });
      await expect(overview).toHaveAttribute("aria-current", "location");
      const section = contents.getByRole("button", { name: "Core behavior" });
      await expect(section).toBeVisible();
      const changesTree = contents.getByRole("list", { name: "Changes in Detail 12" });
      await expect(changesTree.locator(".tour-file-tree-directory .diff-tree-label")).toHaveText(
        "src / nested",
      );
      const changeButtons = changesTree.getByRole("button", { name: /example\.spec\.ts/ });
      await expect(changeButtons).toHaveCount(2);
      for (const button of await changeButtons.all()) {
        await expect(button).toHaveAccessibleName(/example\.spec\.ts · \d+–\d+ \+1 −1$/);
        await expect(button).not.toHaveAttribute("title", /lines/);
        await expect(button.locator(".diff-tree-decoration")).toHaveText(/^L\d+–\d+$/);
        await expect(button.locator(".diff-tree-decoration")).toHaveCSS(
          "background-color",
          "rgba(0, 0, 0, 0)",
        );
        await expect(button.locator(".diff-tree-changes-added")).toHaveText("+1");
        await expect(button.locator(".diff-tree-changes-deleted")).toHaveText("−1");
      }
      await expect(overlay.locator(".commit-tour-chunk-header code").first()).toHaveText(
        /^src\/nested\/example\.spec\.ts · \d+–\d+$/,
      );
      await expect(changesTree.locator(".tour-file-name-stem", { hasText: "example" })).toHaveCount(
        2,
      );
      await expect(
        changesTree.locator(".tour-file-name-suffix", { hasText: ".spec.ts" }),
      ).toHaveCount(2);
      await expect(overlay.locator(".diff-tree")).toHaveCount(0);

      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Files" }).click();
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Files");
      await expect(overlay.locator(".diff-tree")).toBeVisible();
      await expect(overlay.getByRole("navigation", { name: "Tour contents" })).toHaveCount(0);
      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Tour" }).click();
      await expect(overlay.getByRole("navigation", { name: "Tour contents" })).toBeVisible();
      await expect(overlay.locator(".diff-viewer-toast-hint")).toHaveCount(0);

      const tourDocument = overlay.locator(".commit-tour-document");
      await expect
        .poll(async () => {
          const [viewBox, documentBox] = await Promise.all([
            tourView.boundingBox(),
            tourDocument.boundingBox(),
          ]);
          if (!viewBox || !documentBox) return 0;
          return documentBox.width / viewBox.width;
        })
        .toBeGreaterThan(0.97);

      await section.click();
      await expect
        .poll(() => tourView.evaluate((element) => element.scrollTop))
        .toBeGreaterThan(100);
      await expect(section).toHaveAttribute("aria-current", "location");
      await expect
        .poll(() =>
          overlay.locator("#tour-entry-0").evaluate((target) => {
            const view = target.closest(".commit-tour-view");
            if (!view) return Number.POSITIVE_INFINITY;
            return Math.abs(target.getBoundingClientRect().top - view.getBoundingClientRect().top);
          }),
        )
        .toBeLessThan(40);

      const firstChunk = overlay.locator(".commit-tour-chunk").first();
      const firstChangeButton = changesTree
        .getByRole("button", { name: /example\.spec\.ts/ })
        .first();
      await firstChunk.evaluate((element) => element.scrollIntoView({ block: "start" }));
      await expect(firstChangeButton).toHaveAttribute("aria-current", "location");
      await expect(firstChangeButton.locator(".diff-tree-decoration")).toHaveCSS(
        "background-color",
        "rgba(0, 0, 0, 0)",
      );
      await page.setViewportSize({ width: 1800, height: 600 });
      await expect
        .poll(async () => {
          const [containerBox, buttonBox] = await Promise.all([
            tourContentsScroll.boundingBox(),
            firstChangeButton.boundingBox(),
          ]);
          if (!containerBox || !buttonBox) return false;
          return (
            buttonBox.y >= containerBox.y - 1 &&
            buttonBox.y + buttonBox.height <= containerBox.y + containerBox.height + 1
          );
        })
        .toBe(true);
      await page.setViewportSize({ width: 1800, height: 900 });
      await tourView.evaluate((element) => {
        element.scrollTop = element.scrollHeight;
      });
      const lastChangeButton = changesTree
        .getByRole("button", { name: /example\.spec\.ts/ })
        .nth(1);
      await expect(lastChangeButton).toHaveAttribute("aria-current", "location");
      await expect
        .poll(async () => {
          const [containerBox, buttonBox] = await Promise.all([
            tourContentsScroll.boundingBox(),
            lastChangeButton.boundingBox(),
          ]);
          if (!containerBox || !buttonBox) return false;
          return (
            buttonBox.y >= containerBox.y - 1 &&
            buttonBox.y + buttonBox.height <= containerBox.y + containerBox.height + 1
          );
        })
        .toBe(true);

      const pierreDiff = firstChunk.locator("diffs-container");
      await expect(pierreDiff).toBeVisible();
      await expect
        .poll(() =>
          pierreDiff.evaluate((element) => {
            const pre = element.shadowRoot?.querySelector("pre");
            if (!pre) return null;
            const style = window.getComputedStyle(pre);
            return {
              fontSize: style.fontSize,
              lineHeight: style.lineHeight,
              usesUiMonospace: style.fontFamily.startsWith("ui-monospace"),
            };
          }),
        )
        .toEqual({ fontSize: "12px", lineHeight: "18px", usesUiMonospace: true });
    });
  });
});
