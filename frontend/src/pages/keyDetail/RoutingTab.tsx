import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Layers, Network, Search } from "lucide-react";
import {
  fetchKeyModelSettings, setKeyAllowedProviders, setKeyFinalFallback, setKeyModelProvider,
  type APIKey, type KeyModelSettings, type PortalModelSettingsRow as KeyModelSettingsRow,
} from "../../lib/api";
import { Button, EmptyState, ErrorCard, Input, Skeleton } from "../../components/ui";
import {
  FallbackControl, ProvidersButton, ProvidersPanel, TOUCH, errorText, useRowSettings,
  type RowSettingsSource,
  MODEL_SETTINGS_POLL_MS,
} from "../../components/modelSettings/ProviderControls";
import { useToast } from "../../components/Toast";
import { PanelCard } from "../portal/components";

const settingsKey = (keyId: string) => ["key-model-settings", keyId];

function rowSource(keyId: string): RowSettingsSource {
  return {
    queryKey: settingsKey(keyId),
    setProvider: (model, provider, enabled) => setKeyModelProvider(keyId, model, provider, enabled),
    setFallback: (model, enabled) => setKeyFinalFallback(keyId, model, enabled),
  };
}

// AllowedProvidersCard edits the ceiling. The parent keys it on the saved
// list so the draft resets after a save or a refetch.
function AllowedProvidersCard({ keyId, data }: { keyId: string; data: KeyModelSettings }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const saved = data.allowed_providers;
  const [draft, setDraft] = useState<Set<string>>(() => new Set(saved));

  // A saved id that no chain uses any more still shows, so it can be unticked.
  const known = new Set(data.provider_options.map((o) => o.id));
  const options = [
    ...data.provider_options,
    ...saved.filter((id) => !known.has(id)).map((id) => ({ id, name: id, orphan: true })),
  ];

  const save = useMutation({
    mutationFn: (providers: string[]) => setKeyAllowedProviders(keyId, providers),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: settingsKey(keyId) });
      toast.success(
        res.allowed_providers.length > 0 ? "Allowed providers saved" : "Every provider allowed",
        res.allowed_providers.length > 0 ? `${res.allowed_providers.length} ${res.allowed_providers.length === 1 ? "provider" : "providers"} allowed.` : "This key has no provider limit.",
      );
    },
  });

  const next = options.filter((o) => draft.has(o.id)).map((o) => o.id);
  const savedSet = new Set(saved);
  const dirty = next.length !== saved.length || next.some((id) => !savedSet.has(id));

  const toggle = (id: string) => {
    save.reset();
    setDraft((prev) => {
      const out = new Set(prev);
      if (out.has(id)) out.delete(id);
      else out.add(id);
      return out;
    });
  };

  const summary = next.length === 0 ? "Every provider is allowed" : `${next.length} of ${options.length} providers allowed`;

  return (
    <PanelCard
      icon={Network}
      title="Allowed providers"
      subtitle="Empty means every provider. The customer can only choose inside this list."
      footer={
        <div className="flex flex-col gap-2 border-t border-line bg-subtle px-4 py-3 sm:flex-row sm:flex-wrap sm:items-center sm:px-5">
          <div className="min-w-0 flex-1 space-y-1 text-[12.5px]">
            <p role="status" className="text-fg-muted">
              <span className="text-fg">{summary}</span>
              {dirty ? " · Unsaved changes" : ""}
            </p>
            {save.isError && <p role="alert" className="text-bad">{errorText(save.error)}</p>}
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center [&>button]:w-full sm:[&>button]:w-auto">
            <Button
              variant="ghost"
              className={TOUCH}
              disabled={draft.size === 0 || save.isPending}
              onClick={() => {
                save.reset();
                setDraft(new Set());
              }}
            >
              Clear selection
            </Button>
            <Button className={TOUCH} disabled={!dirty || save.isPending} onClick={() => save.mutate(next)}>
              {save.isPending ? "Saving…" : "Save providers"}
            </Button>
          </div>
        </div>
      }
    >
      {options.length === 0 ? (
        <EmptyState
          size="sm"
          title="No providers to choose from"
          hint="Providers appear here once a chain has a step that uses them."
        />
      ) : (
        <fieldset className="min-w-0 px-2 py-2 sm:px-3">
          <legend className="sr-only">Providers this key may use</legend>
          <div className="grid gap-x-4 sm:grid-cols-2 lg:grid-cols-3">
            {options.map((o) => (
              <label
                key={o.id}
                className="flex min-h-11 cursor-pointer items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-hover has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent-500"
              >
                <input
                  type="checkbox"
                  checked={draft.has(o.id)}
                  onChange={() => toggle(o.id)}
                  className="h-4 w-4 shrink-0 rounded border-input accent-accent-500 focus:outline-none"
                />
                <span className="min-w-0 text-[13px] text-fg">
                  <span className="break-words font-medium">{o.name}</span>
                  {"orphan" in o && <span className="block text-[12px] text-fg-muted">No chain uses it. Untick it to save.</span>}
                </span>
              </label>
            ))}
          </div>
        </fieldset>
      )}
    </PanelCard>
  );
}

function discountNote(row: KeyModelSettingsRow): string | null {
  if (!row.supported) return "No discount guarantee";
  return row.effective_pct > 0 ? `Customer minimum discount: ${row.effective_pct}%` : null;
}

function ModelRow({ row, source }: { row: KeyModelSettingsRow; source: RowSettingsSource }) {
  const settings = useRowSettings(row, source);
  const panelId = useId();
  const [open, setOpen] = useState(false);
  const note = discountNote(row);
  return (
    <li className="space-y-3 px-4 py-3 sm:px-5">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-6">
        <div className="min-w-0 space-y-0.5">
          <h3 className="break-all font-mono text-[13px] font-medium text-fg">{row.model}</h3>
          {note && <p className="text-[12px] text-fg-muted">{note}</p>}
        </div>
        <div className="flex flex-wrap items-start gap-x-8 gap-y-2">
          <div className="space-y-0.5">
            <p className="text-[12px] font-medium text-fg-muted">Providers on</p>
            <ProvidersButton row={row} settings={settings} open={open} panelId={panelId} onToggle={() => setOpen((v) => !v)} />
          </div>
          <div className="space-y-0.5">
            <p className="text-[12px] font-medium text-fg-muted">Final fallback</p>
            <FallbackControl row={row} settings={settings} />
          </div>
        </div>
      </div>
      {open && <ProvidersPanel row={row} settings={settings} id={panelId} lockedLabel="Outside allowed providers" />}
    </li>
  );
}

function ModelsCard({ keyId, models }: { keyId: string; models: KeyModelSettingsRow[] }) {
  const [query, setQuery] = useState("");
  const source = rowSource(keyId);
  const q = query.trim().toLowerCase();
  const visible = q ? models.filter((m) => m.model.toLowerCase().includes(q)) : models;
  return (
    <PanelCard
      icon={Layers}
      title="Per-model settings"
      subtitle="Which providers may serve each model for this key, and whether the final fallback runs when they all fail. The customer sets minimum discounts."
      count={models.length}
    >
      {models.length === 0 ? (
        <EmptyState title="No models for this key" hint="When this key can call a model, its settings show up here." />
      ) : (
        <>
          <div className="border-b border-line px-4 py-3 sm:px-5">
            <div className="relative sm:max-w-xs">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-fg-muted" aria-hidden="true" />
              <Input
                type="search"
                aria-label="Search models"
                placeholder="Search models"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className={`pl-9 ${TOUCH}`}
              />
            </div>
          </div>
          {visible.length === 0 ? (
            <EmptyState
              size="sm"
              icon={Search}
              title="No models match"
              hint="Try another name, or show every model."
              action={<Button variant="secondary" className={TOUCH} onClick={() => setQuery("")}>Show all models</Button>}
            />
          ) : (
            <ul className="divide-y divide-line">
              {visible.map((m) => (
                <ModelRow key={m.model} row={m} source={source} />
              ))}
            </ul>
          )}
        </>
      )}
    </PanelCard>
  );
}

export function RoutingTab({ apiKey }: { apiKey: APIKey }) {
  const settings = useQuery({
    queryKey: settingsKey(apiKey.id),
    queryFn: () => fetchKeyModelSettings(apiKey.id),
    refetchInterval: MODEL_SETTINGS_POLL_MS,
  });

  if (settings.isLoading) return <Skeleton className="h-72 w-full rounded-2xl" />;
  if (!settings.data) {
    return (
      <ErrorCard
        message={settings.error instanceof Error ? settings.error.message : "Failed to load routing settings"}
        action={
          <Button variant="secondary" className={TOUCH} disabled={settings.isFetching} onClick={() => settings.refetch()}>
            {settings.isFetching ? "Trying again…" : "Try again"}
          </Button>
        }
      />
    );
  }

  return (
    <div className="space-y-6">
      <AllowedProvidersCard key={settings.data.allowed_providers.join("\u0000")} keyId={apiKey.id} data={settings.data} />
      <ModelsCard keyId={apiKey.id} models={settings.data.models} />
    </div>
  );
}
