import { test } from "node:test";
import assert from "node:assert/strict";
import type { PortalModelProvider } from "./api.ts";
import { discountText, latencyText, offerLevels } from "./providerOffers.ts";

const base: PortalModelProvider = {
  id: "p", name: "P", enabled: true, locked: false, available: true, input_per_m: 0.1, output_per_m: 1,
};

test("offerLevels merges equal prices, cheapest first", () => {
  const levels = offerLevels({
    ...base,
    offers: [
      { input_per_m: 0.1, output_per_m: 1, discount_pct: 50 },
      { input_per_m: 0.1, output_per_m: 1, discount_pct: 50 },
      { input_per_m: 0.2, output_per_m: 2, discount_pct: 0 },
      { input_per_m: 0.3, output_per_m: 3, discount_pct: 0 },
    ],
  });
  assert.deepEqual(levels.map((l) => [l.input, l.count, l.discountPct]), [[0.1, 2, 50], [0.2, 1, 0], [0.3, 1, 0]]);
});

test("offerLevels is empty without offers", () => {
  assert.deepEqual(offerLevels(base), []);
});

test("discountText", () => {
  assert.equal(discountText(31), "31% off official");
  assert.equal(discountText(0), "No discount vs official");
  assert.equal(discountText(null), null);
});

test("latencyText", () => {
  assert.equal(latencyText(850), "850 ms");
  assert.equal(latencyText(2254), "2.3 s");
  assert.equal(latencyText(16545), "17 s");
  assert.equal(latencyText(null), null);
  assert.equal(latencyText(0), null);
});
