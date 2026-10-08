import { test, expect } from "@playwright/test";
import { openWorkspaceTool, createConversationViaAPI } from "./helpers";

test.describe("Git graph diffstat links", () => {
  test("opens the selected changed file in the diff viewer", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 1280, height: 900 });

    const cwdResp = await request.get("/api/git/diffs?cwd=.");
    expect(cwdResp.ok()).toBeTruthy();
    const { gitRoot } = await cwdResp.json();
    const slug = await createConversationViaAPI(request, "Hello", { cwd: gitRoot });

    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openWorkspaceTool(page, "Git Graph");

    const graph = page.locator(".git-graph-container");
    await expect(graph).toBeVisible({ timeout: 30000 });
    const fileLink = graph.locator(".git-graph-diffstat-link").first();
    await expect(fileLink).toBeVisible({ timeout: 30000 });
    const filePath = await fileLink.locator(".git-graph-diffstat-path").textContent();
    expect(filePath).toBeTruthy();
    await expect(fileLink).toHaveAttribute(
      "href",
      new RegExp(`[?&]file=${encodeURIComponent(filePath!)}`),
    );

    await fileLink.click();

    const overlay = page.locator(".diff-viewer-overlay");
    await expect(overlay.locator("select.diff-viewer-select").first()).toHaveValue(filePath!, {
      timeout: 30000,
    });
  });
});
