import { test, expect, type Locator } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { createConversationViaAPIWithDetails } from "./helpers";

const run = promisify(execFile);
const binary = fileURLToPath(new URL("../../bin/shelley", import.meta.url));

async function sendFrom(sender: string, target: string, text: string) {
  await run(
    binary,
    [
      "client",
      "--url",
      `unix://${process.env.TEST_SERVER_SOCKET}`,
      "chat",
      "-c",
      target,
      "-p",
      text,
    ],
    {
      env: { ...process.env, SHELLEY_CONVERSATION_ID: sender },
    },
  );
}

async function expectToolMessage(message: Locator, text: string, background: string) {
  await expect(message).toHaveClass(/message-tool/);
  await expect(message).not.toHaveClass(/message-user|message-agent/);
  await expect(message).toContainText(text);
  await expect(message).not.toContainText(/<(?:parent|subagent)_message/);
  await expect(message).toHaveCSS("background-color", background);
  await expect(message).toHaveCSS("border-radius", "8px");
  await expect(message.getByTestId("message-author-conversation")).toHaveCSS("font-weight", "400");
}

for (const viewport of [
  { width: 390, height: 844 },
  { width: 1280, height: 720 },
]) {
  for (const colorScheme of ["light", "dark"] as const) {
    test(`CLI conversation provenance uses tool styling at ${viewport.width}px (${colorScheme})`, async ({
      page,
      request,
    }) => {
      test.skip(
        !process.env.TEST_SERVER_SOCKET,
        "External test servers need TEST_SERVER_SOCKET for trusted CLI requests",
      );
      await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme });
      const parent = await createConversationViaAPIWithDetails(
        request,
        "subagent: backend echo: Ready.",
      );
      const response = await request.get(`/api/conversation/${parent.conversationId}/subagents`);
      expect(response.ok()).toBeTruthy();
      const [child] = await response.json();
      expect(child.conversation_id).toBeTruthy();

      await page.goto(`/c/${parent.slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible();
      const background = colorScheme === "dark" ? "rgb(31, 41, 55)" : "rgb(243, 244, 246)";
      const progress = "Backend underway: API and database implementation are progressing.";
      await sendFrom(
        child.conversation_id,
        parent.conversationId,
        progress.replace("API and database", "**API and database**"),
      );
      const source = page.getByTestId("message-author-conversation");
      await expect(source).toHaveText(`Message from ${child.slug}`);
      const message = page.getByTestId("message").filter({ has: source });
      await expectToolMessage(message, progress, background);
      await expect(message.locator("strong")).toHaveText("API and database");

      // Reload exercises persisted metadata rather than only stream updates.
      await page.reload();
      await expectToolMessage(message, progress, background);
      const childLink = source.getByRole("link", { name: child.slug, exact: true });
      await expect(childLink).toHaveAttribute("href", `/c/${child.conversation_id}`);
      await expect(childLink).toHaveAttribute("title", "Open subagent conversation");
      await childLink.click();
      await expect(page.locator(".header-title")).toHaveText(child.slug);

      const instruction = "Please finish the API tests before the database changes.";
      await sendFrom(parent.conversationId, child.conversation_id, instruction);
      await expect(source).toHaveText(`Message from ${parent.slug}`);
      await expectToolMessage(message, instruction, background);
      // The label follows renames; the stable ID keeps the link unambiguous.
      const renamedSlug = `parent-renamed-${parent.conversationId.toLowerCase()}`;
      const renamed = await request.post(`/api/conversation/${parent.conversationId}/rename`, {
        data: { slug: renamedSlug },
      });
      expect(renamed.ok()).toBeTruthy();
      parent.slug = renamedSlug;
      await expect(source).toHaveText(`Message from ${parent.slug}`);
      const parentLink = source.getByRole("link", { name: parent.slug, exact: true });
      await expect(parentLink).toHaveAttribute("href", `/c/${parent.conversationId}`);
      await expect(parentLink).toHaveAttribute("title", "Open parent conversation");
      await parentLink.click();
      await expect(page.locator(".header-title")).toHaveText(parent.slug);
      await expect(source).toHaveText(`Message from ${child.slug}`);

      const human = page
        .getByTestId("message")
        .filter({ hasText: "subagent: backend echo: Ready." })
        .first();
      await expect(human).toHaveClass(/message-user/);
      await expect(human.getByTestId("message-author-conversation")).toHaveCount(0);
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      ).toBe(true);
    });
  }
}

test("queued and transcribing conversation messages share the source header", async ({
  page,
  request,
}) => {
  const parent = await createConversationViaAPIWithDetails(request, "echo: queue provenance");
  const source = {
    sender_conversation_id: "child",
    sender_slug: "backend",
    sender_relationship: "subagent",
    Text: "Queued progress",
  };
  const queued = JSON.stringify([
    {
      id: "queued-source",
      created_at: new Date().toISOString(),
      model: "predictable",
      llm: { Role: 0, Content: [{ Type: 2, Text: source.Text }] },
      user_data: source,
    },
    ...["working", "failed"].map((state) => ({
      id: `transcription-${state}`,
      created_at: new Date().toISOString(),
      model: "predictable",
      kind: "transcription",
      state,
      transcription: {
        media_path: "/tmp/progress.webm",
        context: "Progress report",
      },
      user_data: {
        ...source,
        sender_relationship: "parent",
        sender_slug: parent.slug,
        sender_conversation_id: parent.conversationId,
      },
    })),
  ]);
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.route("**/api/conversations/snapshot", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    const conversation = body.conversations.find(
      (c: { conversation_id: string }) => c.conversation_id === parent.conversationId,
    );
    conversation.queued_messages = queued;
    await route.fulfill({ response, json: body });
  });
  await page.route(`**/api/conversation/${parent.conversationId}*`, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.conversation.queued_messages = queued;
    await route.fulfill({ response, json: body });
  });
  await page.goto(`/c/${parent.slug}`);
  const ghost = page.getByTestId("queued-ghost");
  await expect(ghost.getByTestId("message-author-conversation")).toHaveText("Message from backend");
  await expect(ghost).toHaveClass(/message-tool/);
  await expect(ghost).toHaveCSS("background-color", "rgb(243, 244, 246)");
  await expect(ghost.getByTestId("message-content")).toHaveCSS("border-top-style", "dashed");
  await expect(ghost).toContainText("Queued progress");
  const tasks = page.getByTestId("transcription-task");
  await expect(tasks).toHaveCount(2);
  for (const task of await tasks.all()) {
    await expect(task).toHaveCSS("background-color", "rgb(243, 244, 246)");
    await expect(task.getByTestId("message-author-conversation")).toHaveText(
      `Message from ${parent.slug}`,
    );
  }
});
