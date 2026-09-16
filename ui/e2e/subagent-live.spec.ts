import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// Live subagent visualization: while a subagent conversation is working, the
// parent's subagent pill shows a live activity segment, its detail modal shows
// the full live strip, and the drawer badge shows running/total.
test("subagent pill shows live activity, detail, and drawer status", async ({ page, request }) => {
  test.setTimeout(120000);

  const slug = await createConversationViaAPI(request, "hello there");
  await page.goto(`/c/${slug}`);
  const input = page.getByTestId("message-input");
  await expect(input).toBeVisible({ timeout: 30000 });

  // Kick off a subagent that stays busy for a while.
  await input.fill("subagent: helper bash: sleep 120");
  await page.getByTestId("send-button").click();

  const pillLive = page.getByTestId("subagent-pill-live");
  await expect(pillLive).toBeVisible({ timeout: 30000 });
  await expect(pillLive).toContainText("sleep", { timeout: 30000 });

  // Drawer badge: 1 running of 1 total.
  await page.locator('button[aria-label="Open conversations"]').click();
  await expect(page.locator(".drawer.open")).toBeVisible();
  const badge = page.locator(".conversation-item.active .subagent-count-badge");
  await expect(badge).toBeVisible({ timeout: 15000 });
  await expect(badge).toContainText("1/1");
  await expect(badge.locator('[data-testid="subagent-badge-running"]')).toBeVisible();
  await page.locator('button[aria-label="Close conversations"]').click();
  await expect(page.locator(".drawer.open")).toHaveCount(0);

  // The live pill segment opens the subagent directly.
  await pillLive.click();
  await expect(page).toHaveURL(/\/c\/helper/, { timeout: 10000 });
  await page.goBack();
  const subagentPill = page.locator('.tool-pill[data-tool-name="subagent"]').first();
  await expect(subagentPill).toBeVisible({ timeout: 10000 });

  // The detail modal retains the full subagent card's live strip.
  await subagentPill.click();
  const cardLive = page.getByTestId("subagent-live");
  await expect(cardLive).toBeVisible({ timeout: 30000 });
  await expect(cardLive).toContainText("sleep", { timeout: 30000 });

  await cardLive.click();
  await expect(page).toHaveURL(/\/c\/helper/, { timeout: 10000 });
});
