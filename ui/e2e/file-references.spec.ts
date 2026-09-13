import { test, expect } from "@playwright/test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { basename, join } from "node:path";
import { createConversationViaAPI, createConversationViaAPIWithDetails } from "./helpers";

// File references: inline code marked with 📄 renders as a clickable chip that
// opens the editor at the referenced line, while path-shaped inline code
// without the marker stays plain code. The predictable "markdown: " prefix
// echoes the rest of the message back as the agent's reply, so these tests own
// the markdown they assert on.
//
// The referenced file is 60 lines so that revealing a line is observable: Monaco
// only renders the lines in view, so line 40 being visible proves the editor
// scrolled to it, and a reference past the end proves the clamp.

const LINES = Array.from({ length: 60 }, (_, i) => `marker-${String(i + 1).padStart(2, "0")}`);

let dir: string;
let notesPath: string;
let tildeDir: string;

test.beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "shelley-file-ref-"));
  notesPath = join(dir, "notes.txt");
  writeFileSync(notesPath, LINES.join("\n") + "\n");
  // A second file under $HOME, so a ~-rooted reference opens through the server
  // (the browser never expands ~ itself).
  tildeDir = mkdtempSync(join(homedir(), "shelley-e2e-ref-"));
  writeFileSync(join(tildeDir, "home.txt"), "only line\n");
});

test.afterAll(() => {
  rmSync(dir, { recursive: true, force: true });
  rmSync(tildeDir, { recursive: true, force: true });
});

function ref(text: string): string {
  return "`" + text + "`";
}

test.describe("File references", () => {
  test("only marked spans become chips", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      `markdown: marked ${ref("📄notes.txt:40")} and unmarked ${ref("notes.txt:41")} and a range ${ref("📄notes.txt:1-3")}`,
      { cwd: dir },
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("marked", { timeout: 30000 });

    const chips = agent.locator("a.file-ref");
    await expect(chips).toHaveCount(2);
    // A chip carries its reference in its own text, which is what the click
    // handler reads back: what the reader sees is what opens.
    await expect(chips.first()).toHaveText("📄notes.txt:40");
    await expect(chips.nth(1)).toHaveText("📄notes.txt:1-3");

    // An unmarked path-shaped span is code, and is not a link.
    const plain = agent.locator("code", { hasText: "notes.txt:41" });
    await expect(plain).toHaveCount(1);
    await expect(plain.locator("a")).toHaveCount(0);
  });

  test("clicking a reference opens the editor at that line", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      `markdown: ref ${ref("📄notes.txt:40")} and a range ${ref("📄notes.txt:2-4")}`,
      { cwd: dir },
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    const chip = agent.locator("a.file-ref").first();
    await expect(chip).toBeVisible({ timeout: 30000 });

    await chip.click();
    const modal = page.getByRole("dialog", { name: /Edit .*notes\.txt/ });
    await expect(modal).toBeVisible();
    await expect(modal.locator(".agents-md-header-path")).toHaveText(notesPath);
    // Revealed: the referenced line is in view, and the first line is not.
    await expect(modal.locator(".view-lines")).toContainText("marker-40");
    await expect(modal.locator(".view-lines")).not.toContainText("marker-01");
    // A single-line reference highlights the line instead of selecting it: the
    // modal is editable and autosaves, so a whole-line selection would let a
    // stray keystroke replace the line, but a bare cursor is too easy to miss.
    const highlight = modal.locator(".monaco-editor .file-ref-line");
    await expect(highlight.first()).toBeVisible();
    const lineBox = await modal.locator(".view-line", { hasText: "marker-40" }).first().boundingBox();
    const highlightBoxes = await highlight.evaluateAll((els) =>
      els.map((el) => el.getBoundingClientRect().toJSON()),
    );
    // The highlight covers the referenced line, not merely somewhere on screen.
    // (A wrapped line is painted on every row it occupies, so compare boxes
    // rather than counting rows.)
    expect(
      highlightBoxes.some((box) => Math.abs(box.y - (lineBox?.y ?? 0)) < 4),
    ).toBe(true);
    const cursorLeft = await modal
      .locator(".cursors-layer .cursor")
      .evaluate((el) => parseFloat(getComputedStyle(el).left));
    expect(cursorLeft).toBeLessThan(20);
  });

  test("a range reference selects through the range, not one line", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, `markdown: ref ${ref("📄notes.txt:2-4")}`, {
      cwd: dir,
    });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const chip = page.locator(".message-agent").last().locator("a.file-ref").first();
    await expect(chip).toBeVisible({ timeout: 30000 });
    await chip.click();
    const modal = page.getByRole("dialog", { name: /Edit .*notes\.txt/ });
    await expect(modal).toBeVisible();
    // A range is selected, so the cursor sits past the start of a line rather
    // than at column 1 of the first, and the referenced lines are highlighted.
    const cursorLeft = await modal
      .locator(".cursors-layer .cursor")
      .evaluate((el) => parseFloat(getComputedStyle(el).left));
    expect(cursorLeft).toBeGreaterThan(20);
    // The three referenced lines are highlighted (each on every row it wraps
    // onto, so at least one row per line).
    expect(await modal.locator(".monaco-editor .file-ref-line").count()).toBeGreaterThanOrEqual(3);
  });

  test("the export preview renders references as plain code", async ({ page, request }) => {
    const { conversationId } = await createConversationViaAPIWithDetails(
      request,
      `markdown: ref ${ref("📄notes.txt:2")}`,
      { cwd: dir },
    );
    await page.goto(`/export/${conversationId}`);
    await page.waitForLoadState("domcontentloaded");

    // The export page mounts its own app with no editor provider, so a chip
    // there would be a link that does nothing: the reference stays the code
    // span the markdown file carries. (The default project is a phone
    // viewport, where the source and preview panes are tabs.)
    await page.getByRole("tab", { name: "Preview" }).click();
    const preview = page.locator(".export-preview");
    await expect(preview).toContainText("notes.txt:2", { timeout: 30000 });
    await expect(preview.locator("a.file-ref")).toHaveCount(0);
    // The user's message and the agent's echo both quote the raw span.
    await expect(preview.locator("code", { hasText: "📄notes.txt:2" }).first()).toBeVisible();
  });

  test("a chip whose text is split across elements cannot be activated", async ({ page, request }) => {
    // A prompt-injected page echoed into a reply could emit this: a hidden
    // prefix plus the visible label, so the chip reads "notes.txt:1" while
    // textContent names a different file. The handler requires a single text
    // node, so the click must do nothing.
    const slug = await createConversationViaAPI(
      request,
      `markdown: see <a class="file-ref">`+
        `<span style="display:none">📄/etc/</span>notes.txt:1</a>`,
      { cwd: dir },
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    const chip = agent.locator("a.file-ref").first();
    await expect(chip).toBeVisible({ timeout: 30000 });
    await chip.click();
    // No editor: the label is not one text node, so it is not a reference.
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("a marker outside agent output is never a chip", async ({ page, request }) => {
    // References are a feature of agent replies: the request text (rendered as
    // the user's own message) carries the same marker, and must keep the code
    // span however that host is configured to render markdown.
    const slug = await createConversationViaAPI(request, "markdown: ref `📄notes.txt:1`", {
      cwd: dir,
    });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent.locator("a.file-ref").first()).toBeVisible({ timeout: 30000 });
    const user = page.locator(".message-user").last();
    await expect(user).toContainText("📄notes.txt:1");
    await expect(user.locator("a.file-ref")).toHaveCount(0);
  });

  test("an invisible chip cannot be activated", async ({ page, request }) => {
    // A hidden chip is still in the tab order, so Enter on it would open a file
    // the reader cannot see. The handler requires the label to fit the box the
    // anchor paints, so this one refuses the click however it arrives.
    const slug = await createConversationViaAPI(
      request,
      `markdown: see <a class="file-ref sr-only">📄notes.txt:1</a> here`,
      { cwd: dir },
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const chip = page.locator(".message-agent").last().locator("a.file-ref").first();
    await expect(chip).toHaveCount(1, { timeout: 30000 });
    await chip.dispatchEvent("click");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await chip.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("a reference past the end of the file is clamped", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      `markdown: ref ${ref("📄notes.txt:9999")}`,
      { cwd: dir },
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const chip = page.locator(".message-agent").last().locator("a.file-ref").first();
    await expect(chip).toBeVisible({ timeout: 30000 });
    await chip.click();
    const modal = page.getByRole("dialog", { name: /Edit .*notes\.txt/ });
    await expect(modal).toBeVisible();
    // Revealed at the last line rather than failing on a line that isn't there.
    await expect(modal.locator(".view-lines")).toContainText("marker-60");
  });

  test("a focused reference opens with the keyboard", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, `markdown: ref ${ref("📄notes.txt:1")}`, {
      cwd: dir,
    });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const chip = page.locator(".message-agent").last().locator("a.file-ref").first();
    await expect(chip).toBeVisible({ timeout: 30000 });
    await chip.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("dialog", { name: /Edit .*notes\.txt/ })).toBeVisible();
  });

  test("a ~-rooted reference reaches the file through the server", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      `markdown: ref ${ref(`📄~/${basename(tildeDir)}/home.txt:1`)}`,
      { cwd: dir },
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const chip = page.locator(".message-agent").last().locator("a.file-ref").first();
    await expect(chip).toBeVisible({ timeout: 30000 });
    await chip.click();
    // The editor keeps the tilde form (the browser does not know $HOME) and the
    // server expands it, so the file loads.
    const modal = page.getByRole("dialog", { name: /Edit .*home\.txt/ });
    await expect(modal).toBeVisible();
    await expect(modal.locator(".agents-md-header-path")).toHaveText(`~/${basename(tildeDir)}/home.txt`);
    await expect(modal.locator(".view-lines")).toContainText("only line");
  });
});
