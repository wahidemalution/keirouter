import { test } from "node:test";
import assert from "node:assert/strict";
import { agoLabel, detailState, fmtLatency, fmtPct, fmtTps } from "./modelDetail.ts";
import type { PublicModelDetail } from "./publicApi.ts";

const base: PublicModelDetail = {
  model: "deepseek-v4.1-flash",
  as_of: "2026-11-02T08:00:00Z",
  official_input_per_m: 0.045,
  official_output_per_m: 0.5,
  best: { input_per_m: 0.0002, output_per_m: 0.0015, discount_pct: 99 },
  median_discount_pct: 72,
  offer_count: 14,
  requests_24h: 1832,
  performance: {
    as_of: "2026-11-02T07:55:00Z",
    routes: [
      { name: "OpenRouter", p50_latency_ms: 1670, p95_latency_ms: 18420, tokens_per_sec: 134, success_pct: 99.5, share_pct: 61 },
    ],
  },
};

test("fmtPct shows a dash for null and keeps zero", () => {
  assert.equal(fmtPct(null), "–");
  assert.equal(fmtPct(0), "0%");
  assert.equal(fmtPct(72), "72%");
  assert.equal(fmtPct(99.5), "99,5%");
});

test("agoLabel counts seconds, then minutes, then hours", () => {
  const at = Date.parse("2026-11-02T08:00:00Z");
  assert.equal(agoLabel("2026-11-02T08:00:00Z", at + 12_000), "Diperbarui 12 detik lalu");
  assert.equal(agoLabel("2026-11-02T08:00:00Z", at + 59_000), "Diperbarui 59 detik lalu");
  assert.equal(agoLabel("2026-11-02T08:00:00Z", at + 3 * 60_000 + 20_000), "Diperbarui 3 menit lalu");
  assert.equal(agoLabel("2026-11-02T08:00:00Z", at + 2 * 3_600_000 + 60_000), "Diperbarui 2 jam lalu");
});

test("agoLabel clamps clock skew and rejects missing or bad input", () => {
  const at = Date.parse("2026-11-02T08:00:00Z");
  assert.equal(agoLabel("2026-11-02T08:00:00Z", at - 5_000), "Diperbarui 0 detik lalu");
  assert.equal(agoLabel(null, at), null);
  assert.equal(agoLabel("not a date", at), null);
});

test("fmtLatency shows seconds with two decimals", () => {
  assert.equal(fmtLatency(1670), "1,67 dtk");
  assert.equal(fmtLatency(18420), "18,42 dtk");
  assert.equal(fmtLatency(null), "–");
});

test("fmtTps rounds to whole tokens per second", () => {
  assert.equal(fmtTps(133.6), "134 tok/dtk");
  assert.equal(fmtTps(null), "–");
});

test("detailState: unavailable when both sources are null", () => {
  assert.equal(detailState({ ...base, best: null, median_discount_pct: null, offer_count: null, performance: null }), "unavailable");
});

test("detailState: no-offers when the book has no eligible offer", () => {
  assert.equal(detailState({ ...base, best: null, median_discount_pct: null, offer_count: 0, performance: { as_of: "2026-11-02T07:55:00Z", routes: [] } }), "no-offers");
});

test("detailState: ok with data, and when only performance is present", () => {
  assert.equal(detailState(base), "ok");
  assert.equal(detailState({ ...base, best: null, median_discount_pct: null, offer_count: null }), "ok");
});

test("detailState: an empty book is no-offers even without performance", () => {
  assert.equal(detailState({ ...base, best: null, median_discount_pct: null, offer_count: 0, performance: null }), "no-offers");
});
