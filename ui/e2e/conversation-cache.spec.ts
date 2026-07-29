import { test, expect } from '@playwright/test';
import { createConversationViaAPIWithDetails } from './helpers';

/**
 * Helper: wait for text to appear on the page.
 */
async function waitForText(page: import('@playwright/test').Page, text: string, timeout = 15000) {
  await page.waitForFunction((t) => document.body.textContent?.includes(t) ?? false, text, {
    timeout
  });
}

/**
 * Helper: select a conversation by clicking its item in the drawer.
 * Uses exact slug text matching to find the right item.
 */
async function selectConversation(page: import('@playwright/test').Page, slug: string) {
  // Open drawer (mobile: hamburger button)
  const drawerButton = page.locator('button[aria-label="Open conversations"]');
  await drawerButton.click();
  const drawer = page.locator('.drawer.open');
  await expect(drawer).toBeVisible({ timeout: 5000 });
  // Click the conversation title with exact slug text
  const titleEl = drawer.locator('.conversation-title').getByText(slug, { exact: true });
  await expect(titleEl).toBeVisible({ timeout: 15000 });
  await titleEl.click();
  await expect(drawer).toBeHidden({ timeout: 10000 });
}

/**
 * Wait until the conversation's encrypted IndexedDB row reports a complete
 * cached history.
 *
 * Persistence is write-behind (see messageStore's `inflight` set), so "the
 * messages are on screen" does NOT imply "the cache is on disk". Any test that
 * reloads or navigates away in order to exercise the cache has to wait for
 * this, or under parallel load it races the write and gets a cold cache.
 */
async function waitForCachedHistory(
  page: import('@playwright/test').Page,
  conversationId: string,
  timeout = 15000
) {
  await page.waitForFunction(
    async (id) => {
      const req = indexedDB.open('shelley-messages');
      const db: IDBDatabase = await new Promise((res, rej) => {
        req.onsuccess = () => res(req.result);
        req.onerror = () => rej(req.error);
      });
      try {
        if (!db.objectStoreNames.contains('conversation_meta')) return false;
        const row = await new Promise<{ has_full_history?: boolean } | undefined>((res) => {
          const r = db.transaction('conversation_meta', 'readonly')
            .objectStore('conversation_meta').get(id);
          r.onsuccess = () => res(r.result);
          r.onerror = () => res(undefined);
        });
        return !!row?.has_full_history;
      } finally {
        db.close();
      }
    },
    conversationId,
    { timeout }
  );
}

test.describe('Conversation cache', () => {
  test('switching conversations uses cache (no extra fetch on second visit)', async ({ page, request }) => {
    // Create two conversations with distinct messages
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

    // Navigate directly to conv1 by slug
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Wait for conversation 1's response
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Switch to conversation 2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Now intercept network requests to verify cache hit.
    // We specifically watch for the full conversation load endpoint
    // (GET /api/conversation/<id> without any further path segments).
    const conversationLoadFetches: string[] = [];
    // Match exactly the full-load endpoint: /api/conversation/<id> with no sub-path
    const loadPattern = new RegExp(`/api/conversation/${conv1.conversationId}$`);
    page.on('request', (req) => {
      if (loadPattern.test(new URL(req.url()).pathname)) {
        conversationLoadFetches.push(req.url());
      }
    });

    // Switch back to conversation 1
    await selectConversation(page, conv1.slug);

    // Conversation 1 messages should be visible from cache
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Verify no new fetch was made for the full conversation load
    expect(conversationLoadFetches).toHaveLength(0);
  });

  test('cached conversation shows correct messages after streaming updates', async ({ page, request }) => {
    // Create a conversation
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');

    // Navigate to it
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Send a follow-up message
    await messageInput.fill('echo: follow up message');
    const sendButton = page.getByTestId('send-button');
    await sendButton.click();
    await waitForText(page, 'follow up message');

    // Create a second conversation and switch to it
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

    // Reload to pick up the new conversation in the list
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Navigate to conv2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Switch back to conv1 — cache should have both original + follow-up
    await selectConversation(page, conv1.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await waitForText(page, 'follow up message');
  });

  test('cache serves messages instantly without loading spinner', async ({ page, request }) => {
    // Create two conversations
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

    // Navigate to conv1
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Switch to conv2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Switch back to conv1 — should be instant (cache hit)
    await selectConversation(page, conv1.slug);

    // Verify no loading spinner is shown
    await expect(page.locator('.spinner')).toHaveCount(0);
    await expect(page.locator("text=Hello! I'm Shelley, your AI assistant.").first()).toBeVisible();
  });

  test('page reload serves history from the IndexedDB cache', async ({ page, request }) => {
    // The regression this guards: metadata mutators (setMaxSequenceIdKnown /
    // setConversation, which App pumps for EVERY conversation in the list on
    // startup) used to mark conversations "hydrated" without reading IndexedDB.
    // The disk cache was therefore never consulted on a fresh page load, so
    // every reload re-downloaded the whole conversation over REST and the cache
    // only ever helped for in-session conversation switches.
    const conv = await createConversationViaAPIWithDetails(request, 'Hello');

    await page.goto(`/c/${conv.slug}`);
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Let the write-behind IndexedDB persistence land before reloading.
    await waitForCachedHistory(page, conv.conversationId);

    // Count full-conversation loads across the reload. The cache should make
    // this zero: a complete cached history that the conversation list agrees
    // is up to date needs no server round-trip at all.
    const fullLoads: string[] = [];
    const fullLoadPattern = new RegExp(`/api/conversation/${conv.conversationId}$`);
    page.on('request', (req) => {
      const url = new URL(req.url());
      if (fullLoadPattern.test(url.pathname) && !url.searchParams.has('last_sequence_id')) {
        fullLoads.push(req.url());
      }
    });

    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    expect(fullLoads).toHaveLength(0);

    // And the cache diagnostics agree it was a cache hit, not a reload.
    const stats = await page.evaluate(() => window.__shelleyCache?.stats() ?? {});
    expect(Object.keys(stats)).toContain('load.served_from_cache');
    expect(stats['load.full_rest'] ?? 0).toBe(0);
  });

  test('messages added while the tab was closed arrive via an incremental fetch', async ({
    page,
    request,
  }) => {
    // With a complete cached history we should never re-download it wholesale
    // just to discover the tail: ask for ?last_sequence_id=N instead.
    const conv = await createConversationViaAPIWithDetails(request, 'Hello');

    await page.goto(`/c/${conv.slug}`);
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    // Wait for the cache to actually be on disk. Observing the REST load is
    // not enough: persistence is write-behind, so navigating away here would
    // leave nothing cached and the return trip would do a full reload.
    await waitForCachedHistory(page, conv.conversationId);

    // Navigate away so the tab isn't streaming, then add a message server-side.
    await page.goto('about:blank');
    const chatResp = await request.post(`/api/conversation/${conv.conversationId}/chat`, {
      data: { message: 'echo: added while closed', model: 'predictable' },
    });
    expect(chatResp.ok()).toBeTruthy();
    await expect(async () => {
      const resp = await request.get(`/api/conversation/${conv.conversationId}`);
      const body = await resp.json();
      // The message text lives inside llm_data (JSON), so match on the
      // serialized payload rather than a specific field.
      expect(JSON.stringify(body.messages ?? [])).toContain('added while closed');
    }).toPass({ timeout: 30000 });

    const incremental: string[] = [];
    const fullLoads: string[] = [];
    const pattern = new RegExp(`/api/conversation/${conv.conversationId}$`);
    page.on('request', (req) => {
      const url = new URL(req.url());
      if (!pattern.test(url.pathname)) return;
      if (url.searchParams.has('last_sequence_id')) incremental.push(req.url());
      else fullLoads.push(req.url());
    });

    await page.goto(`/c/${conv.slug}`);
    await page.waitForLoadState('domcontentloaded');
    // Both the cached history and the newly-added tail must be present.
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await waitForText(page, 'added while closed');

    expect(incremental.length).toBeGreaterThan(0);
    expect(fullLoads).toHaveLength(0);
  });
});
