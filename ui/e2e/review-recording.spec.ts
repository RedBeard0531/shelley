import { expect, test, type Locator, type Page, type Route } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createConversationViaAPI, installTranscriptionAvailability, withTempDir } from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

// A repository whose HEAD commit has a tour: a heading, then one chunk.
function tourRepo(dir: string): string {
  const repo = join(dir, "repo");
  mkdirSync(join(repo, "src"), { recursive: true });
  git(repo, "init");
  git(repo, "config", "user.name", "Review Test");
  git(repo, "config", "user.email", "review@example.com");
  const lines = Array.from({ length: 40 }, (_, index) => `const value${index + 1} = ${index + 1};`);
  writeFileSync(join(repo, "src", "app.ts"), lines.join("\n") + "\n");
  git(repo, "add", ".");
  git(repo, "commit", "-m", "Base\n\nPrompt: review recording base");
  lines[19] = "const renamedValue = 20;";
  writeFileSync(join(repo, "src", "app.ts"), lines.join("\n") + "\n");
  git(repo, "commit", "-am", "Rename value20\n\nPrompt: review recording rename");
  const scaffold = JSON.parse(
    execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, "HEAD"], { encoding: "utf8" }),
  );
  const tour = {
    ...scaffold,
    title: "Rename value20",
    intro: "One rename.",
    chunks: [{ header: "## The rename" }, { ...scaffold.chunks[0], comment: "The new name." }],
  };
  writeFileSync(join(dir, "tour.json"), JSON.stringify(tour));
  execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", join(dir, "tour.json")]);
  return repo;
}

// A microphone and recorder that produce a few bytes per second.
async function installMicrophone(page: Page, transcription = true) {
  await installTranscriptionAvailability(page, transcription);
  await page.addInitScript(() => {
    class Track extends EventTarget {
      kind = "audio";
      stop() {}
    }
    class Stream {
      tracks = [new Track()];
      getTracks() {
        return this.tracks;
      }
      getAudioTracks() {
        return this.tracks;
      }
      getVideoTracks() {
        return [];
      }
    }
    class Recorder extends EventTarget {
      static isTypeSupported() {
        return true;
      }
      state = "inactive";
      mimeType: string;
      ondataavailable: ((event: Event) => void) | null = null;
      private timer = 0;
      constructor(_stream: unknown, options?: { mimeType?: string }) {
        super();
        this.mimeType = options?.mimeType ?? "audio/webm";
      }
      start(timeslice: number) {
        this.state = "recording";
        this.timer = window.setInterval(() => this.emit("chunk"), timeslice);
      }
      stop() {
        window.clearInterval(this.timer);
        this.state = "inactive";
        this.emit("last");
        setTimeout(() => this.dispatchEvent(new Event("stop")));
      }
      emit(text: string) {
        const event = new Event("dataavailable");
        Object.defineProperty(event, "data", { value: new Blob([text], { type: this.mimeType }) });
        this.ondataavailable?.(event);
      }
    }
    Object.defineProperty(window, "MediaRecorder", { value: Recorder, configurable: true });
    Object.defineProperty(window, "AudioContext", { value: undefined, configurable: true });
    Object.defineProperty(navigator, "mediaDevices", {
      configurable: true,
      value: { getUserMedia: async () => new Stream() },
    });
  });
}

async function openTour(page: Page, slug: string, viewport = { width: 1500, height: 900 }) {
  await page.setViewportSize(viewport);
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
  await page.locator(".overflow-menu-item", { hasText: /diffs/i }).click();
  const overlay = page.locator(".diff-viewer-overlay");
  await expect(overlay.locator(".commit-tour-view")).toBeVisible({ timeout: 30000 });
  return overlay;
}

const switcherTop = async (overlay: Locator) =>
  (await overlay.locator(".diff-viewer-view-switcher").boundingBox())?.y;

// The live status takes over the header's selector area instead of adding a
// strip, so nothing below the header moves.
async function expectInHeader(overlay: Locator, top: number | undefined) {
  await expect(
    overlay.locator(".diff-viewer-header").getByTestId("review-recording-status"),
  ).toBeVisible();
  expect(await switcherTop(overlay)).toBe(top);
}

function diffRow(page: Page, text: string) {
  return page.locator('[data-review-file="src/app.ts"] diffs-container [data-line]', {
    hasText: text,
  });
}

interface Chat {
  message: string;
}

test.describe("Narrated review recording", () => {
  test("captures what was pointed at and selected, then hands it to the conversation", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: tourRepo(dir) });
      await installMicrophone(page);
      const chats: Chat[] = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        chats.push(route.request().postDataJSON() as Chat);
        await route.fulfill({ status: 202, contentType: "application/json", body: "{}" });
      });
      const overlay = await openTour(page, slug);
      const status = overlay.getByTestId("review-recording-status");
      const top = await switcherTop(overlay);

      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-record-stop")).toBeVisible();
      // With nothing pointed at, the status names what is on screen.
      await expect(overlay.getByTestId("review-recording-where")).toHaveText("Commit message");
      await expectInHeader(overlay, top);
      await expect(overlay.getByTestId("review-recording-bar")).toHaveCount(0);

      // The viewer cannot be closed mid-recording.
      await expect(overlay.locator(".diff-viewer-close")).toBeDisabled();
      await page.keyboard.press("Escape");
      await expect(overlay).toBeVisible();

      await diffRow(page, "renamedValue").last().hover();
      await expect(overlay.getByTestId("review-recording-where")).toHaveText(
        /The rename.*src\/app\.ts.*new 20$/,
      );

      // Drag-select within the row; locator positions retry if it re-renders.
      const row = diffRow(page, "renamedValue").last();
      await row.hover({ position: { x: 8, y: 8 } });
      await page.mouse.down();
      await row.hover({ position: { x: 120, y: 8 } });
      await page.mouse.up();
      await expect(status).toContainText("Selected");

      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      await expect(overlay.locator(".diff-viewer-close")).toBeEnabled();
      // Once sent, the selectors are back.
      await expect(status).toHaveCount(0);
      await expect(overlay.locator(".diff-viewer-selectors-row")).not.toHaveAttribute("inert");

      expect(chats).toHaveLength(1);
      const audioPath = chats[0].message.replace(/^\/transcription /, "");
      expect(audioPath).toMatch(/^\/.*review-.*\.webm$/);
      expect(readFileSync(audioPath, "utf8")).toMatch(/^(chunk)*last$/);
      const review = JSON.parse(readFileSync(`${audioPath}.review.json`, "utf8"));
      expect(review).toMatchObject({
        version: 1,
        source: "diff-viewer",
        mime_type: "audio/webm;codecs=opus",
      });
      const events = review.events as Array<Record<string, unknown> & { ms: number; type: string }>;
      expect(events.map((event) => event.ms)).toEqual(
        [...events.map((e) => e.ms)].sort((a, b) => a - b),
      );
      expect(events[0]).toMatchObject({ ms: 0, type: "view", mode: "tour" });
      expect(events.at(-1)?.type).toBe("stop");
      expect(events).toContainEqual(
        expect.objectContaining({ type: "pointer", file: "src/app.ts", side: "new", line: 20 }),
      );
      expect(events).toContainEqual(
        expect.objectContaining({
          type: "selection",
          file: "src/app.ts",
          line: 20,
          text: expect.stringContaining("renamed"),
        }),
      );
      expect(events).toContainEqual(
        expect.objectContaining({
          type: "on_screen",
          items: expect.arrayContaining([
            expect.stringMatching(/src\/app\.ts.*\(old \d+–\d+, new \d+–\d+\)/),
          ]),
        }),
      );
      // Stopping is not part of the review.
      expect(events.filter((event) => event.type === "click")).toEqual([]);
    });
  });

  test("offers no narrated review without transcription", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: tourRepo(dir) });
      await installMicrophone(page, false);
      const overlay = await openTour(page, slug);
      await expect(overlay.getByTestId("review-record-start")).toHaveCount(0);
    });
  });

  test("shows the live status in the mobile header", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: tourRepo(dir) });
      await installMicrophone(page);
      const overlay = await openTour(page, slug, { width: 390, height: 844 });
      const top = await switcherTop(overlay);
      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-recording-where")).toHaveText("Commit message");
      await expectInHeader(overlay, top);
      // The pickers are covered rather than squeezed, and cannot be reached.
      await expect(overlay.locator(".diff-viewer-mobile-selectors")).toHaveAttribute("inert", "");
    });
  });

  test("keeps an unsent recording across reloads until the conversation accepts it", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: tourRepo(dir) });
      await installMicrophone(page);
      let uploads = 0;
      page.on("request", (req) => {
        if (req.url().includes("/api/upload/raw") && req.method() === "POST") uploads++;
      });
      let accept = false;
      const chats: Chat[] = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        if (!accept) {
          await route.fulfill({ status: 503, body: "unavailable" });
          return;
        }
        chats.push(route.request().postDataJSON() as Chat);
        await route.fulfill({ status: 202, contentType: "application/json", body: "{}" });
      });

      let overlay = await openTour(page, slug);
      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-record-stop")).toBeVisible();
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-error")).toContainText("unavailable");
      await expect(overlay.getByTestId("review-recording-pending")).toHaveCount(1);
      expect(uploads).toBe(2);

      await page.reload();
      overlay = await openTour(page, slug);
      await expect(overlay.getByTestId("review-recording-pending")).toContainText(
        "Unsent recording",
      );

      // A new recording leaves the unsent one in place, waiting its turn.
      accept = true;
      const top = await switcherTop(overlay);
      await overlay.getByTestId("review-record-start").click();
      await expectInHeader(overlay, top);
      await expect(overlay.getByTestId("review-recording-send")).toBeDisabled();
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      await expect(overlay.getByTestId("review-recording-pending")).toHaveCount(1);
      expect(uploads).toBe(4);
      expect(chats).toHaveLength(1);

      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-pending")).toHaveCount(0);
      // Both files were already on the server; only the handoff was retried.
      expect(uploads).toBe(4);
      expect(chats).toHaveLength(2);
      for (const chat of chats)
        expect(chat.message).toMatch(/^\/transcription \/.*review-.*\.webm$/);

      await page.reload();
      overlay = await openTour(page, slug);
      await expect(overlay.getByTestId("review-recording-bar")).toHaveCount(0);
    });
  });
});
