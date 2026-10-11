import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

async function mockVoiceBrowser(page: Page, denyMicrophone = false, holdMicrophone = false) {
  await page.clock.install();
  await page.addInitScript(({ denied, held }) => {
    (window as any).__voiceTracksStopped = 0;
    const track = {
      kind: "audio", enabled: true,
      stop() { (window as any).__voiceTracksStopped++; },
    };
    Object.defineProperty(navigator.mediaDevices, "getUserMedia", {
      value: async () => {
        if (denied) throw new DOMException("Microphone denied", "NotAllowedError");
        if (held) await new Promise<void>((resolve) => {
          (window as any).__releaseVoiceMicrophone = resolve;
        });
        return { getAudioTracks: () => [track], getTracks: () => [track] };
      },
    });
    class Channel {
      readyState = "open";
      onmessage: ((event: { data: string }) => void) | null = null;
      onclose: (() => void) | null = null;
      sent: Array<Record<string, unknown>> = [];
      send(raw: string) {
        this.sent.push(JSON.parse(raw));
      }
      emit(event: Record<string, unknown>) {
        this.onmessage?.({ data: JSON.stringify(event) });
      }
      close() {
        this.readyState = "closed";
        this.onclose?.();
      }
    }
    class Peer {
      iceGatheringState = "complete";
      connectionState = "connected";
      localDescription: { type: string; sdp: string } | null = null;
      channel = new Channel();
      addTrack() {}
      createDataChannel() {
        (window as any).__mockLive = this.channel;
        return this.channel;
      }
      createOffer() {
        return Promise.resolve({ type: "offer", sdp: "v=0\r\nm=audio test" });
      }
      setLocalDescription(description: { type: string; sdp: string }) {
        this.localDescription = description;
        return Promise.resolve();
      }
      setRemoteDescription() {
        queueMicrotask(() =>
          this.channel.emit({ type: "session.started", session: { id: "live_test" } }),
        );
        return Promise.resolve();
      }
      addEventListener() {}
      close() {
        this.channel.close();
      }
    }
    Object.defineProperty(window, "RTCPeerConnection", { value: Peer });
  }, { denied: denyMicrophone, held: holdMicrophone });
}

async function navigateWithinApp(page: Page, path: string) {
  await page.evaluate((url) => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, path);
  await expect(page).toHaveURL(new RegExp(`${path}$`));
}

type Live = { emit(event: Record<string, unknown>): void; sent: Array<Record<string, unknown>> };

const emit = (page: Page, event: Record<string, unknown>) =>
  page.evaluate((value) => ((window as any).__mockLive as Live).emit(value), event);
const notes = (page: Page) =>
  page.evaluate(() => JSON.stringify(((window as any).__mockLive as Live).sent));

// Live delegates a client task after this speech; the call settles the
// transcript and submits it.
async function speakAndDelegate(page: Page, id: string, speech: string) {
  await emit(page, { type: "session.input_transcript.delta", delta: speech, start_ms: 0, end_ms: 900 });
  await emit(page, { type: "session.delegation.created", offset_ms: 900, delegation: { id, target: "client" } });
  await page.clock.fastForward(1_300);
}

test("a delegated question reaches the chat, and its reply reaches Live", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: ready");
  await mockVoiceBrowser(page);
  await page.route(`**/api/conversation/${conversationId}/live-session`, (route) => {
    expect(route.request().postDataJSON()).not.toHaveProperty("conversation_options");
    return route.fulfill({
      status: 201, contentType: "application/json",
      body: JSON.stringify({ session: { id: "live_direct" }, transport: { sdp: "v=0" } }),
    });
  });
  const submitted: Array<{ transcript: string; voice_context: string }> = [];
  page.on("request", (req) => {
    if (req.url().endsWith(`/api/conversation/${conversationId}/live-message`)) submitted.push(req.postDataJSON());
  });
  await page.goto(`/c/${conversationId}`);
  const call = page.getByTestId("live-call-header");
  await expect(call).toHaveAttribute("aria-label", "Start voice conversation");
  await call.click();
  const bar = page.getByTestId("live-bar");
  await expect(bar).toContainText("On call");
  await expect(call).toHaveAttribute("aria-label", "End voice call");
  await expect(call).toHaveCSS("color", await page.getByTestId("live-stop").evaluate((el) => getComputedStyle(el).backgroundColor));
  await expect(page.getByTestId("live-timer")).toBeVisible();
  // The strip sits by the composer, with a wide meter and no captions.
  const barBox = await bar.boundingBox();
  const composerBox = await page.locator(".message-input-container").boundingBox();
  expect(barBox && composerBox && barBox.y >= composerBox.y).toBe(true);
  expect((await page.getByTestId("live-meter").boundingBox())?.width).toBeGreaterThanOrEqual(110);
  await expect(page.getByTestId("live-transcript")).toHaveCount(0);

  await emit(page, { type: "session.input_transcript.delta", delta: "Hello ", start_ms: 0, end_ms: 400 });
  await emit(page, { type: "session.output_transcript.delta", delta: "Hey there!", start_ms: 500, end_ms: 900 });
  await emit(page, { type: "session.input_transcript.delta", delta: "echo: why is this so slow", start_ms: 1_200, end_ms: 1_900 });
  await page.clock.fastForward(1_300);
  expect(submitted).toHaveLength(0); // speech alone is not a task
  for (let i = 0; i < 2; i++) { // Live may repeat an event
    await emit(page, { type: "session.delegation.created", offset_ms: 1_900, delegation: { id: "captured_question", target: "client" } });
  }
  await page.clock.fastForward(1_300);
  await expect.poll(() => submitted.length).toBe(1);
  expect(submitted[0]).toMatchObject({ transcript: "Hello echo: why is this so slow", voice_context: "USER: Hello\nLIVE: Hey there!\nUSER: echo: why is this so slow" });
  const message = page.getByTestId("message").filter({ hasText: "Spoken request:\nHello echo: why is this so slow" });
  await expect(message).toHaveCount(1);
  await expect(message).toContainText("Voice exchange (for context only):");
  await expect.poll(() => notes(page)).toContain("The backend accepted this task: Hello echo: why is this so slow");
  await expect.poll(() => notes(page)).toContain('"delegation_id":"captured_question","content":"Backend reply: edit predictable.go');

  await call.click();
  await expect(bar).toBeHidden();
  await expect(call).toHaveAttribute("aria-label", "Start voice conversation");
  expect(await notes(page)).toContain("session.close");
  expect(await page.evaluate(() => (window as any).__voiceTracksStopped)).toBe(1);
});

test("a call stays pinned to its original chat while browsing other chats", async ({ page, request }) => {
  const origin = await createConversationViaAPIWithDetails(request, "echo: original voice chat");
  const other = await createConversationViaAPIWithDetails(request, "echo: other chat");
  await mockVoiceBrowser(page);
  const sessions: string[] = [];
  const tasks: string[] = [];
  await page.route("**/api/conversation/*/live-session", (route) => {
    sessions.push(new URL(route.request().url()).pathname.split("/").at(-2)!);
    return route.fulfill({
      status: 201, contentType: "application/json",
      body: JSON.stringify({ session: { id: "live_pinned" }, transport: { sdp: "v=0" } }),
    });
  });
  page.on("request", (req) => {
    if (req.url().endsWith("/live-message")) tasks.push(new URL(req.url()).pathname.split("/").at(-2)!);
  });
  await page.goto(`/c/${origin.slug}`);
  await page.getByTestId("live-call-header").click();
  const bar = page.getByTestId("live-bar");
  await expect(bar).toContainText("On call");

  await navigateWithinApp(page, `/c/${other.slug}`);
  await expect(page.locator(".header-title")).toHaveText(other.slug);
  await expect(bar).toBeVisible();
  await expect(page.getByTestId("live-call-header")).toHaveAttribute("aria-label", "End voice call");
  expect((await request.post(`/api/conversation/${other.conversationId}/chat`, {
    data: { message: "echo: unrelated private reply", model: "predictable" },
  })).ok()).toBe(true);
  await expect(page.getByTestId("message").filter({ hasText: "unrelated private reply" }).last()).toBeVisible();
  expect((await request.post(`/api/conversation/${origin.conversationId}/chat`, {
    data: { message: "echo: original background reply", model: "predictable" },
  })).ok()).toBe(true);
  await expect.poll(() => notes(page)).toContain("Backend reply: original background reply");
  expect(await notes(page)).not.toContain("unrelated private reply");

  await navigateWithinApp(page, "/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await speakAndDelegate(page, "off_view_task", "echo: delegated to the original chat");
  await expect.poll(() => tasks).toEqual([origin.conversationId]);
  await expect.poll(() => notes(page)).toContain('"delegation_id":"off_view_task","content":"Backend reply: edit predictable.go');
  await expect(page).toHaveURL(/\/new$/);
  expect(sessions).toEqual([origin.conversationId]);
  const returnToCall = page.getByTestId("live-call-origin");
  await expect(returnToCall).toContainText(origin.slug);
  await returnToCall.click();
  await expect(page.locator(".header-title")).toHaveText(origin.slug);
  await expect(bar).toBeVisible();
  await expect(returnToCall).toBeDisabled();

  // The strip and its controls fit a phone, in an archive too.
  expect((await request.post(`/api/conversation/${other.conversationId}/archive`)).ok()).toBe(true);
  await navigateWithinApp(page, `/c/${other.slug}`);
  await expect(page.locator(".header-title")).toHaveText(`${other.slug} (archived)`);
  await page.setViewportSize({ width: 320, height: 640 });
  const panel = await bar.boundingBox();
  const stop = await page.getByTestId("live-stop").boundingBox();
  expect(panel && stop && stop.x + stop.width <= panel.x + panel.width).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await expect(page.getByTestId("live-meter")).toBeVisible();
  await page.getByTestId("live-mute").click();
  await expect(bar).toContainText("Muted");
  await page.getByTestId("live-stop").click();
  await expect(bar).toBeHidden();
  expect(await page.evaluate(() => (window as any).__voiceTracksStopped)).toBe(1);
});

test("a call on /new starts one blank draft with the composer's selection, wherever the user browses meanwhile", async ({ page, request }) => {
  const other = await createConversationViaAPIWithDetails(request, "echo: browsing during permission");
  const profile = `VoicePersonal-${crypto.randomUUID()}`;
  expect((await request.post("/api/profiles", {
    data: { name: profile, model: "predictable", system_prompt: "You are a personal reflection partner." },
  })).ok()).toBe(true);
  await page.addInitScript((name) => localStorage.setItem("shelley.profile", name), profile);
  await mockVoiceBrowser(page, false, true);
  let drafts = 0;
  let startedId = "";
  let sessionSettings: Record<string, any> | null = null;
  const tasks: Array<{ id: string; body: Record<string, unknown> }> = [];
  await page.route("**/api/conversations/draft", (route) => {
    drafts++;
    return route.continue();
  });
  await page.route("**/api/conversation/*/live-session", (route) => {
    startedId = new URL(route.request().url()).pathname.split("/").at(-2)!;
    sessionSettings = route.request().postDataJSON();
    return route.fulfill({
      status: 201, contentType: "application/json",
      body: JSON.stringify({ session: { id: "live_new" }, transport: { sdp: "v=0" } }),
    });
  });
  page.on("request", (req) => {
    if (req.url().endsWith("/live-message")) {
      tasks.push({ id: new URL(req.url()).pathname.split("/").at(-2)!, body: req.postDataJSON() });
    }
  });
  const profilesLoaded = page.waitForResponse((response) => response.url().endsWith("/api/profiles") && response.ok());
  await page.goto("/new");
  await profilesLoaded;
  const call = page.getByTestId("live-call-header");
  await call.click();
  await expect(page.getByTestId("live-bar")).toContainText("Connecting");
  expect(drafts).toBe(0); // nothing is created before the microphone is granted
  await navigateWithinApp(page, `/c/${other.slug}`);
  await expect(page.locator(".header-title")).toHaveText(other.slug);
  await page.evaluate(() => (window as any).__releaseVoiceMicrophone());
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  expect(startedId).not.toBe("");
  expect(startedId).not.toBe(other.conversationId);
  expect(sessionSettings).toMatchObject({ model: "predictable", conversation_options: { profile } });
  expect((await (await request.get(`/api/conversation/${startedId}`)).json()).conversation.is_draft).toBe(true);

  await speakAndDelegate(page, "first_task", "echo: started by voice");
  await expect.poll(() => tasks.length).toBe(1);
  expect(tasks[0].id).toBe(startedId);
  expect(tasks[0].body).toMatchObject({
    model: "predictable", transcript: "echo: started by voice",
    conversation_options: expect.objectContaining({ profile, tool_overrides: expect.any(Object) }),
  });
  await expect.poll(async () => (await (await request.get(`/api/conversation/${startedId}`)).json()).conversation)
    .toMatchObject({ is_draft: false, model: "predictable" });
  expect(JSON.parse((await (await request.get(`/api/conversation/${startedId}`)).json()).conversation.conversation_options).profile).toBe(profile);
  expect(drafts).toBe(1);
  await expect(page.locator(".header-title")).toHaveText(other.slug);
  await expect.poll(() => notes(page)).toContain('"delegation_id":"first_task","content":"Backend reply: edit predictable.go');

  // A conversation under way has its own settings; the next call sends none.
  await page.getByTestId("live-call-origin").click();
  await expect(page.getByTestId("message").filter({ hasText: "Spoken request:\necho: started by voice" })).toHaveCount(1);
  await page.getByTestId("live-stop").click();
  await call.click();
  await expect(page.getByTestId("live-bar")).toContainText("Connecting");
  await page.evaluate(() => (window as any).__releaseVoiceMicrophone());
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  expect(sessionSettings).not.toHaveProperty("conversation_options");
  await call.click();
});
