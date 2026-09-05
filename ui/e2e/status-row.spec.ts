import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConversationViaAPIWithDetails } from "./helpers";

test("TOC shares the model row and working text yields to controls", async ({ page, request }) => {
  const { slug, conversationId } = await createConversationViaAPIWithDetails(
    request,
    "echo contents row",
  );
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/c/${slug}`);
  const toc = page.getByRole("button", { name: "Conversation table of contents", exact: true });
  await expect(toc).toBeVisible();
  await toc.evaluate((el) => {
    el.dataset.mountIdentity = "original";
  });
  await expect(page.locator(".chat-nav-cluster .toc-button")).toHaveCount(0);

  // Block on a pipe, not a timer: the turn stays working until Stop is clicked.
  const dir = mkdtempSync(join(tmpdir(), "shelley-status-row-"));
  const pipe = join(dir, "gate");
  execFileSync("mkfifo", [pipe]);
  try {
    await page.getByTestId("message-input").fill(`bash: read -r line < ${pipe}`);
    await page.getByTestId("send-button").click();
    await expect(page.getByTestId("agent-thinking")).toBeVisible();
    const seen = new Set<string>();
    for (const width of [
      1280, 900, 800, 700, 650, 600, 575, 550, 525, 500, 460, 420, 390, 360, 320,
    ]) {
      await page.setViewportSize({ width, height: 800 });
      await expect(async () => {
        const geometry = await page.evaluate(() => {
          const rect = (selector: string) => {
            const el = [...document.querySelectorAll<HTMLElement>(selector)].find(
              (e) => e.offsetWidth > 0,
            )!;
            if (!el) throw new Error(`Missing visible ${selector} at ${window.innerWidth}px`);
            const r = el.getBoundingClientRect();
            return {
              left: r.left,
              right: r.right,
              top: r.top,
              bottom: r.bottom,
              overflow: el.scrollWidth > el.clientWidth + 1,
            };
          };
          const text = document.querySelector<HTMLElement>(".animated-working")!;
          return {
            toc: rect(".toc-button"),
            model: rect(".status-readout-model"),
            status: rect(".animated-working"),
            stop: rect(".status-stop-button"),
            row: rect(".status-bar-active"),
            tokens: rect(".context-usage-label-tokens"),
            text: [...text.querySelectorAll('span[aria-hidden="true"]')]
              .filter((e) => e.getClientRects().length > 0)
              .map((e) => e.textContent)
              .join(""),
          };
        });
        expect(geometry.toc.left).toBeGreaterThanOrEqual(geometry.model.right);
        expect(
          Math.abs(
            (geometry.toc.top + geometry.toc.bottom - geometry.model.top - geometry.model.bottom) /
              2,
          ),
        ).toBeLessThan(2);
        expect(geometry.toc.right).toBeLessThanOrEqual(width);
        expect(geometry.status.bottom - geometry.status.top).toBeLessThan(24);
        expect(geometry.stop.right).toBeLessThanOrEqual(geometry.status.left);
        expect(geometry.row.overflow).toBe(false);
        expect(geometry.tokens.overflow).toBe(false);
        expect(["Agent working...", "working...", "..."]).toContain(geometry.text);
        seen.add(geometry.text);
      }).toPass({ timeout: 5000 });
      await expect(toc).toHaveAttribute("data-mount-identity", "original");
      await toc.click();
      await expect(page.locator(".toc-popover")).toBeVisible();
      await page.keyboard.press("Escape");
    }
    expect(seen).toEqual(new Set(["Agent working...", "working...", "..."]));
    // A narrow conversation pane on a desktop must shorten the label too.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.locator(".main-content").evaluate((el) => {
      el.style.maxWidth = "320px";
    });
    await expect(page.locator(".working-prefix").first()).toBeHidden();
    await expect(page.locator(".working-word").first()).toBeHidden();
    await expect(page.locator(".animated-working .sr-only")).toBeVisible();
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(page.getByTestId("agent-thinking")).toBeHidden();
    await expect(toc).toBeVisible();
    await page.setViewportSize({ width: 390, height: 800 });
    expect((await request.post(`/api/conversation/${conversationId}/archive`)).ok()).toBe(true);
    await page.reload();
    await expect(page.getByTestId("message-input")).toBeHidden();
    await expect(page.locator(".toc-button")).toHaveCount(1);
    await expect(toc).toBeVisible();
  } finally {
    await request.post(`/api/conversation/${conversationId}/cancel`);
    rmSync(dir, { recursive: true, force: true });
  }
});
