// chainVendor names the company behind a chain's model (DeepSeek, Anthropic,
// OpenAI) for the Chains page filter. A display provider set on the chain
// wins; otherwise the vendor is read from the model family in the name.

export interface ChainVendor {
  id: string;
  label: string;
}

// Ids follow the usual provider-category ids so a chain with a display
// provider and one without land in the same filter option.
const FAMILY_VENDORS: Record<string, ChainVendor> = {
  deepseek: { id: "deepseek", label: "DeepSeek" },
  claude: { id: "anthropic", label: "Anthropic" },
  gpt: { id: "openai", label: "OpenAI" },
  chatgpt: { id: "openai", label: "OpenAI" },
  codex: { id: "openai", label: "OpenAI" },
  gemini: { id: "google", label: "Google" },
  gemma: { id: "google", label: "Google" },
  glm: { id: "zai", label: "Z.ai" },
  kimi: { id: "moonshot", label: "Moonshot AI" },
  moonshot: { id: "moonshot", label: "Moonshot AI" },
  qwen: { id: "alibaba", label: "Alibaba (Qwen)" },
  qwq: { id: "alibaba", label: "Alibaba (Qwen)" },
  minimax: { id: "minimax", label: "MiniMax" },
  grok: { id: "xai", label: "xAI" },
  llama: { id: "meta", label: "Meta" },
  mistral: { id: "mistral", label: "Mistral" },
  mixtral: { id: "mistral", label: "Mistral" },
  codestral: { id: "mistral", label: "Mistral" },
  devstral: { id: "mistral", label: "Mistral" },
};

export const OTHER_VENDOR: ChainVendor = { id: "other", label: "Other" };

// knownVendor reads the vendor from a model name, or null when the family is
// not one we know. A routing prefix such as "cbcn/" is dropped first.
function knownVendor(model: string): ChainVendor | null {
  const name = model.trim().toLowerCase().split("/").pop() ?? "";
  // o1, o3, o4-mini are OpenAI's reasoning models.
  if (/^o\d/.test(name)) return FAMILY_VENDORS.gpt;
  return FAMILY_VENDORS[/^[a-z]+/.exec(name)?.[0] ?? ""] ?? null;
}

const LABEL_BY_ID = new Map(Object.values(FAMILY_VENDORS).map((vendor) => [vendor.id, vendor.label]));

export function chainVendor(
  chain: { name: string; display_provider?: string; steps: { model: string }[] },
  categoryLabels: Map<string, string>,
): ChainVendor {
  const display = chain.display_provider?.trim().toLowerCase();
  if (display) return { id: display, label: categoryLabels.get(display) ?? LABEL_BY_ID.get(display) ?? display };
  // A chain named freely (e.g. "fast-coder") still has a model in its steps.
  for (const model of [chain.name, ...chain.steps.map((step) => step.model)]) {
    const vendor = knownVendor(model);
    if (vendor) return vendor;
  }
  return OTHER_VENDOR;
}
