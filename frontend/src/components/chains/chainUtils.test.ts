import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveCacheRates, makeDraftStep, staticStepPreview, stepKey, stepPayload, toDraftSteps } from "./chainUtils.ts";

test("deriveCacheRates derives write 1.25x and read 0.1x from input", () => {
  assert.deepEqual(deriveCacheRates(2, { cacheWritePerM: 0, cacheReadPerM: 0 }), {
    cacheWritePerM: 2.5,
    cacheReadPerM: 0.2,
  });
});

test("deriveCacheRates preserves a manual override that differs from the old derivation", () => {
  assert.deepEqual(
    deriveCacheRates(4, { cacheWritePerM: 9, cacheReadPerM: 0.2, prevInput: 2 }),
    { cacheWritePerM: 9, cacheReadPerM: 0.4 },
  );
});

test("deriveCacheRates re-derives a field that still equals the old formula", () => {
  assert.deepEqual(
    deriveCacheRates(4, { cacheWritePerM: 2.5, cacheReadPerM: 0.2, prevInput: 2 }),
    { cacheWritePerM: 5, cacheReadPerM: 0.4 },
  );
});

test("makeDraftStep drops any pricing carried on the step payload", () => {
  const step = makeDraftStep({
    provider: "openai",
    model: "gpt-4o",
    input_per_m: 9,
    output_per_m: 9,
    cache_write_per_m: 9,
    cache_read_per_m: 9,
  } as unknown as Parameters<typeof makeDraftStep>[0]);
  assert.equal(step.inputPerM, 0);
  assert.equal(step.outputPerM, 0);
  assert.equal(step.cacheWritePerM, 0);
  assert.equal(step.cacheReadPerM, 0);
});

test("toDraftSteps maps steps without reading step pricing", () => {
  const steps = toDraftSteps({
    id: "c1",
    name: "priced",
    strategy: "priority",
    input_per_m: 2.5,
    output_per_m: 10,
    cache_write_per_m: 3.125,
    cache_read_per_m: 0.25,
    steps: [{ provider: "openai", model: "gpt-4o", position: 0, input_per_m: 9, output_per_m: 9, cache_write_per_m: 9, cache_read_per_m: 9 }],
  } as unknown as Parameters<typeof toDraftSteps>[0]);
  assert.equal(steps.length, 1);
  assert.equal(steps[0].provider, "openai");
  assert.equal(steps[0].inputPerM, 0);
  assert.equal(steps[0].outputPerM, 0);
  assert.equal(steps[0].cacheWritePerM, 0);
  assert.equal(steps[0].cacheReadPerM, 0);
});

const close = (a: number, b: number) => assert.ok(Math.abs(a - b) < 1e-9, `${a} != ${b}`);

const staticDraft = (over: Record<string, string> = {}) => ({
  ...makeDraftStep({ provider: "openai", model: "gpt-4o" }),
  priceSource: "static" as const,
  staticInputPerM: "0.5",
  staticOutputPerM: "2",
  ...over,
});

test("staticStepPreview inherits the chain markup when the step markup is empty", () => {
  const p = staticStepPreview(staticDraft(), 30);
  assert.equal(p.ok, true);
  if (!p.ok) return;
  assert.equal(p.markup, 30);
  close(p.inputPerM, 0.65);
  close(p.outputPerM, 2.6);
  close(p.cacheReadPerM, 0.065);
  close(p.cacheWritePerM, 0.8125);
});

test("staticStepPreview uses the step markup and cache override", () => {
  const p = staticStepPreview(staticDraft({ stepMarkup: "10", staticCacheReadPerM: "0.2" }), 30);
  assert.equal(p.ok, true);
  if (!p.ok) return;
  assert.equal(p.markup, 10);
  close(p.inputPerM, 0.55);
  close(p.cacheReadPerM, 0.22);
  close(p.cacheWritePerM, 0.6875);
});

test("staticStepPreview reports a missing output price", () => {
  const p = staticStepPreview(staticDraft({ staticOutputPerM: "" }), 30);
  assert.deepEqual(p, { ok: false, error: "Enter an input and output price above 0." });
});

test("staticStepPreview rejects negative prices and out-of-range markup", () => {
  assert.deepEqual(staticStepPreview(staticDraft({ staticCacheReadPerM: "-1" }), 0), { ok: false, error: "Prices must be 0 or more." });
  assert.deepEqual(staticStepPreview(staticDraft({ stepMarkup: "2000" }), 0), { ok: false, error: "Markup must be between 0 and 1000." });
});

test("stepPayload for a market step drops leftover static fields", () => {
  const draft = { ...staticDraft(), priceSource: "market" as const, marketSlug: "surplus-fast:x" };
  const out = stepPayload(draft);
  assert.equal(out.market_slug, "surplus-fast:x");
  assert.equal("static_input_per_m" in out, false);
  assert.equal("markup_percent" in out, false);
});

test("stepPayload for a static step clears a leftover slug", () => {
  const out = stepPayload({ ...staticDraft({ stepMarkup: "10" }), marketSlug: "y" });
  assert.deepEqual(out, {
    provider: "openai", model: "gpt-4o", market_slug: "",
    static_input_per_m: 0.5, static_output_per_m: 2,
    static_cache_read_per_m: null, static_cache_write_per_m: null, markup_percent: 10,
  });
});

test("makeDraftStep reads a static step from the API", () => {
  const d = makeDraftStep({ provider: "openai", model: "gpt-4o", static_input_per_m: 0.5, static_output_per_m: 2, markup_percent: null, static_cache_write_per_m: 1.5 });
  assert.equal(d.priceSource, "static");
  assert.equal(d.staticInputPerM, "0.5");
  assert.equal(d.staticCacheWritePerM, "1.5");
  assert.equal(d.staticCacheReadPerM, "");
  assert.equal(d.stepMarkup, "");
  assert.equal(makeDraftStep({ provider: "a", model: "b", market_slug: "s" }).priceSource, "market");
});

test("stepKey separates a static step from a slug step on the same model", () => {
  const slug = { ...makeDraftStep({ provider: "openai", model: "gpt-4o", market_slug: "surplus-fast:gpt-4o" }) };
  const plain = makeDraftStep({ provider: "openai", model: "gpt-4o" });
  const stat = staticDraft();
  assert.notEqual(stepKey(slug), stepKey(stat));
  assert.notEqual(stepKey(plain), stepKey(stat));
  assert.equal(stepKey(stat), stepKey(staticDraft()));
  assert.notEqual(stepKey(stat), stepKey(staticDraft({ staticOutputPerM: "3" })));
});
