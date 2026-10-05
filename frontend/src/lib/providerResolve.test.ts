import { test } from "node:test";
import assert from "node:assert/strict";
import { resolveProvider, familySlug } from "./providerResolve.ts";

const model = (over: Partial<Record<string, unknown>>) => ({
  name: "x", model_id: "x", provider: "", provider_id: "", input_per_m: 0,
  output_per_m: 0, cached_per_m: 0, cache_write_per_m: 0,
  capabilities: {}, usage: { users: 0, requests: 0, tokens: 0 },
  ...over,
}) as any;

test("resolveProvider uses provider_id verbatim", () => {
  const r = resolveProvider(model({ model_id: "claude-sonnet-4", provider: "DeepSeek", provider_id: "deepseek" }));
  assert.deepEqual(r, { id: "deepseek", label: "DeepSeek" });
});

test("resolveProvider no longer remaps by model name", () => {
  const r = resolveProvider(model({ model_id: "gemini-2.5-flash", provider: "combo", provider_id: "combo" }));
  assert.deepEqual(r, { id: "combo", label: "combo" });
});

test("resolveProvider falls back to provider_id for the label", () => {
  const r = resolveProvider(model({ provider: "", provider_id: "mistral" }));
  assert.deepEqual(r, { id: "mistral", label: "mistral" });
});

test("familySlug is retained for logo fallback", () => {
  assert.equal(familySlug("claude-sonnet-4"), "anthropic");
});
