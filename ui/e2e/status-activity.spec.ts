import { test, expect, type APIRequestContext } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

// The status bar says what is still running for the open conversation once the
// agent's own turn is over: backgrounded bash jobs and working subagents. Each
// count opens a popover listing them, where a job can be stopped and a
// subagent opened.

// These jobs and subagents sleep for minutes; stop them so a failed run does
// not leave them on the shared server.
async function stopWork(request: APIRequestContext, conversationIds: string[]) {
  for (const id of conversationIds) {
    const subagents = await (await request.get(`/api/conversation/${id}/subagents`)).json();
    for (const { conversation_id } of subagents ?? []) {
      await request.post(`/api/conversation/${conversation_id}/cancel`);
    }
    const jobs = await (await request.get(`/api/conversation/${id}/background-jobs`)).json();
    for (const { job_id } of jobs ?? []) {
      await request.post(`/api/conversation/${id}/background-jobs/${job_id}/kill`);
    }
  }
}

test.describe("status bar activity", () => {
  test("lists running background jobs; stopping one keeps the popover open", async ({
    page,
    request,
  }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "bash-bg: echo ready; sleep 600",
    );
    try {
      expect(
        (
          await request.post(`/api/conversation/${conversationId}/chat`, {
            data: { message: "bash-bg: sleep 601", model: "predictable" },
          })
        ).ok(),
      ).toBe(true);
      await page.goto(`/c/${slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

      const activity = page.getByTestId("status-activity");
      await expect(activity.getByTestId("status-activity-jobs")).toHaveText("2", {
        timeout: 15000,
      });
      // Icon + number on the bar; the words are its tooltip and accessible name.
      await expect(activity).toHaveAccessibleName("2 background jobs running");
      await expect(activity.getByTestId("status-activity-subagents")).toHaveCount(0);
      // Sits beside "Ready on <host>": the agent itself is idle.
      await expect(page.locator(".status-ready")).toBeVisible({ timeout: 15000 });

      await activity.click();
      const popup = page.locator(".status-activity-popup");
      const jobs = popup.getByTestId("background-job");
      await expect(jobs).toHaveCount(2);
      // A job exiting starts an agent turn ("Background job … finished"), so
      // the bar goes Ready -> working -> Ready under the open popover.
      await jobs.filter({ hasText: "echo ready" }).getByTestId("background-job-kill").click();
      await expect(activity.getByTestId("status-activity-jobs")).toHaveText("1", {
        timeout: 15000,
      });
      await expect(page.getByTestId("agent-thinking")).toBeHidden({ timeout: 30000 });
      await expect(jobs).toHaveCount(1);
      await expect(jobs).toContainText("sleep 601");

      // A click inside the list must not swallow the next outside click.
      await jobs.locator(".background-jobs-command").click();
      await page.locator(".messages-container").click({ position: { x: 5, y: 5 } });
      await expect(popup).toHaveCount(0);

      await activity.click();
      await jobs.getByTestId("background-job-kill").click();
      await expect(activity).toHaveCount(0, { timeout: 15000 });
      await expect(popup).toHaveCount(0);
    } finally {
      await stopWork(request, [conversationId]);
    }
  });

  test("keyboard opens the popover into its list; switching conversations closes it", async ({
    page,
    request,
  }) => {
    const first = await createConversationViaAPIWithDetails(request, "bash-bg: sleep 600");
    const second = await createConversationViaAPIWithDetails(request, "bash-bg: sleep 601");
    try {
      await page.goto(`/c/${first.slug}`);
      const activity = page.getByTestId("status-activity");
      await expect(activity.getByTestId("status-activity-jobs")).toHaveText("1", {
        timeout: 15000,
      });

      await activity.focus();
      await page.keyboard.press("Enter");
      const popup = page.locator(".status-activity-popup");
      await expect(popup.getByTestId("background-job")).toContainText("sleep 600");
      await page.keyboard.press("Tab");
      await expect(popup.getByTestId("background-job-kill")).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(popup).toHaveCount(0);
      await expect(activity).toBeFocused();

      // Same job count on both sides, so nothing but the conversation changes.
      await activity.click();
      await expect(popup.getByTestId("background-job")).toContainText("sleep 600");
      await page.evaluate((slug) => {
        window.history.pushState({}, "", `/c/${slug}`);
        window.dispatchEvent(new PopStateEvent("popstate"));
      }, second.slug);
      await expect(
        page.getByTestId("message").filter({ hasText: "sleep 601" }).first(),
      ).toBeVisible();
      await expect(popup).toHaveCount(0);
      await activity.click();
      await expect(popup.getByTestId("background-job")).toContainText("sleep 601");
      await expect(popup.getByTestId("background-job")).toHaveCount(1);
    } finally {
      await stopWork(request, [first.conversationId, second.conversationId]);
    }
  });

  test("shows running subagents and opens one from the popover", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "hello there",
    );
    try {
      await page.goto(`/c/${slug}`);
      const input = page.getByTestId("message-input");
      await expect(input).toBeVisible({ timeout: 30000 });

      await input.fill("subagent: statushelper bash: sleep 120");
      await page.getByTestId("send-button").click();

      const activity = page.getByTestId("status-activity");
      await expect(activity.getByTestId("status-activity-subagents")).toHaveText("1", {
        timeout: 30000,
      });
      await expect(activity).toHaveAccessibleName("1 subagent running");
      await expect(page.locator(".status-ready")).toBeVisible({ timeout: 30000 });

      await activity.click();
      const row = page.locator(".status-activity-popup").getByTestId("subagent-live");
      await expect(row).toHaveCount(1);
      await expect(row).toContainText("statushelper");
      await expect(row).toContainText("sleep", { timeout: 30000 });
      await row.click();
      await expect(page).toHaveURL(/\/c\/statushelper/, { timeout: 10000 });
      await expect(page.locator(".status-activity-popup")).toHaveCount(0);
    } finally {
      await stopWork(request, [conversationId]);
    }
  });
});
