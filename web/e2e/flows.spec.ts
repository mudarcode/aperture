import { expect, test } from "@playwright/test";

// The demo shop must have produced traffic before these run (see CI: the
// stack is started and given time to generate traces).

test("services overview lists the demo services with health stats", async ({ page }) => {
  await page.goto("/services?window=15m");
  const table = page.getByRole("table", { name: "Service health" });
  for (const svc of ["gateway", "checkout", "catalog", "inventory", "payments"]) {
    await expect(table.getByRole("link", { name: svc, exact: true })).toBeVisible();
  }
});

test("service page shows charts and operations", async ({ page }) => {
  await page.goto("/services/checkout?window=15m");
  await expect(page.getByRole("heading", { level: 1 })).toContainText("checkout");
  await expect(page.getByText("Latency", { exact: true })).toBeVisible();
  await expect(page.getByRole("table", { name: "Operations of checkout" }).getByText("POST /checkout")).toBeVisible();
});

test("search finds a checkout trace and the waterfall explains it", async ({ page }) => {
  await page.goto("/traces?window=15m&service=gateway&operation=POST+%2Fapi%2Fcheckout");
  const firstTrace = page.getByRole("table", { name: "Traces" }).getByRole("link", { name: /POST \/api\/checkout/ }).first();
  await expect(firstTrace).toBeVisible();
  await firstTrace.click();

  await expect(page).toHaveURL(/\/traces\/[0-9a-f]{32}$/);
  const tree = page.getByRole("tree", { name: "Spans" });
  // gateway → checkout → catalog/inventory/payments: a checkout trace spans 5 services.
  await expect(tree.getByRole("treeitem").first()).toContainText("POST /api/checkout");
  await expect(tree.getByText("POST /charge").first()).toBeVisible();
  await expect(page.getByRole("region", { name: "Time by service" })).toContainText("payments");

  // Selecting a span shows its attributes.
  await tree.getByRole("button", { name: /fraud\.score/ }).first().click();
  const details = page.getByRole("complementary", { name: "Span details" });
  await expect(details.getByRole("heading", { name: "fraud.score" })).toBeVisible();
  await expect(details).toContainText("service.name");

  // Collapsing the root hides every other row.
  const before = await tree.getByRole("treeitem").count();
  await page.getByRole("button", { name: "Collapse POST /api/checkout" }).click();
  await expect(tree.getByRole("treeitem")).toHaveCount(1);
  await page.getByRole("button", { name: "Expand all" }).click();
  await expect(tree.getByRole("treeitem")).toHaveCount(before);
});

test("failed-trace filter only returns traces with errors", async ({ page }) => {
  await page.goto("/traces?window=15m&status=error");
  const rows = page.getByRole("table", { name: "Traces" }).locator("tbody tr");
  await expect(rows.first()).toBeVisible();
  const n = await rows.count();
  for (let i = 0; i < n; i++) {
    await expect(rows.nth(i).locator("td").nth(3)).not.toHaveText("0");
  }
});

test("dependency map shows the call graph", async ({ page }) => {
  await page.goto("/dependencies?window=15m");
  const calls = page.getByRole("table", { name: "Service calls" });
  await expect(calls.getByRole("row", { name: /gateway\s+checkout/ })).toBeVisible();
  await expect(calls.getByRole("row", { name: /checkout\s+payments/ })).toBeVisible();
  expect(await page.locator(".react-flow__node").count()).toBeGreaterThanOrEqual(5);
});
