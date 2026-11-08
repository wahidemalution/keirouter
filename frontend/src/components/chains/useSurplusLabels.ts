import { useEffect, useMemo, useState } from "react";
import { api } from "../../lib/api";
import type { SurplusLabels } from "./chainUtils";

const MODEL_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

// One request per model per page load. A fallback answer (the book could not
// be read) or an error is not kept, so the next call asks again.
const loaded = new Map<string, Promise<SurplusLabels | null>>();

// loadSurplusLabels resolves the Surplus provider ids and names for a model,
// or null when the model name is not one Surplus could list or the request
// failed.
export function loadSurplusLabels(model: string): Promise<SurplusLabels | null> {
  const name = model.trim();
  if (!MODEL_NAME.test(name)) return Promise.resolve(null);
  const hit = loaded.get(name);
  if (hit) return hit;
  const req = api.surplusProviders(name).then(
    (res) => {
      if (!res.complete) loaded.delete(name);
      return Object.fromEntries(res.providers.map((p) => [p.id, p.name]));
    },
    () => {
      loaded.delete(name);
      return null;
    },
  );
  loaded.set(name, req);
  return req;
}

// useSurplusLabels returns the labels loaded so far for each model, keyed by
// trimmed model name. A model missing from the result is still loading or
// could not be read.
export function useSurplusLabels(models: string[]): Record<string, SurplusLabels> {
  const key = useMemo(() => [...new Set(models.map((m) => m.trim()).filter((m) => MODEL_NAME.test(m)))].sort().join("\n"), [models]);
  const [byModel, setByModel] = useState<Record<string, SurplusLabels>>({});
  useEffect(() => {
    let live = true;
    for (const model of key ? key.split("\n") : []) {
      void loadSurplusLabels(model).then((labels) => {
        if (live && labels) setByModel((cur) => (cur[model] === labels ? cur : { ...cur, [model]: labels }));
      });
    }
    return () => { live = false; };
  }, [key]);
  return byModel;
}
