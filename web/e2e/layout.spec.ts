import { expect, test } from "@playwright/test";

for (const path of ["/services", "/traces", "/dependencies"]) {
  test(`no horizontal page scroll on ${path}`, async ({ page }) => {
    await page.goto(path + "?window=15m");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });
}
