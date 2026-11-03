import { test } from "node:test";
import assert from "node:assert/strict";
import { modelGroup } from "./modelGroup.ts";

test("modelGroup uses the leading letters and the display-name map", () => {
  assert.deepEqual(modelGroup("deepseek-v4.1-flash"), { id: "deepseek", label: "DeepSeek" });
  assert.deepEqual(modelGroup("qwen3-coder"), { id: "qwen", label: "Qwen" });
  assert.deepEqual(modelGroup("GLM-5.3"), { id: "glm", label: "GLM" });
});

test("modelGroup sends runs shorter than two letters to Other", () => {
  assert.deepEqual(modelGroup("o3"), { id: "other", label: "Other" });
  assert.deepEqual(modelGroup("3-turbo"), { id: "other", label: "Other" });
});

test("modelGroup capitalises names missing from the map", () => {
  assert.deepEqual(modelGroup("mistral-large"), { id: "mistral", label: "Mistral" });
});
