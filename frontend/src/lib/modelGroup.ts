// Model Settings groups models by the leading run of letters in their name,
// so deepseek-v4.1-flash and deepseek-r2 share one filter button.

const GROUP_LABELS: Record<string, string> = {
  deepseek: "DeepSeek",
  glm: "GLM",
  qwen: "Qwen",
  gpt: "GPT",
  kimi: "Kimi",
  minimax: "MiniMax",
  gemini: "Gemini",
  claude: "Claude",
};

export interface ModelGroup {
  id: string;
  label: string;
}

export function modelGroup(model: string): ModelGroup {
  const id = /^[a-z]+/.exec(model.trim().toLowerCase())?.[0] ?? "";
  // A single letter (o3, o4-mini) says nothing about the family.
  if (id.length < 2) return { id: "other", label: "Other" };
  return { id, label: GROUP_LABELS[id] ?? id[0].toUpperCase() + id.slice(1) };
}
