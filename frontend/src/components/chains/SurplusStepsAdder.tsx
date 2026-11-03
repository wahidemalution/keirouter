import { useId, useMemo, useState } from "react";
import { Layers } from "lucide-react";
import { cn } from "@/lib/utils";
import type { Provider } from "../../lib/api";
import { useModelCatalog } from "../ModelSelect";
import { ProviderLogo } from "../ProviderLogo";
import { Button, Input } from "../ui";
import { autoMarketSlug, providerIcon, surplusProviderOf } from "./chainUtils";

const focusRing = "focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500";

export interface NewStep {
  provider: string;
  model: string;
  marketSlug: string;
}

// SurplusStepsAdder adds one step per pinned Surplus provider for a model in
// one go: the operator types the model once, ticks providers, and each step
// gets its surplus-trusted@<provider>:<model> slug filled in.
export function SurplusStepsAdder({
  providers,
  existing,
  defaultModel,
  onAdd,
}: {
  providers: Provider[];
  /** Steps already in the chain, so a provider is not added twice. */
  existing: { provider: string; model: string }[];
  defaultModel: string;
  onAdd: (steps: NewStep[]) => void;
}) {
  const uid = useId();
  const catalog = useModelCatalog();
  const [open, setOpen] = useState(false);
  const [model, setModel] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const surplus = useMemo(
    () => providers.filter((p) => surplusProviderOf(p)).sort((a, b) => a.display_name.localeCompare(b.display_name)),
    [providers],
  );
  const trimmed = model.trim();
  // Model ids any Surplus provider lists, for the suggestion list.
  const knownModels = useMemo(() => {
    const ids = new Set(surplus.map((p) => p.id));
    return [...new Set(catalog.models.filter((m) => ids.has(m.providerId)).map((m) => m.id))].sort();
  }, [catalog.models, surplus]);
  const listed = (providerID: string) => catalog.models.some((m) => m.providerId === providerID && m.id === trimmed);
  const already = (providerID: string) => existing.some((s) => s.provider === providerID && s.model === trimmed);
  const choosable = surplus.filter((p) => !already(p.id));
  const chosen = choosable.filter((p) => picked.has(p.id));

  if (surplus.length === 0) return null;

  const start = () => {
    setModel(defaultModel);
    setPicked(new Set());
    setOpen(true);
  };
  const toggle = (id: string, on: boolean) =>
    setPicked((cur) => {
      const next = new Set(cur);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  const add = () => {
    onAdd(chosen.map((p) => ({ provider: p.id, model: trimmed, marketSlug: autoMarketSlug(p, trimmed) })));
    setOpen(false);
  };

  if (!open) {
    return (
      <button
        type="button"
        onClick={start}
        className={cn("inline-flex min-h-9 w-full items-center justify-center gap-1.5 rounded-lg border border-line bg-surface px-3 text-[13px] font-medium text-fg-muted transition-colors hover:bg-hover hover:text-fg [@media(pointer:coarse)]:min-h-11", focusRing)}
      >
        <Layers className="h-4 w-4 shrink-0" strokeWidth={1.75} aria-hidden="true" />
        Add Surplus providers
      </button>
    );
  }

  const allOn = choosable.length > 0 && chosen.length === choosable.length;
  return (
    <section aria-labelledby={`${uid}-title`} className="space-y-3 rounded-lg border border-line bg-subtle p-3">
      <div>
        <h3 id={`${uid}-title`} className="text-[13px] font-medium text-fg">Add Surplus providers</h3>
        <p className="mt-0.5 text-[12px] leading-5 text-fg-muted">
          One step per provider you tick, with its market slug filled in.
        </p>
      </div>
      <label className="block space-y-1.5">
        <span className="text-[12.5px] font-medium text-fg">Model ID</span>
        <Input
          value={model}
          onChange={(e) => setModel(e.target.value)}
          list={`${uid}-models`}
          placeholder="e.g. kimi-k3"
          className="font-mono"
          autoFocus
        />
        <datalist id={`${uid}-models`}>
          {knownModels.map((id) => <option key={id} value={id} />)}
        </datalist>
      </label>
      <fieldset disabled={!trimmed} className="space-y-1">
        <div className="flex items-center justify-between gap-2">
          <legend className="text-[12.5px] font-medium text-fg">Providers</legend>
          {choosable.length > 1 && (
            <button
              type="button"
              onClick={() => setPicked(allOn ? new Set() : new Set(choosable.map((p) => p.id)))}
              className={cn("min-h-11 rounded-md px-2 text-[12.5px] font-medium text-link hover:underline disabled:opacity-50 sm:min-h-8", focusRing)}
            >
              {allOn ? "Clear all" : "Select all"}
            </button>
          )}
        </div>
        {!trimmed && <p className="text-[12px] text-fg-muted">Enter a model ID first.</p>}
        <ul className="divide-y divide-line rounded-lg border border-line bg-surface">
          {surplus.map((p) => {
            const dup = trimmed !== "" && already(p.id);
            const id = `${uid}-${p.id}`;
            return (
              <li key={p.id}>
                <label htmlFor={id} className={cn("flex min-h-11 items-center gap-2.5 px-3 py-1.5", dup ? "cursor-not-allowed" : "cursor-pointer hover:bg-hover")}>
                  <input
                    id={id}
                    type="checkbox"
                    checked={dup || picked.has(p.id)}
                    disabled={dup}
                    onChange={(e) => toggle(p.id, e.target.checked)}
                    className="h-4 w-4 shrink-0 accent-[var(--color-accent-500)]"
                  />
                  <ProviderLogo icon={providerIcon(p, p.id)} name={p.display_name} size={18} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[13px] text-fg">{p.display_name}</span>
                    {trimmed && (
                      <span className="block truncate font-mono text-[11.5px] text-fg-muted">{autoMarketSlug(p, trimmed)}</span>
                    )}
                  </span>
                  {dup ? (
                    <span className="shrink-0 text-[12px] text-fg-muted">Already a step</span>
                  ) : trimmed && !listed(p.id) && !catalog.loading ? (
                    <span className="shrink-0 text-[12px] text-warn">Not in its model list</span>
                  ) : null}
                </label>
              </li>
            );
          })}
        </ul>
      </fieldset>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="secondary" onClick={() => setOpen(false)} className="[@media(pointer:coarse)]:min-h-11">
          Cancel
        </Button>
        <Button type="button" onClick={add} disabled={!trimmed || chosen.length === 0} className="[@media(pointer:coarse)]:min-h-11">
          {chosen.length === 1 ? "Add 1 step" : `Add ${chosen.length} steps`}
        </Button>
      </div>
    </section>
  );
}
