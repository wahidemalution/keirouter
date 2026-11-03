import { test } from "node:test";
import assert from "node:assert/strict";
import { fuzzyFilter, fuzzyScore } from "./fuzzyMatch.ts";

const row = (provider: string, model: string) => `${model} ${provider}`;

test("words match in any order, ignoring case and punctuation", () => {
  assert.ok(fuzzyScore("kimi dashscope", row("Surplus DashScope CN", "kimi-k3")) > 0);
  assert.ok(fuzzyScore("DASHSCOPE cn KIMI-K3", row("Surplus DashScope CN", "kimi-k3")) > 0);
  assert.ok(fuzzyScore("kimik3", row("Surplus DashScope CN", "kimi-k3")) > 0);
});

test("small typos still match", () => {
  assert.ok(fuzzyScore("dashcope", row("Surplus DashScope CN", "kimi-k3")) > 0);
  assert.ok(fuzzyScore("opnrouter kimi", row("Surplus OpenRouter", "kimi-k3")) > 0);
  assert.ok(fuzzyScore("surplsu", row("Surplus OpenRouter", "kimi-k3")) > 0);
});

test("every word must match", () => {
  assert.equal(fuzzyScore("kimi zai", row("Surplus OpenRouter", "kimi-k3")), 0);
  assert.equal(fuzzyScore("glm", row("Surplus OpenRouter", "kimi-k3")), 0);
});

test("fuzzyFilter ranks exact matches before typo matches", () => {
  const items = [row("Surplus DashScpe CN", "kimi-k3"), row("Surplus DashScope CN", "kimi-k3"), row("Surplus Z.ai", "glm-5.3")];
  assert.deepEqual(fuzzyFilter(items, "dashscope", (s) => s), [items[1], items[0]]);
  assert.equal(fuzzyFilter(items, "", (s) => s).length, 3);
});
