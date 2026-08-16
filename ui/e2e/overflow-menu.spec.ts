import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// The top-right overflow ("kebab") menu is built from PrimeVue Popover/Select
// plus compact native icon buttons. See components/ChatOverflowMenu.vue. The
// DOM contract (.chat-overflow-menu-wrapper / .btn-icon / .overflow-menu-item)
// is covered by other specs (agents-md-vim, diff-viewer-find); here we
// exercise the PrimeVue-specific controls.
test.describe("Overflow menu (PrimeVue)", () => {
  test("popover opens, compact controls and language Select work", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.addInitScript(() => {
      Object.defineProperty(window, "Notification", {
        configurable: true,
        value: class FakeNotification {
          static permission = "default";
          static requestPermission = async () => {
            FakeNotification.permission = "denied";
            return "denied";
          };
          close() {}
        },
      });
    });

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    // Reset persisted prefs so assertions are deterministic regardless of
    // what an earlier test in the same worker stored.
    await page.evaluate(() => {
      localStorage.setItem("shelley-theme", "system");
      localStorage.setItem(
        "shelley-notification-prefs",
        JSON.stringify({ channels: { favicon: { enabled: true }, browser: { enabled: true } } }),
      );
    });
    await page.reload();
    await page.waitForLoadState("domcontentloaded");

    // Open the PrimeVue Popover.
    const trigger = page.locator(".chat-overflow-menu-wrapper .btn-icon");
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();

    const popover = page.locator(".chat-overflow-popover");
    await expect(popover).toBeVisible();

    // --- Compact controls: view, theme cycle, notifications ---
    await expect(popover.locator(".overflow-quick-control")).toHaveCount(3);
    await expect(popover.getByText("Brevity", { exact: true })).toBeVisible();
    await expect(popover.getByText("Look", { exact: true })).toBeVisible();
    await expect(popover.getByText("Notifications", { exact: true })).toBeVisible();
    await expect(popover.locator(".overflow-choice-current")).toHaveCount(3);
    await expect(popover.locator(".overflow-choice-alternatives")).toHaveCount(3);
    const themeCycle = popover.getByTestId("theme-cycle");
    await expect(themeCycle).toHaveAttribute("aria-label", "System → Light");

    const notificationToggle = popover.getByTestId("notification-toggle");
    await expect(notificationToggle).toHaveAttribute("aria-label", "Disable Notifications");
    await notificationToggle.click();
    await expect(notificationToggle).toHaveAttribute("aria-label", "Enable Notifications");
    await expect(notificationToggle).toBeEnabled();
    await notificationToggle.click();
    await expect(notificationToggle).toHaveAttribute("aria-label", "Blocked by browser");
    await expect(notificationToggle).toBeDisabled();
    expect(
      await page.evaluate(
        () =>
          JSON.parse(localStorage.getItem("shelley-notification-prefs") || "{}").channels?.browser
            ?.enabled,
      ),
    ).toBe(false);

    // System → Light.
    await themeCycle.click();
    await expect(page.locator("html")).not.toHaveClass(/dark/);
    expect(await page.evaluate(() => localStorage.getItem("shelley-theme"))).toBe("light");

    // Light → Dark.
    await themeCycle.click();
    await expect(page.locator("html")).toHaveClass(/dark/);
    expect(await page.evaluate(() => localStorage.getItem("shelley-theme"))).toBe("dark");

    // --- Language Select: open and pick Japanese ---
    const select = popover.locator(".overflow-language-select");
    await select.click();
    // The overlay renders inside the popover (appendTo="self"), so the popover
    // must stay open while we pick.
    const jpOption = page.locator(".p-select-option").filter({ hasText: /日本語/ });
    await expect(jpOption).toBeVisible();
    await jpOption.click();
    expect(await page.evaluate(() => localStorage.getItem("shelley-locale"))).toBe("ja");
    await expect(popover).toBeVisible();

    // The compact control labels re-translate live while the menu stays open.
    await expect(themeCycle).toHaveAttribute("aria-label", "ダーク → システム");

    // Reset locale so we don't leak Japanese UI into sibling tests' assertions.
    await page.evaluate(() => localStorage.setItem("shelley-locale", "en"));
  });

  test("can show only user, end-of-turn, and notification messages", async ({ page, request }) => {
    test.setTimeout(60000);

    const response = await request.post("/debug/loremipsum?json=1", {
      form: { size: "18", model: "predictable" },
    });
    expect(response.ok()).toBeTruthy();
    const { conversation_id: conversationId } = await response.json();

    await page.goto(`/c/${conversationId}`);
    await expect(page.getByText("Turn 1:", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("I'll work on turn 1.", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("Done with turn 1.", { exact: false }).first()).toBeVisible();
    // The conversation pins to the newest messages on load, so the first
    // completed tool card (turn 1) starts far above the viewport. Bring it
    // into view before asserting it rendered, instead of racing the auto-
    // scroll that moves it out of the way.
    const firstCompleted = page.locator('[data-testid="tool-call-completed"]').first();
    await firstCompleted.scrollIntoViewIfNeeded();
    await expect(firstCompleted).toBeVisible();

    await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
    const viewToggle = page.getByTestId("conversation-view-toggle");
    await expect(viewToggle).toHaveAttribute(
      "aria-label",
      "See All → See End of Turn Messages Only",
    );
    await viewToggle.click();
    await expect(viewToggle).toHaveAttribute(
      "aria-label",
      "See End of Turn Messages Only → See All",
    );

    await expect(page.getByText("Turn 1:", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("Done with turn 1.", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("Warning on turn 18:", { exact: false })).toBeVisible();
    await expect(page.getByText("I'll work on turn 1.", { exact: false })).toHaveCount(0);
    await expect(page.locator('[data-testid="tool-call-completed"]')).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem("shelley-conversation-view"))).toBe(
      "end-of-turn",
    );

    await page.reload();
    await expect(page.getByText("I'll work on turn 1.", { exact: false })).toHaveCount(0);
    await expect(page.getByText("Done with turn 1.", { exact: false }).first()).toBeVisible();
  });
});
