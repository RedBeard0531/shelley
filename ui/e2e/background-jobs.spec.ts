import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test("drawer expands running jobs with live output and stops one", async ({ page, request }) => {
  const slug = await createConversationViaAPI(request, "bash-bg: echo ready; sleep 600");
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

  await page.locator('button[aria-label="Open conversations"]').click();
  await expect(page.locator(".drawer.open")).toBeVisible();
  const badge = page.locator(".conversation-item.active").getByTestId("background-jobs-badge");
  await expect(badge).toHaveText("1", { timeout: 15000 });

  // Expanding the jobs must not navigate away or open a popover.
  const url = page.url();
  await badge.click();
  const job = page.getByTestId("background-job");
  await expect(job).toHaveCount(1);
  await expect(job).toContainText("echo ready; sleep 600");
  await expect(job.getByTestId("background-job-tail")).toContainText("ready");
  await expect(job).not.toContainText("PGID");
  await expect(job).not.toContainText(".log");
  await expect(job.locator("xpath=..")).toHaveClass(/drawer-background-jobs-list/);
  await expect(page.locator(".background-jobs-popover")).toHaveCount(0);
  expect(page.url()).toBe(url);

  await badge.click();
  await expect(job).toHaveCount(0);
  await badge.click();
  await expect(job).toHaveCount(1);
  await job.getByTestId("background-job-kill").click();
  await expect(badge).toHaveCount(0, { timeout: 15000 });
  await expect(job).toHaveCount(0);
  await page.locator('button[aria-label="Close conversations"]').click();
  const notice = page.getByTestId("bash-tool-finished-job");
  await expect(notice).toBeVisible({ timeout: 15000 });
  await expect(notice).toContainText("exit 143");
  await expect(page.locator(".bash-tool", { has: notice })).toContainText("sleep 600");
});
