// otel.spec.ts — Claude Code's OpenTelemetry export as the Usage view's second source: the
// recorded export posts to the hub's OTLP intake the way Claude Code's exporter sends it
// (protobuf, the owner's credential, no action header), and Usage › Source › OpenTelemetry
// shows its model with its own token and cost totals, while Transcripts keeps the seeded report.

import { expect, test } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { ConsoleErrors, USAGE, signIn, theSetup } from "./helpers.ts";
import { repoRoot } from "./paths.ts";

const FIXTURE = path.join(
  repoRoot,
  "internal",
  "radar",
  "ingest",
  "testdata",
  "otlp",
  "claude-code-metrics.pb",
);

test("the usage view's OpenTelemetry source shows Claude Code's export apart from the transcripts", async ({
  page,
}) => {
  const errors = new ConsoleErrors(page);
  const exported = await fetch(`${theSetup().rootUrl}/api/radar/otlp/v1/metrics`, {
    method: "POST",
    headers: { "content-type": "application/x-protobuf", "x-huddle-token": theSetup().token },
    body: fs.readFileSync(FIXTURE),
  });
  expect(exported.status).toBe(200);

  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/usage`);
  const tiles = page.locator("[data-usage-tiles]");
  await expect(tiles).toContainText(USAGE.processed.toLocaleString("en-US"));

  const source = page.getByRole("group", { name: "Source" });
  await source.getByRole("button", { name: "OpenTelemetry" }).click();
  const table = page.locator("[data-key='usage-otel']");
  await expect(table).toBeVisible();
  const sonnet = table.locator("tbody tr", { hasText: "Sonnet 4.5" });
  await expect(sonnet).toContainText("1.2k");
  await expect(sonnet).toContainText("56k");
  await expect(page.getByText(/never added to the other views/)).toBeVisible();
  await expect(tiles).toHaveCount(0);

  await source.getByRole("button", { name: "Transcripts" }).click();
  await expect(tiles).toContainText(USAGE.processed.toLocaleString("en-US"));
  expect(errors.errors()).toEqual([]);
});
