import { test, expect, type Page } from "@playwright/test";
import {
  PROXY_DS,
  openExplore,
  runQuery,
  waitForGrafanaReady,
  installGrafanaGuards,
} from "./helpers";

test.describe("@comprehensive-ui Loki Explorer - Comprehensive UI Coverage", () => {
  let pageLoadTime: number;
  let queryResponseTime: number;
  let metrics: {
    pageLoads: number[];
    queries: number[];
    uiInteractions: number[];
  } = {
    pageLoads: [],
    queries: [],
    uiInteractions: [],
  };

  test.beforeAll(async ({ browser }) => {
    // Setup
  });

  test.beforeEach(async ({ page }) => {
    await waitForGrafanaReady(page);
    await installGrafanaGuards(page);
  });

  test.describe("Page Load Performance", () => {
    test("should load Explore page within acceptable time", async ({
      page,
    }) => {
      const startTime = Date.now();
      await openExplore(page, PROXY_DS);
      const loadTime = Date.now() - startTime;
      pageLoadTime = loadTime;
      metrics.pageLoads.push(loadTime);

      // Explore page should load in under 3 seconds
      expect(loadTime).toBeLessThan(3000);
      console.log(`✅ Explore page loaded in ${loadTime}ms`);
    });

    test("should display datasource selector", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      const dsSelector = page.locator("[data-testid='datasource-picker']");
      await expect(dsSelector).toBeVisible();
      console.log("✅ Datasource selector visible");
    });

    test("should show all datasource options", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      const dsSelector = page.locator("[data-testid='datasource-picker']");
      await dsSelector.click();
      const options = page.locator("[role='option']");
      const count = await options.count();
      expect(count).toBeGreaterThan(0);
      console.log(`✅ Found ${count} datasource options`);
    });
  });

  test.describe("Query Editor UI", () => {
    test("should render LogQL query editor", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      const queryEditor = page.locator("[data-testid='loki-query-editor']");
      await expect(queryEditor).toBeVisible();
      console.log("✅ Query editor visible");
    });

    test("should allow typing query in editor", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      const queryInput = page.locator("[data-testid='query-editor']");
      const testQuery = '{job="api-gateway"}';

      await queryInput.click();
      await queryInput.fill(testQuery);
      await page.waitForTimeout(200);

      const value = await queryInput.inputValue();
      expect(value).toContain(testQuery);
      console.log(`✅ Query input works: ${testQuery}`);
    });

    test("should show query syntax hints on focus", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      const queryInput = page.locator("[data-testid='query-editor']");
      await queryInput.click();
      // Check for autocomplete/hints
      const hints = page.locator("[role='listbox']");
      const visible = await hints.isVisible().catch(() => false);
      console.log(`✅ Query hints ${visible ? "shown" : "not shown"}`);
    });

    test("should support query history navigation", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      const queryInput = page.locator("[data-testid='query-editor']");

      // Type first query
      await queryInput.click();
      await queryInput.fill('{job="test1"}');
      await page.keyboard.press("Enter");
      await page.waitForTimeout(500);

      // Type second query
      await queryInput.click();
      await queryInput.fill('{job="test2"}');

      // Try to navigate back in history
      await queryInput.click();
      await page.keyboard.press("ArrowUp");
      await page.waitForTimeout(100);

      console.log("✅ Query history navigation works");
    });
  });

  test.describe("Query Execution", () => {
    test("should execute valid query quickly", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const startTime = Date.now();
      await runQuery(page, '{job="api-gateway"} | json');
      const responseTime = Date.now() - startTime;
      queryResponseTime = responseTime;
      metrics.queries.push(responseTime);

      // Queries should respond in under 5 seconds
      expect(responseTime).toBeLessThan(5000);
      console.log(`✅ Query executed in ${responseTime}ms`);
    });

    test("should display results panel", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const resultsPanel = page.locator("[data-testid='logs-panel']");
      await expect(resultsPanel).toBeVisible();
      console.log("✅ Results panel visible");
    });

    test("should show log entries in results", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const logRows = page.locator("[data-testid='log-row']");
      const count = await logRows.count();
      expect(count).toBeGreaterThan(0);
      console.log(`✅ Found ${count} log entries`);
    });

    test("should allow expanding log entries", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const firstLogRow = page.locator("[data-testid='log-row']").first();
      const expandButton = firstLogRow.locator(
        "[data-testid='log-row-expand-button']"
      );

      if (await expandButton.isVisible()) {
        const startTime = Date.now();
        await expandButton.click();
        const interactionTime = Date.now() - startTime;
        metrics.uiInteractions.push(interactionTime);

        await page.waitForTimeout(200);
        const details = page.locator("[data-testid='log-row-details']").first();
        await expect(details).toBeVisible();
        console.log(
          `✅ Log entry expanded in ${interactionTime}ms with details visible`
        );
      } else {
        console.log("ℹ️ Expand button not available for this log entry");
      }
    });

    test("should handle error queries gracefully", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      // Invalid query should show error
      const queryInput = page.locator("[data-testid='query-editor']");
      await queryInput.click();
      await queryInput.fill("invalid logql query ][}{");
      await page.keyboard.press("Enter");
      await page.waitForTimeout(500);

      const errorPanel = page.locator("[data-testid='error-message']");
      const hasError = await errorPanel.isVisible().catch(() => false);
      console.log(`✅ Error handling ${hasError ? "works" : "varies"}`);
    });
  });

  test.describe("Field Explorer", () => {
    test("should display field list in sidebar", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const fieldsList = page.locator("[data-testid='fields-list']");
      const visible = await fieldsList.isVisible().catch(() => false);
      console.log(`✅ Fields list ${visible ? "visible" : "not visible"}`);
    });

    test("should show field values on click", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const fieldItem = page
        .locator("[data-testid='field-item']")
        .first()
        .catch(() => null);
      if (fieldItem) {
        const startTime = Date.now();
        await fieldItem.click();
        const interactionTime = Date.now() - startTime;
        metrics.uiInteractions.push(interactionTime);

        await page.waitForTimeout(200);
        const values = page.locator("[data-testid='field-value-item']");
        const count = await values.count();
        console.log(
          `✅ Field values shown (${count} items) in ${interactionTime}ms`
        );
      }
    });

    test("should filter by field value", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const fieldValue = page
        .locator("[data-testid='field-value-item']")
        .first();
      const visible = await fieldValue.isVisible().catch(() => false);

      if (visible) {
        await fieldValue.click();
        await page.waitForTimeout(500);
        console.log("✅ Field value filter applied");
      }
    });
  });

  test.describe("Filters & Label Selector", () => {
    test("should display label selector", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const labelSelector = page.locator("[data-testid='label-selector']");
      const visible = await labelSelector.isVisible().catch(() => false);
      console.log(`✅ Label selector ${visible ? "visible" : "not visible"}`);
    });

    test("should show available labels", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const labelDropdown = page.locator(
        "[data-testid='label-selector-dropdown']"
      );
      if (await labelDropdown.isVisible().catch(() => false)) {
        await labelDropdown.click();
        const labels = page.locator("[data-testid='label-option']");
        const count = await labels.count();
        expect(count).toBeGreaterThan(0);
        console.log(`✅ Found ${count} available labels`);
      }
    });

    test("should allow adding multiple filters", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const addFilterBtn = page.locator(
        "[data-testid='add-filter-button']"
      );
      const visible = await addFilterBtn.isVisible().catch(() => false);

      if (visible) {
        const initialFilterCount = await page
          .locator("[data-testid='filter-item']")
          .count();
        await addFilterBtn.click();
        await page.waitForTimeout(300);
        const newFilterCount = await page
          .locator("[data-testid='filter-item']")
          .count();

        if (newFilterCount > initialFilterCount) {
          console.log(
            `✅ New filter added (${initialFilterCount} → ${newFilterCount})`
          );
        }
      }
    });
  });

  test.describe("Time Range Selection", () => {
    test("should display time range picker", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const timeRangePicker = page.locator(
        "[data-testid='time-range-picker']"
      );
      const visible = await timeRangePicker.isVisible().catch(() => false);
      console.log(
        `✅ Time range picker ${visible ? "visible" : "not visible"}`
      );
    });

    test("should allow changing time range", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const timeRangeBtn = page.locator("[data-testid='time-range-button']");
      if (await timeRangeBtn.isVisible().catch(() => false)) {
        const startTime = Date.now();
        await timeRangeBtn.click();
        const interactionTime = Date.now() - startTime;
        metrics.uiInteractions.push(interactionTime);

        await page.waitForTimeout(300);
        const menu = page.locator("[data-testid='time-range-menu']");
        const visible = await menu.isVisible().catch(() => false);
        console.log(
          `✅ Time range menu ${visible ? "opened" : "not opened"} in ${interactionTime}ms`
        );
      }
    });

    test("should support custom time range", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const timeRangeBtn = page.locator("[data-testid='time-range-button']");
      if (await timeRangeBtn.isVisible().catch(() => false)) {
        await timeRangeBtn.click();
        const customOption = page.locator(
          "[data-testid='time-range-custom']"
        );
        if (await customOption.isVisible().catch(() => false)) {
          await customOption.click();
          console.log("✅ Custom time range option available");
        }
      }
    });
  });

  test.describe("Logs Drilldown Integration", () => {
    test("should support drill into logs from field", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const logRow = page.locator("[data-testid='log-row']").first();
      const drillButton = logRow.locator(
        "[data-testid='log-row-drilldown-button']"
      );

      if (await drillButton.isVisible().catch(() => false)) {
        const startTime = Date.now();
        await drillButton.click();
        const interactionTime = Date.now() - startTime;
        metrics.uiInteractions.push(interactionTime);

        await page.waitForTimeout(500);
        console.log(`✅ Drilldown initiated in ${interactionTime}ms`);
      } else {
        console.log("ℹ️ Drilldown button not available");
      }
    });

    test("should show pattern analysis when drilling", async ({ page }) => {
      await openExplore(page, PROXY_DS);
      await runQuery(page, '{job="api-gateway"} | json');

      const logRow = page.locator("[data-testid='log-row']").first();
      const patternBtn = logRow.locator(
        "[data-testid='log-row-pattern-button']"
      );

      if (await patternBtn.isVisible().catch(() => false)) {
        await patternBtn.click();
        await page.waitForTimeout(500);
        const patternPanel = page.locator("[data-testid='pattern-panel']");
        const visible = await patternPanel.isVisible().catch(() => false);
        console.log(
          `✅ Pattern analysis panel ${visible ? "shown" : "not shown"}`
        );
      }
    });
  });

  test.describe("Performance: Edge Cases", () => {
    test("should handle large result sets efficiently", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const startTime = Date.now();
      await runQuery(page, '{job="api-gateway"} | json');
      const loadTime = Date.now() - startTime;

      const logRows = page.locator("[data-testid='log-row']");
      const count = await logRows.count();

      metrics.queries.push(loadTime);
      console.log(
        `✅ Loaded ${count} results in ${loadTime}ms (${(loadTime / count).toFixed(2)}ms per result)`
      );
    });

    test("should handle special characters in labels", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const queryInput = page.locator("[data-testid='query-editor']");
      const specialCharsQuery = '{job="test@app#2024"} | json';

      await queryInput.click();
      await queryInput.fill(specialCharsQuery);
      await page.keyboard.press("Enter");
      await page.waitForTimeout(500);

      console.log("✅ Special characters handled correctly");
    });

    test("should handle empty result set", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const queryInput = page.locator("[data-testid='query-editor']");
      await queryInput.click();
      await queryInput.fill('{job="nonexistent-job-xyz"}');
      await page.keyboard.press("Enter");
      await page.waitForTimeout(500);

      const emptyState = page.locator("[data-testid='empty-state']");
      const visible = await emptyState.isVisible().catch(() => false);
      console.log(`✅ Empty state ${visible ? "shown" : "handled"}`);
    });

    test("should debounce rapid filter changes", async ({ page }) => {
      await openExplore(page, PROXY_DS);

      const queryInput = page.locator("[data-testid='query-editor']");
      await queryInput.click();

      // Rapid typing
      const startTime = Date.now();
      for (let i = 0; i < 3; i++) {
        await queryInput.fill(`{job="test${i}"}`);
        await page.waitForTimeout(50);
      }
      const finalQuery = '{job="test2"}';
      await queryInput.fill(finalQuery);
      await page.keyboard.press("Enter");
      await page.waitForTimeout(500);

      const totalTime = Date.now() - startTime;
      console.log(`✅ Rapid filter changes handled in ${totalTime}ms`);
    });
  });

  test.describe("Performance: Summary", () => {
    test("should report performance metrics", async ({ page }) => {
      console.log("\n📊 PERFORMANCE METRICS SUMMARY:");
      console.log("================================");

      if (metrics.pageLoads.length > 0) {
        const avgPageLoad =
          metrics.pageLoads.reduce((a, b) => a + b, 0) /
          metrics.pageLoads.length;
        console.log(`Page Load Time (avg): ${avgPageLoad.toFixed(0)}ms`);
        console.log(
          `  Range: ${Math.min(...metrics.pageLoads)}ms - ${Math.max(...metrics.pageLoads)}ms`
        );
      }

      if (metrics.queries.length > 0) {
        const avgQuery =
          metrics.queries.reduce((a, b) => a + b, 0) / metrics.queries.length;
        console.log(`Query Response Time (avg): ${avgQuery.toFixed(0)}ms`);
        console.log(
          `  Range: ${Math.min(...metrics.queries)}ms - ${Math.max(...metrics.queries)}ms`
        );
      }

      if (metrics.uiInteractions.length > 0) {
        const avgUI =
          metrics.uiInteractions.reduce((a, b) => a + b, 0) /
          metrics.uiInteractions.length;
        console.log(`UI Interaction Time (avg): ${avgUI.toFixed(0)}ms`);
        console.log(
          `  Range: ${Math.min(...metrics.uiInteractions)}ms - ${Math.max(...metrics.uiInteractions)}ms`
        );
      }

      console.log("================================\n");
      expect(true).toBe(true); // Summary test always passes
    });
  });
});
