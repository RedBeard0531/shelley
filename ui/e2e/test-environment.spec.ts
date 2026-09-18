import { expect, test } from "@playwright/test";
import { basename, dirname, join } from "node:path";
import { createConversationViaAPIWithDetails } from "./helpers";

// Editor tests write AGENTS.md in HOME, and every new conversation scans cwd
// for guidance and skills. Neither may use the runner's shared HOME or /tmp.
test("the managed server isolates its home and conversation working directory", async ({
  page,
  request,
}) => {
  test.skip(!!process.env.TEST_SERVER_URL, "External servers own their environment");
  await page.goto("/new");
  const init = await page.evaluate(() => window.__SHELLEY_INIT__!);
  expect(basename(init.default_cwd!)).toBe("cwd");
  expect(basename(init.home_dir!)).toBe("home");
  expect(dirname(init.default_cwd!)).toBe(dirname(init.home_dir!));
  expect(basename(dirname(init.default_cwd!))).toMatch(/^shelley-e2e-/);
  expect(init.user_agents_md_path).toBe(join(init.home_dir!, ".config/shelley/AGENTS.md"));

  const { conversationId } = await createConversationViaAPIWithDetails(request, "Hello");
  const response = await request.get(`/api/conversation/${conversationId}`);
  expect(response.ok()).toBeTruthy();
  const { conversation } = await response.json();
  expect(conversation.cwd).toBe(init.default_cwd);
});
