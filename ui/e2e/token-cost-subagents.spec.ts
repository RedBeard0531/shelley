import { expect, test, type Page, type APIRequestContext } from "@playwright/test";
import type { Message } from "../src/types";

const price = { input: 2, output: 8, cache_read: 0.2, cache_write: 2.5 };
const reviewer = {
  model: "review-model",
  url: "https://review.test",
  llm_calls: 4,
  input_tokens: 2_000_000,
  output_tokens: 250_000,
  cache_read_input_tokens: 5_000_000,
  cache_creation_input_tokens: 200_000,
  estimated_usd: 7.5,
  reported_usd: 999,
  cost: price,
};
const unpriced = {
  model: "unpriced-model",
  url: "https://unpriced.test",
  llm_calls: 1,
  input_tokens: 3000,
  output_tokens: 500,
  cache_read_input_tokens: 0,
  cache_creation_input_tokens: 0,
  estimated_usd: 0,
  reported_usd: 0.75,
  cost: null,
};
const usage = {
  llm_calls: 5,
  estimated_usd: 7.5,
  reported_usd: 999.75,
  unpriced_reported_usd: 0.75,
  unpriced_models: [unpriced.model],
  unpriced_calls: 1,
  per_model: [reviewer, unpriced],
};

async function openSpend(
  page: Page,
  request: APIRequestContext,
  { direct = true, pricingFailed = false }: { direct?: boolean; pricingFailed?: boolean } = {},
) {
  await page.route("**/api/model-costs", (route) =>
    route.fulfill(pricingFailed ? { status: 503 } : { json: { costs: { "main-model": price } } }),
  );
  const generated = await request.post("/debug/loremipsum?json=1", {
    form: { size: "1", model: "predictable" },
  });
  expect(generated.ok()).toBeTruthy();
  const { conversation_id: id } = await generated.json();
  const response = await request.get(`/api/conversation/${id}`);
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  const agents = (body.messages as Message[]).filter((message) => message.type === "agent");
  for (const message of body.messages as Message[]) {
    message.usage_data = null;
    message.other_usage_data = null;
  }
  if (direct) {
    agents[0].usage_data = JSON.stringify({
      model: "main-model",
      input_tokens: 1_000_000,
      output_tokens: 100_000,
      cache_read_input_tokens: 2_000_000,
      cache_creation_input_tokens: 100_000,
      cost_usd: 999,
    });
    agents[0].other_usage_data = JSON.stringify([
      { purpose: "compaction", model: "main-model", input_tokens: 100_000, output_tokens: 10_000 },
    ]);
  }
  await page.route(`**/api/conversation/${id}`, (route) => route.fulfill({ json: body }));
  await page.goto(`/c/${body.conversation.slug}`);
  await page.locator(".context-usage-label:visible").click();
  return page.locator(".chat-context-popup");
}

for (const width of [393, 1280]) {
  test(`per-model main and sub-agent spend with subtotals (${width}px)`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/conversation/*/subagent-usage", (route) =>
      route.fulfill({ json: usage }),
    );
    const popup = await openSpend(page, request);
    const main = popup.getByRole("region", { name: "Main conversation", exact: true });
    const subagents = popup.getByRole("region", { name: "Sub-agents", exact: true });
    await expect(main).toBeVisible();
    await expect(subagents).toBeVisible();
    await expect(main).toContainText("main-model");
    await expect(main).not.toContainText("review-model");
    await expect(main).toContainText("Other (indirect)");
    await expect(main.getByTestId("conversation-cost-subtotal")).toContainText("$3.73");
    await expect(subagents.getByTestId("subagent-cost-row")).toContainText("$8.25");
    await expect(popup.getByTestId("token-cost-total")).toContainText("≈$11.98");
    const models = subagents.locator(".token-cost-model-breakdown");
    await expect(models).toHaveCount(2);
    await expect(models.nth(0).locator(".token-cost-model-row")).toHaveText(/review-model.*\$7.50/);
    await expect(models.nth(0).locator(".token-cost-legend-row")).toHaveText([
      /Output\s*250k\s*@ \$8\/M\s*\$2.00/,
      /Input\s*2.0M\s*@ \$2\/M\s*\$4.00/,
      /Cache write\s*200k\s*@ \$2.50\/M\s*\$0.500/,
      /Cache read\s*5.0M\s*@ \$0.20\/M\s*\$1.00/,
    ]);
    await expect(models.nth(1)).toContainText("unpriced-model");
    await expect(models.nth(1)).toContainText("$0.750 reported");
    await expect(models.nth(1).locator(".token-cost-legend-unit")).toHaveCount(0);
    await expect(popup).toContainText("1 call has no model pricing");
    await expect(subagents).toContainText("Includes nested sub-agents and indirect usage.");

    const mainBox = (await main.boundingBox())!;
    const subBox = (await subagents.boundingBox())!;
    if (width > 768) {
      expect(Math.abs(mainBox.y - subBox.y)).toBeLessThan(1);
      expect(subBox.x).toBeGreaterThanOrEqual(mainBox.x + mainBox.width);
    } else {
      expect(subBox.y).toBeGreaterThanOrEqual(mainBox.y + mainBox.height);
    }
    const popupBox = (await popup.boundingBox())!;
    expect(popupBox.x).toBeGreaterThanOrEqual(0);
    expect(popupBox.x + popupBox.width).toBeLessThanOrEqual(width);
    expect(await popup.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  });
}

test("sub-agent models and totals without direct usage", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ json: usage }),
  );
  const popup = await openSpend(page, request, { direct: false });
  await expect(popup).toContainText("No direct usage data yet.");
  await expect(popup.getByRole("region", { name: "Sub-agents", exact: true })).toContainText(
    "review-model",
  );
  await expect(popup.getByTestId("token-cost-total")).toContainText("≈$8.25");
});

test("unpriced sub-agent usage is not presented as free", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({
      json: {
        ...usage,
        llm_calls: 1,
        estimated_usd: 0,
        reported_usd: 0,
        unpriced_reported_usd: 0,
        per_model: [{ ...unpriced, reported_usd: 0 }],
      },
    }),
  );
  const popup = await openSpend(page, request, { direct: false });
  const subagents = popup.getByRole("region", { name: "Sub-agents", exact: true });
  await expect(subagents.locator(".token-cost-model-breakdown")).toContainText("no pricing");
  await expect(subagents.getByTestId("subagent-cost-row")).toContainText("no pricing");
  await expect(popup).toContainText("total may be incomplete");
  await expect(popup.getByTestId("token-cost-total")).toBeHidden();
});

test("keeps conversations without sub-agents compact", async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  const popup = await openSpend(page, request);
  await expect(popup.getByTestId("token-cost-total")).toHaveText("Total≈$3.73");
  await expect(popup.getByRole("region", { name: "Sub-agents", exact: true })).toBeHidden();
  expect((await popup.boundingBox())!.width).toBe(440);
});

test("shows a zero total when sub-agent pricing is known and free", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({
      json: {
        ...usage,
        llm_calls: 4,
        estimated_usd: 0,
        reported_usd: 0,
        unpriced_reported_usd: 0,
        unpriced_calls: 0,
        unpriced_models: [],
        per_model: [
          {
            ...reviewer,
            estimated_usd: 0,
            reported_usd: 0,
            cost: { input: 0, output: 0, cache_read: 0, cache_write: 0 },
          },
        ],
      },
    }),
  );
  const popup = await openSpend(page, request, { direct: false });
  await expect(popup.getByTestId("subagent-cost-row")).toHaveText("Subtotal$0");
  await expect(popup.getByTestId("token-cost-total")).toHaveText("Total≈$0");
  await expect(popup).not.toContainText("no pricing");
});

test("warns when sub-agent details cannot be fetched", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ status: 500, body: "unavailable" }),
  );
  const popup = await openSpend(page, request);
  await expect(popup.getByTestId("token-cost-total")).toHaveText("Total≈$3.73");
  await expect(popup).toContainText("Subagent cost unavailable; total may be incomplete.");
  await expect(popup).not.toContainText("Loading total");
});

test("distinguishes the same sub-agent model at different endpoints", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({
      json: {
        ...usage,
        llm_calls: 8,
        estimated_usd: 15,
        reported_usd: 1998,
        unpriced_reported_usd: 0,
        unpriced_models: [],
        unpriced_calls: 0,
        per_model: [reviewer, { ...reviewer, url: "https://second.test/another-provider" }],
      },
    }),
  );
  const popup = await openSpend(page, request);
  const models = popup
    .getByRole("region", { name: "Sub-agents", exact: true })
    .locator(".token-cost-model-breakdown");
  await expect(models).toHaveCount(2);
  await expect(models.nth(0).locator(".token-cost-model-source")).toHaveText(reviewer.url);
  await expect(models.nth(1).locator(".token-cost-model-source")).toHaveText(
    "https://second.test/another-provider",
  );
  await expect(popup.getByTestId("subagent-cost-row")).toHaveCSS("border-top-style", "solid");
  await expect(popup.getByTestId("conversation-cost-subtotal")).toHaveCSS(
    "border-top-style",
    "solid",
  );
});

test("does not count failed pricing lookups as confirmed unpriced calls", async ({
  page,
  request,
}) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ json: usage }),
  );
  const popup = await openSpend(page, request, { pricingFailed: true });
  await expect(popup).toContainText("Pricing lookup failed");
  await expect(popup).toContainText("1 call has no model pricing");
  await expect(popup).not.toContainText("3 calls have no model pricing");
});

test("keeps a tall breakdown above its toggle on short desktop viewports", async ({
  page,
  request,
}) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ json: usage }),
  );
  const popup = await openSpend(page, request);
  await expect(popup.getByTestId("token-cost-total")).toContainText("$11.98");
  const label = page.locator(".context-usage-label:visible");
  await expect
    .poll(async () => {
      const popupBox = (await popup.boundingBox())!;
      const labelBox = (await label.boundingBox())!;
      return popupBox.y + popupBox.height - labelBox.y;
    })
    .toBeLessThanOrEqual(0);
  await label.click();
  await expect(popup).toBeHidden();
});
