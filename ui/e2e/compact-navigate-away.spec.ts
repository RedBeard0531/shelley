import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

// Compaction runs server-side; the client only kicks it off. If the user moves
// to another conversation while the request is in flight, the response landing
// must not yank them back to the conversation they compacted.
test.describe("compact while navigating away", () => {
  test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });

  test("stays on the newly selected conversation", async ({ page, request }) => {
    const source = await createConversationViaAPIWithDetails(request, "echo: compact source");
    const other = await createConversationViaAPIWithDetails(request, "echo: compact other");

    // Hold the compact response until the test releases it.
    let release!: () => void;
    const held = new Promise<void>((resolve) => (release = resolve));
    let compactRequests = 0;
    await page.route("**/api/conversations/distill-new-generation", async (route) => {
      compactRequests++;
      await held;
      await route.fulfill({
        json: { conversation_id: source.conversationId, current_generation: 2 },
      });
    });

    await page.goto(`/c/${source.slug}`);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30_000 });
    await input.fill("/compact");
    await input.press("Enter");
    await expect.poll(() => compactRequests).toBe(1);

    // SPA-navigate to the other conversation while compaction is in flight.
    const otherRow = page.locator(
      `.conversation-item[data-conversation-id="${other.conversationId}"]`,
    );
    await otherRow.click();
    await expect(otherRow).toHaveClass(/active/);
    await expect(page).toHaveURL(new RegExp(`/c/${other.slug}$`));

    // Once the body is delivered, everything downstream (api promise, handler,
    // Vue's pre-flush URL-sync watcher, replaceState) runs in microtasks, so
    // the next round-trip to the page sees the final URL.
    const response = page.waitForResponse("**/api/conversations/distill-new-generation");
    release();
    await (await response).finished();

    await expect(page).toHaveURL(new RegExp(`/c/${other.slug}$`));
    await expect(otherRow).toHaveClass(/active/);
  });
});
