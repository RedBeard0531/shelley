import { expect, type APIRequestContext, type Page } from "@playwright/test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

export interface CreatedConversation {
  conversationId: string;
  slug: string;
}

interface CreateConversationOptions {
  agentTimeout?: number;
  cwd?: string;
  model?: string;
}

function sanitizeSlug(input: string): string {
  return input
    .toLowerCase()
    .replace(/[\s_]+/g, "-")
    .replace(/[^a-z0-9-]+/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 60)
    .replace(/-$/g, "");
}

function buildStableTestSlug(currentSlug: string, conversationId: string): string {
  const uniqueSuffix = conversationId
    .replace(/[^a-z0-9]/gi, "")
    .toLowerCase()
    .slice(0, 8);
  const slugBase = sanitizeSlug(currentSlug) || "conversation";
  const maxBaseLength = Math.max(1, 60 - uniqueSuffix.length - 1);
  const truncatedBase = slugBase.slice(0, maxBaseLength).replace(/-$/g, "");
  return `${truncatedBase || "conversation"}-${uniqueSuffix}`;
}

async function renameConversationForTest(
  request: APIRequestContext,
  conversationId: string,
  currentSlug: string,
): Promise<string> {
  const desiredSlug = buildStableTestSlug(currentSlug, conversationId);
  const renameResp = await request.post(`/api/conversation/${conversationId}/rename`, {
    data: { slug: desiredSlug },
  });
  expect(renameResp.ok()).toBeTruthy();
  const renamedConversation = await renameResp.json();
  return renamedConversation.slug || desiredSlug;
}

/**
 * Poll a conversation until it has a slug. This is used for distillation flows
 * where there is no end_of_turn marker to wait on.
 */
export async function waitForConversationSlug(
  request: APIRequestContext,
  conversationId: string,
  timeout = 30000,
): Promise<string> {
  let slug = "";
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversationId}`);
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    slug = body.conversation?.slug || "";
    expect(slug).toBeTruthy();
  }).toPass({ timeout });
  return slug;
}

/**
 * Rename a conversation to a stable unique test slug after background slug
 * generation has completed. This avoids collisions when many tests create
 * predictable-model conversations with the same prompts.
 */
export async function stabilizeConversationSlug(
  request: APIRequestContext,
  conversationId: string,
  currentSlug: string,
): Promise<string> {
  return renameConversationForTest(request, conversationId, currentSlug);
}

/**
 * Create a conversation via the API, wait for the agent to finish, then rename
 * it to a stable unique slug for deterministic direct navigation.
 *
 * This avoids two sources of flake:
 * 1. The SSE subscribe-vs-publish race when the browser opens a brand new
 *    conversation while the first turn is still being recorded.
 * 2. Slug collisions when many predictable-model tests create similar prompts.
 */
export async function createConversationViaAPIWithDetails(
  request: APIRequestContext,
  message: string,
  opts: CreateConversationOptions = {},
): Promise<CreatedConversation> {
  const { agentTimeout = 30000, cwd = "/tmp", model = "predictable" } = opts;
  const newResp = await request.post("/api/conversations/new", {
    data: { message, model, cwd },
  });
  expect(newResp.ok()).toBeTruthy();
  const { conversation_id: conversationId } = await newResp.json();

  let currentSlug = "";
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversationId}`);
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    const done = body.messages?.some(
      (m: { type: string; end_of_turn?: boolean }) => m.type === "agent" && m.end_of_turn === true,
    );
    expect(done).toBeTruthy();
    currentSlug = body.conversation?.slug || "";
    expect(currentSlug).toBeTruthy();
  }).toPass({ timeout: agentTimeout });

  const slug = await stabilizeConversationSlug(request, conversationId, currentSlug);
  return { conversationId, slug };
}

export async function createConversationViaAPI(
  request: APIRequestContext,
  message: string,
  opts: CreateConversationOptions = {},
): Promise<string> {
  const { slug } = await createConversationViaAPIWithDetails(request, message, opts);
  return slug;
}

/** Completed tool cards far from the viewport render as cheap geometry
 *  placeholders until they scroll near it (see composables/nearViewport.ts).
 *  Printing reveals them all at once, which is how a spec that needs every
 *  card in a long conversation mounted gets there without scrolling (and
 *  without a sleep): the reveal is synchronous in the page. */
export async function mountAllToolCards(page: Page): Promise<void> {
  await page.evaluate(() => window.dispatchEvent(new Event("beforeprint")));
  await expect(page.locator(".tool-card-mount-placeholder")).toHaveCount(0, { timeout: 15000 });
}

/** Override a boolean feature flag for THIS page only (via localStorage).
 *  Call before the first `page.goto(...)` so the override is in place when
 *  the React app first reads the flag. Per-page scope means parallel
 *  workers can disagree on the same flag without racing on the global DB. */
export async function setPageFeatureFlag(page: Page, name: string, value: boolean): Promise<void> {
  await page.addInitScript(
    ([n, v]) => {
      try {
        window.localStorage.setItem(`ff:${n}`, String(v));
      } catch {
        /* localStorage unavailable; flag will fall back to server default */
      }
    },
    [name, value] as const,
  );
}

/** Run `fn` with a fresh temp directory, removing it afterwards. Specs that
 *  need a real cwd with real files on disk (file finder, patch cards) use this
 *  so a full suite run doesn't litter /tmp. */
export async function withTempDir(
  prefix: string,
  fn: (dir: string) => Promise<void>,
): Promise<void> {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  try {
    await fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
