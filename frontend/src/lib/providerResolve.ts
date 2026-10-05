// Pure provider-resolution helpers shared by the model catalog and cards.
// Kept JSX-free and in .ts so node --test can import them directly.

import type { PublicModel } from "./publicApi";

// Family logo detection for models whose provider has no brand PNG of its own
// (e.g. relay/custom providers). Each family slug maps to an existing PNG in
// frontend/public/providers/.
const FAMILY_RULES: [RegExp, string][] = [
  [/claude|opus|sonnet|haiku/, "anthropic"],
  [/gpt|dall-e|whisper|text-embedding|(^|[^a-z])o[134](-|$)/, "openai"],
  [/gemini|gemma|palm|learnlm/, "gemini"],
  [/deepseek/, "deepseek"],
  [/kimi|moonshot/, "kimi"],
  [/qwen/, "qwen"],
  [/minimax/, "minimax"],
  [/glm/, "glm"],
  [/grok/, "xai"],
  [/mistral|codestral|pixtral|mixtral/, "mistral"],
  [/nemotron/, "nvidia"],
  [/sonar/, "perplexity"],
  [/qoder/, "qoder"],
  [/mimo/, "xiaomi-mimo"],
  [/command-[ra]/, "cohere"],
];

export const familySlug = (modelId: string): string | null => {
  const m = modelId.toLowerCase();
  for (const [re, slug] of FAMILY_RULES) if (re.test(m)) return slug;
  return null;
};

// The provider shown on a card / used to group the filter is the chain's
// operator-chosen display provider, sent verbatim by the backend. Empty
// resolves to the backend's "combo" sentinel.
export function resolveProvider(model: PublicModel): { id: string; label: string } {
  return { id: model.provider_id, label: model.provider || model.provider_id };
}
