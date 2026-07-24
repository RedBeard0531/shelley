import { test, expect } from "@playwright/test";
import { createConversationViaAPI, setPageFeatureFlag } from "./helpers";

// The performance-hud feature flag overlays live recomputation counters
// (see ui/src/utils/perf.ts). The counters themselves are always collected
// and exposed at window.__shelleyPerf; the flag only controls the overlay.
test.describe("Performance HUD", () => {
  test("hidden by default, __shelleyPerf still available", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "echo perf-hud-off");
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await expect(page.locator(".perf-hud")).toHaveCount(0);
    const counters = await page.evaluate(() => {
      const perf = (window as unknown as { __shelleyPerf?: { snapshot(): object } }).__shelleyPerf;
      return perf ? Object.keys(perf.snapshot()) : null;
    });
    expect(counters).not.toBeNull();
    expect(counters!.length).toBeGreaterThan(0);
  });

  test("flag shows the HUD with live counters", async ({ page, request }) => {
    await setPageFeatureFlag(page, "performance-hud", true);
    const slug = await createConversationViaAPI(request, "echo perf-hud-on");
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    const hud = page.locator(".perf-hud");
    await expect(hud).toBeVisible({ timeout: 10000 });
    // Loading a conversation mounts Message components, so the table should
    // list at least one counter row within a poll interval.
    await expect(hud.locator("tbody tr").first()).toBeVisible({ timeout: 5000 });
    await expect(hud.locator(".perf-hud-empty")).toHaveCount(0);

    // Collapse toggles to the mini summary. Click the title: the header's
    // right side holds reset/copy/pause buttons that stop propagation.
    await hud.locator(".perf-hud-title").click();
    await expect(hud).toHaveClass(/collapsed/);
    await expect(hud.locator(".perf-hud-mini")).toBeVisible();
    await hud.locator(".perf-hud-title").click();

    // Long tasks (>50ms main-thread blocks) get their own section. Reset so
    // page-load counters don't crowd the top-4 suspects list, register a
    // sentinel counter, then block the main thread; the sentinel should be
    // reported as a suspect for the resulting long task. The block must run
    // in a real event-loop task (setTimeout): work executed directly inside
    // a DevTools-protocol evaluate is not reported by the Longtask API.
    await page.evaluate(
      () =>
        new Promise<void>((resolve) => {
          const perf = (
            window as unknown as {
              __shelleyPerf: { reset(): void; count(name: string): void };
            }
          ).__shelleyPerf;
          perf.reset();
          perf.count("test.suspect");
          setTimeout(() => {
            const t0 = performance.now();
            while (performance.now() - t0 < 80) {
              /* block the main thread */
            }
            resolve();
          }, 0);
        }),
    );
    await expect(hud.locator(".perf-hud-longtask").first()).toBeVisible({ timeout: 5000 });
    await expect(
      hud.locator(".perf-hud-longtask-suspects", { hasText: "test.suspect" }),
    ).toBeVisible();
  });
});
