import { expect, test } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test("Folded bash cards elide leading cd/env prefixes with a left ellipsis", async ({
  page,
  request,
}) => {
  const command = "cd /tmp && GOFLAGS=-tags=x go test ./ui -run TestBashFold";
  const slug = await createConversationViaAPI(request, `bash: ${command}`);
  await page.goto(`/c/${slug}`);
  await page.waitForLoadState("domcontentloaded");

  const bashTool = page.locator(".bash-tool").first();
  // The prefix is elided to the left, same ellipsis style as right-truncation.
  await expect(bashTool.locator(".bash-tool-summary-ellipsis")).toHaveText("...", {
    timeout: 30000,
  });
  await expect(bashTool.locator(".bash-tool-command")).toHaveText(
    "go test ./ui -run TestBashFold",
  );
  // The full command stays on the title tooltip and in the expanded view.
  await expect(bashTool.locator(".bash-tool-command")).toHaveAttribute("title", command);
  await bashTool.locator(".bash-tool-header").click();
  const details = bashTool.locator(".bash-tool-details");
  // The details start with the working-directory block; the command block follows.
  const commandBlock = details.locator(".bash-tool-code:not(.bash-tool-code-cwd)").first();
  await expect(commandBlock).toHaveText(command);
});
