import { test } from "node:test";
import assert from "node:assert/strict";
import {
  deletesIn, formatImagePriceMicro, imageAlt, imageErrorText, optionsForFamily, resolutionsFor, totalPriceMicro,
  unitPriceMicro,
} from "./imageOptions.ts";

test("optionsForFamily lists each family's own options", () => {
  assert.deepEqual(optionsForFamily("gpt-image").sizes, ["1024x1024", "1536x1024", "1024x1536", "auto"]);
  assert.equal(optionsForFamily("gpt-image").aspectRatios, undefined);
  assert.ok(optionsForFamily("grok-imagine").resolutions?.includes("2k"));
  assert.deepEqual(optionsForFamily("grok-imagine").aspectRatios, ["1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "21:9"]);
  assert.deepEqual(optionsForFamily("gemini-image").aspectRatios, ["1:1", "16:9", "9:16", "4:3", "3:4"]);
  assert.equal(optionsForFamily("gemini-image").resolutions, undefined);
  assert.deepEqual(optionsForFamily("unknown"), {});
});

test("resolutionsFor offers 2K only when the model has a 2K price", () => {
  assert.deepEqual(resolutionsFor({ family: "grok-imagine", price_2k_micro: 55000 }), ["1k", "2k"]);
  assert.deepEqual(resolutionsFor({ family: "grok-imagine" }), ["1k"]);
  assert.equal(resolutionsFor({ family: "gpt-image", price_2k_micro: 55000 }), undefined);
});

test("unitPriceMicro is the price the page shows and sends", () => {
  assert.equal(unitPriceMicro(8400, 45000, ""), 8400);
  assert.equal(unitPriceMicro(8400, 45000, "1k"), 8400);
  assert.equal(unitPriceMicro(8400, 45000, "2K"), 45000);
  // No 2K price: 2K cannot be priced, never the standard price.
  assert.equal(unitPriceMicro(8400, 0, "2k"), 0);
});

test("totalPriceMicro multiplies the per-image price by n", () => {
  assert.equal(totalPriceMicro(8400, 0, "", 2), 16800);
  assert.equal(totalPriceMicro(8400, 45000, "1k", 3), 25200);
  assert.equal(totalPriceMicro(8400, 45000, "2K", 1), 45000);
  assert.equal(totalPriceMicro(8400, 45000, "2k", 2), 90000);
});

test("formatImagePriceMicro writes dollars with a decimal comma", () => {
  assert.equal(formatImagePriceMicro(8400), "$0,0084");
  assert.equal(formatImagePriceMicro(16800), "$0,0168");
  assert.equal(formatImagePriceMicro(45000), "$0,045");
  assert.equal(formatImagePriceMicro(1_250_000), "$1,25");
});

test("deletesIn rounds the time left down to hours, then minutes", () => {
  const now = Date.parse("2026-11-26T00:00:00Z");
  assert.equal(deletesIn("2026-11-26T23:30:00Z", now), "dihapus dalam 23 jam");
  assert.equal(deletesIn("2026-11-26T00:45:00Z", now), "dihapus dalam 45 menit");
  assert.equal(deletesIn("2026-11-26T00:00:20Z", now), "dihapus dalam 1 menit");
  assert.equal(deletesIn("2026-11-25T23:00:00Z", now), "segera dihapus");
  assert.equal(deletesIn(null, now), "");
});

test("imageErrorText maps known codes and falls back otherwise", () => {
  assert.equal(imageErrorText("invalid_aspect_ratio", "x"), "Rasio aspek ini tidak didukung model yang dipilih.");
  assert.equal(imageErrorText("price_changed", "x"), "Harga berubah, silakan cek lagi lalu buat ulang.");
  assert.equal(imageErrorText("something_new", "Gagal"), "Gagal");
  assert.equal(imageErrorText(undefined, "Gagal"), "Gagal");
});

test("imageAlt truncates long prompts", () => {
  assert.equal(imageAlt("kucing oranye"), "kucing oranye");
  const long = "a".repeat(200);
  const alt = imageAlt(long);
  assert.ok(alt.length <= 121);
  assert.ok(alt.endsWith("…"));
});
