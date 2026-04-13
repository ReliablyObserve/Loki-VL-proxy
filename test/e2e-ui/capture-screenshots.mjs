import { chromium } from "@playwright/test";
import fs from "node:fs/promises";
import path from "node:path";

const baseUrl = process.env.GRAFANA_URL || "http://127.0.0.1:3002";
const explicitExecutablePath = process.env.PLAYWRIGHT_EXECUTABLE_PATH;
const outDir =
  process.env.SCREENSHOT_OUT_DIR ||
  path.resolve(process.cwd(), "../../docs/images/ui");
const screenshotFrom = process.env.SCREENSHOT_FROM || "now-5m";
const screenshotTo = process.env.SCREENSHOT_TO || "now";

const datasourceNames = {
  proxy: "Loki (via VL proxy)",
  multi: "Loki (via VL proxy multi-tenant)",
};

const drilldownPath = "/a/grafana-lokiexplore-app/explore";

function buildExploreUrl(datasourceUid, expr = "") {
  const paneState = {
    A: {
      datasource: datasourceUid,
      queries: [
        {
          refId: "A",
          expr,
          queryType: "range",
          datasource: {
            type: "loki",
            uid: datasourceUid,
          },
          editorMode: expr ? "code" : "builder",
          direction: "backward",
        },
      ],
      range: {
        from: screenshotFrom,
        to: screenshotTo,
      },
      compact: false,
    },
  };
  const params = new URLSearchParams({
    schemaVersion: "1",
    panes: JSON.stringify(paneState),
    orgId: "1",
  });
  return `/explore?${params.toString()}`;
}

function buildDrilldownUrl(datasourceUid) {
  const params = new URLSearchParams({
    patterns: "[]",
    from: screenshotFrom,
    to: screenshotTo,
    timezone: "browser",
    "var-lineFormat": "",
    "var-ds": datasourceUid,
    "var-filters": "",
    "var-fields": "",
    "var-levels": "",
    "var-metadata": "",
    "var-jsonFields": "",
    "var-all-fields": "",
    "var-patterns": "",
    "var-lineFilterV2": "",
    "var-lineFilters": "",
    "var-primary_label": "service_name|=~|.+",
  });
  return `${drilldownPath}?${params.toString()}`;
}

async function resolveDatasourceUid(request, datasourceName) {
  const response = await request.get(
    `${baseUrl}/api/datasources/name/${encodeURIComponent(datasourceName)}`
  );
  if (!response.ok()) {
    throw new Error(
      `failed to resolve datasource "${datasourceName}" (status=${response.status()})`
    );
  }
  const body = await response.json();
  if (!body.uid) {
    throw new Error(`datasource "${datasourceName}" has no uid`);
  }
  return body.uid;
}

async function waitForGrafanaReady(page) {
  await page.waitForLoadState("networkidle");
  await page.waitForTimeout(1000);
}

async function waitForLogsTable(page) {
  await page
    .locator('[data-testid="logRows"], [class*="logs-row"], [class*="LogsTable"]')
    .first()
    .waitFor({ state: "visible", timeout: 30000 });
}

async function runExploreQuery(page) {
  const runButton = page.getByRole("button", { name: /run query/i }).first();
  if (await runButton.isVisible().catch(() => false)) {
    await runButton.click();
  } else {
    const overflow = page.getByRole("button", { name: /show more items/i });
    await overflow.click();
    await page.getByRole("menuitem", { name: /run query/i }).click();
  }
}

async function main() {
  await fs.mkdir(outDir, { recursive: true });

  const browser = await chromium.launch({
    headless: true,
    ...(explicitExecutablePath ? { executablePath: explicitExecutablePath } : {}),
  });
  const context = await browser.newContext({
    viewport: { width: 1600, height: 1200 },
  });
  const page = await context.newPage();

  const proxyUid = await resolveDatasourceUid(page.request, datasourceNames.proxy);
  const multiUid = await resolveDatasourceUid(page.request, datasourceNames.multi);

  await page.goto(`${baseUrl}${buildExploreUrl(proxyUid, '{app="api-gateway"}')}`);
  await waitForGrafanaReady(page);
  await runExploreQuery(page);
  await waitForGrafanaReady(page);
  await waitForLogsTable(page);
  await page.screenshot({
    path: path.join(outDir, "explore-main.png"),
    fullPage: true,
  });

  const firstRow = page
    .locator('[data-testid="logRows"] tr, [class*="logs-row"]')
    .first();
  if (await firstRow.isVisible().catch(() => false)) {
    await firstRow.click();
    await waitForGrafanaReady(page);
  }
  await page.screenshot({
    path: path.join(outDir, "explore-details.png"),
    fullPage: true,
  });

  await page.goto(`${baseUrl}${buildDrilldownUrl(proxyUid)}`);
  await waitForGrafanaReady(page);
  await page
    .getByRole("combobox", { name: "Filter by labels" })
    .waitFor({ state: "visible", timeout: 30000 });
  await page.screenshot({
    path: path.join(outDir, "drilldown-main.png"),
    fullPage: true,
  });

  await page.goto(
    `${baseUrl}${buildExploreUrl(
      multiUid,
      '{app="api-gateway", __tenant_id__!~"f.*"}'
    )}`
  );
  await waitForGrafanaReady(page);
  await runExploreQuery(page);
  await waitForGrafanaReady(page);
  await waitForLogsTable(page);
  await page.screenshot({
    path: path.join(outDir, "explore-tail-multitenant.png"),
    fullPage: true,
  });

  await browser.close();
  // eslint-disable-next-line no-console
  console.log(`Saved screenshots to ${outDir}`);
}

main().catch((err) => {
  // eslint-disable-next-line no-console
  console.error(err);
  process.exit(1);
});
