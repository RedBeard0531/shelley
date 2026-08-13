import { test, expect } from "@playwright/test";
import { writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPI, withTempDir } from "./helpers";

// The fuzzy file finder (Cmd/Ctrl+Shift+P) ANDs whitespace-separated terms, so
// a half-remembered filename typed as words finds the file: "vm storage s3"
// must reach vm-storage-s3-backup-design.md even though the literal query
// (spaces and all) never appears in the path.

test.describe("File finder multi-term search", () => {
  test("space-separated terms are ANDed", async ({ page, request }) => {
    await withTempDir("shelley-finder-", async (dir) => {
      mkdirSync(join(dir, "docs"), { recursive: true });
      for (const name of [
        "vm-storage-s3-backup-design.md",
        "vm-storage-replication.md",
        "s3-uploads.md",
      ]) {
        writeFileSync(join(dir, "docs", name), "x\n");
      }

      const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });
      await finderInput.fill("vm storage s3");

      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(1, { timeout: 10000 });
      await expect(rows.first()).toContainText("docs/vm-storage-s3-backup-design.md");
      // Each term underlines its literal run rather than a scattered subsequence.
      await expect(rows.first().locator("mark")).toHaveText(["vm", "storage", "s3"]);

      // A single term still matches every file containing it.
      await finderInput.fill("vm-storage");
      await expect(rows).toHaveCount(2, { timeout: 10000 });
    });
  });
});
