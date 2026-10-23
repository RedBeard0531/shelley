import { test, expect, type Page } from "@playwright/test";
import { execFileSync } from "child_process";
import { mkdirSync, renameSync, writeFileSync } from "fs";
import { writeFile } from "fs/promises";
import { dirname, join } from "path";
import { createConversationViaAPI, testWorkingDirectory, withTempDir } from "./helpers";

// The hook prints .status-bar.json from the conversation's directory, so it
// shows nothing for every other spec's conversations on the shared server.
// A .status-bar.wait FIFO there holds it until the test writes to it.
const hookScript = `#!/bin/sh
[ ! -p .status-bar.wait ] || { read _ < .status-bar.wait; rm .status-bar.wait; }
[ ! -f .status-bar.json ] || cat .status-bar.json
`;

function installHook() {
  const hooks = join(dirname(testWorkingDirectory()), "home", ".config", "shelley", "hooks");
  mkdirSync(hooks, { recursive: true });
  const tmp = join(hooks, `.status-bar-${process.pid}`);
  writeFileSync(tmp, hookScript, { mode: 0o755 });
  // Replace, never rewrite in place: the server may be running it.
  renameSync(tmp, join(hooks, "status-bar"));
}

async function navigateWithinApp(page: Page, slug: string) {
  await page.evaluate((url) => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, `/c/${slug}`);
}

test("the status-bar hook shows the conversation's status and reruns after messages", async ({
  page,
  request,
}) => {
  test.skip(!!process.env.TEST_SERVER_URL, "installs a hook in the test server's home");
  installHook();
  await withTempDir("status-bar-hook-", async (cwd) => {
    const status = (s: object | string) =>
      writeFileSync(join(cwd, ".status-bar.json"), typeof s === "string" ? s : JSON.stringify(s));
    status({
      body: "feature-x `wip` http://localhost:8000/",
      bgcolor: "rgb(32, 74, 135)",
      color: "white",
    });
    const slug = await createConversationViaAPI(request, "Hello", { cwd });

    await page.addInitScript(() => {
      let init: Record<string, unknown> | undefined;
      Object.defineProperty(window, "__SHELLEY_INIT__", {
        configurable: true,
        get: () => init,
        set: (value) => {
          init = { ...value, is_exe_dev: true, hostname: "myvm.exe.xyz" };
        },
      });
    });
    await page.goto(`/c/${slug}`);
    const segment = page.getByTestId("status-bar-hook");
    // VM-local URLs point at the exe.dev proxy, like links in chat.
    // Backticks stay backticks: the body isn't chat text.
    await expect(segment).toHaveText("feature-x `wip` https://myvm.exe.xyz:8000/");
    await expect(segment).toHaveCSS("background-color", "rgb(32, 74, 135)");
    await expect(segment).toHaveCSS("color", "rgb(255, 255, 255)");
    await expect(segment.getByRole("link")).toHaveAttribute("href", "https://myvm.exe.xyz:8000/");
    // Its own row, above the composer.
    const row = (await segment.boundingBox())!;
    const input = (await page.getByTestId("message-input").boundingBox())!;
    expect(row.y + row.height).toBeLessThanOrEqual(input.y);
    expect(row.width).toBeGreaterThanOrEqual(input.width);

    // A message reruns the hook.
    const send = async () => {
      await page.getByTestId("message-input").fill("Hello");
      await page.getByTestId("send-button").click();
    };
    status({ body: "feature-y" });
    await send();
    await expect(segment).toHaveText("feature-y");
    await expect(segment).not.toHaveAttribute("style", /background/);

    status("not json");
    await send();
    await expect(segment).toHaveText(/^status-bar hook failed: invalid JSON output: /);

    status({ body: "feature-z", bgcolor: "rgb(32, 74, 135)" });
    await send();
    await expect(segment).toHaveText("feature-z");

    // Switching to another conversation keeps the row's place, blank, until
    // its hook answers, so the messages don't shift twice.
    await withTempDir("status-bar-hook-other-", async (other) => {
      writeFileSync(join(other, ".status-bar.json"), JSON.stringify({ body: "other-branch" }));
      execFileSync("mkfifo", [join(other, ".status-bar.wait")]);
      const otherSlug = await createConversationViaAPI(request, "Hello", { cwd: other });
      await navigateWithinApp(page, otherSlug);
      await expect(page.getByTestId("message").first()).toBeVisible();
      await expect(segment).toHaveText(/^\s*$/);
      await expect(segment).not.toHaveAttribute("style", /background/);
      await writeFile(join(other, ".status-bar.wait"), "go\n");
      await expect(segment).toHaveText("other-branch");

      // A conversation whose hook prints nothing has no row.
      status("");
      await navigateWithinApp(page, slug);
      await expect(segment).toHaveCount(0);
    });
  });
});
