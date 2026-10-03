import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test("drawer badge lists running background jobs and kills one", async ({ page, request }) => {
  const slug = await createConversationViaAPI(request, "bash-bg: sleep 600");
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

  await page.locator('button[aria-label="Open conversations"]').click();
  await expect(page.locator(".drawer.open")).toBeVisible();
  const badge = page.locator(".conversation-item.active").getByTestId("background-jobs-badge");
  await expect(badge).toHaveText("1", { timeout: 15000 });

  // Opening the list must not navigate away.
  const url = page.url();
  await badge.click();
  const job = page.getByTestId("background-job");
  await expect(job).toHaveCount(1);
  await expect(job).toContainText("sleep 600");
  await expect(job).toContainText("PGID");
  expect(page.url()).toBe(url);

  await job.getByTestId("background-job-kill").click();
  await expect(badge).toHaveCount(0, { timeout: 15000 });
  await expect(job).toHaveCount(0);
  await page.locator('button[aria-label="Close conversations"]').click();
  await expect(page.getByTestId("message-author-background-job")).toBeVisible({ timeout: 15000 });
  await expect(page.getByText(/finished: exit 143/)).toBeVisible();
});
