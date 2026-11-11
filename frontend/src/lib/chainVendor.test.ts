import { test } from "node:test";
import assert from "node:assert/strict";
import { chainVendor } from "./chainVendor.ts";

const none = new Map<string, string>();
const chain = (name: string, models: string[] = [], display_provider = "") => ({ name, display_provider, steps: models.map((model) => ({ model })) });

test("reads the vendor from the chain name", () => {
  assert.deepEqual(chainVendor(chain("deepseek-v4.1-flash"), none), { id: "deepseek", label: "DeepSeek" });
  assert.deepEqual(chainVendor(chain("glm-5.3"), none), { id: "zai", label: "Z.ai" });
  assert.deepEqual(chainVendor(chain("kimi-k3"), none), { id: "moonshot", label: "Moonshot AI" });
  assert.equal(chainVendor(chain("claude-sonnet-5"), none).id, "anthropic");
  assert.equal(chainVendor(chain("o4-mini"), none).id, "openai");
});

test("falls back to the step models when the name is free-form", () => {
  assert.equal(chainVendor(chain("fast-coder", ["cbcn/qwen3-coder"]), none).id, "alibaba");
  assert.equal(chainVendor(chain("fast-coder", ["mystery-1"]), none).id, "other");
});

test("a display provider wins and uses its category label", () => {
  const labels = new Map([["anthropic", "Anthropic PBC"]]);
  assert.deepEqual(chainVendor(chain("deepseek-v4", [], "Anthropic"), labels), { id: "anthropic", label: "Anthropic PBC" });
  assert.deepEqual(chainVendor(chain("x", [], "zai"), none), { id: "zai", label: "Z.ai" });
});
