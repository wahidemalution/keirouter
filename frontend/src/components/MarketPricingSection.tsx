import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LineChart, Search } from "lucide-react";
import { api, type MarketBinding, type MarketPricingSettings } from "../lib/api";
import { Button, Card, Field, Input, SectionHeader, Spinner, Toggle } from "./ui";
import { useToast } from "./Toast";

function bindingKey(providerId: string, modelId: string) {
  return `${providerId}/${modelId}`;
}

export function MarketPricingSection() {
  const qc = useQueryClient();
  const toast = useToast();
  const settings = useQuery({ queryKey: ["market-pricing-settings"], queryFn: () => api.marketPricingSettings() });
  const [markup, setMarkup] = useState("");
  const [intervalMin, setIntervalMin] = useState("");
  useEffect(() => {
    if (settings.data) {
      setMarkup(String(settings.data.markup_percent));
      setIntervalMin(String(settings.data.refresh_interval_minutes));
    }
  }, [settings.data]);

  const save = useMutation({
    mutationFn: (patch: Partial<Pick<MarketPricingSettings, "auto_refresh" | "refresh_interval_minutes" | "markup_percent">>) =>
      api.updateMarketPricingSettings(patch),
    onSuccess: (data) => {
      qc.setQueryData(["market-pricing-settings"], data);
      toast.success("Saved", "Market pricing settings updated.");
    },
    onError: (e) => toast.error("Save failed", (e as Error).message),
  });
  const refresh = useMutation({
    mutationFn: () => api.refreshMarketPrices(),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["market-pricing-settings"] });
      qc.invalidateQueries({ queryKey: ["chains"] });
      toast.success("Synced", `${r.synced} chain price(s) updated.`);
    },
    onError: (e) => {
      qc.invalidateQueries({ queryKey: ["market-pricing-settings"] });
      toast.error("Sync failed", (e as Error).message);
    },
  });

  if (settings.isLoading || !settings.data) return <Spinner />;
  const data = settings.data;
  const markupTrim = markup.trim();
  const markupInvalid =
    markupTrim === "" || !Number.isFinite(Number(markup)) || Number(markup) < 0 || Number(markup) > 1000;
  const intervalTrim = intervalMin.trim();
  const intervalInvalid =
    intervalTrim === "" || !Number.isInteger(Number(intervalMin)) || Number(intervalMin) < 1;

  return (
    <div className="space-y-4">
      <Card>
        <SectionHeader
          title="Market Pricing"
          description="Auto-update bound chain prices from the market. Markup is applied on top of the cheapest bound slug."
          icon={LineChart}
          iconTone="neutral"
        />
        <div className="divide-y divide-[var(--border)] border-t border-[var(--border)]">
          <div className="flex items-center justify-between gap-4 px-6 py-4">
            <div>
              <p className="text-sm font-medium">Auto-refresh</p>
              <p className="mt-0.5 text-xs text-[var(--text-muted)]">
                Periodically sync bound chain prices from the market.
              </p>
            </div>
            <Toggle checked={data.auto_refresh} onChange={(v) => save.mutate({ auto_refresh: v })} />
          </div>

          <div className="px-6 py-5">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label="Markup %">
                <Input
                  type="number"
                  min={0}
                  max={1000}
                  value={markup}
                  onChange={(e) => setMarkup(e.target.value)}
                  className="w-28"
                />
                <p className="mt-1 text-xs text-[var(--text-muted)]">
                  Added on top of the cheapest bound slug price.
                </p>
              </Field>
              <Field label="Refresh interval (minutes)">
                <Input
                  type="number"
                  min={1}
                  value={intervalMin}
                  onChange={(e) => setIntervalMin(e.target.value)}
                  className="w-24"
                />
              </Field>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-3 px-6 py-4">
            <Button
              onClick={() =>
                save.mutate({
                  markup_percent: Number(markup),
                  refresh_interval_minutes: Number(intervalMin),
                })
              }
              disabled={save.isPending || markupInvalid || intervalInvalid}
            >
              {save.isPending ? "Saving…" : "Save"}
            </Button>
            <Button variant="ghost" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
              {refresh.isPending ? "Refreshing…" : "Refresh now"}
            </Button>
            {data.last_fetched_at && (
              <span className="text-xs text-[var(--text-muted)]">
                Last sync {new Date(data.last_fetched_at).toLocaleString()} · {data.last_synced_count} updated
              </span>
            )}
            {data.last_fetch_error && (
              <span className="text-xs text-[color:var(--color-danger)]">{data.last_fetch_error}</span>
            )}
          </div>
        </div>
      </Card>

      <MarketBindingsCard />
    </div>
  );
}

function MarketBindingsCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const bindings = useQuery({ queryKey: ["market-bindings"], queryFn: () => api.listMarketBindings() });
  const providers = useQuery({ queryKey: ["providers"], queryFn: () => api.providers() });
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<string | null>(null);

  const models = useQuery({
    queryKey: ["provider-models", selected],
    queryFn: () => api.providerModels(selected!),
    enabled: !!selected,
  });

  const byKey = useMemo(() => {
    const map = new Map<string, MarketBinding>();
    for (const b of bindings.data?.bindings ?? []) {
      map.set(bindingKey(b.provider_id, b.model_id), b);
    }
    return map;
  }, [bindings.data]);

  const filtered = useMemo(() => {
    const rows = providers.data?.providers ?? [];
    const q = search.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter((p) => p.display_name.toLowerCase().includes(q) || p.id.toLowerCase().includes(q));
  }, [providers.data, search]);

  const selectedProvider = providers.data?.providers.find((p) => p.id === selected) ?? null;

  const invalidate = () => qc.invalidateQueries({ queryKey: ["market-bindings"] });

  if (bindings.isLoading || providers.isLoading) return <Spinner />;

  return (
    <Card>
      <SectionHeader
        title="Market Bindings"
        description="Bind a provider model to the market slug it should be priced from."
        icon={LineChart}
        iconTone="neutral"
      />
      <div className="border-t border-[var(--border)]">
        <div className="px-6 py-4">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-muted)]" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search providers…"
              className="pl-9"
            />
          </div>
          <div className="mt-3 max-h-56 overflow-y-auto rounded-xl border border-[var(--border)]">
            {filtered.length === 0 ? (
              <p className="px-3 py-4 text-sm text-[var(--text-muted)]">No providers match.</p>
            ) : (
              filtered.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => setSelected(p.id)}
                  className={`flex w-full items-center justify-between px-3 py-2 text-left text-sm transition-colors hover:bg-[var(--bg-subtle)] ${
                    selected === p.id ? "bg-[var(--bg-subtle)]" : ""
                  }`}
                >
                  <span className="font-medium">{p.display_name}</span>
                  <span className="text-xs text-[var(--text-muted)]">{p.id}</span>
                </button>
              ))
            )}
          </div>
        </div>

        {selectedProvider && (
          <div className="border-t border-[var(--border)] px-6 py-4">
            <p className="mb-3 text-sm font-medium">{selectedProvider.display_name} models</p>
            {models.isLoading ? (
              <Spinner />
            ) : (models.data?.models ?? []).length === 0 ? (
              <p className="text-sm text-[var(--text-muted)]">No models for this provider.</p>
            ) : (
              <div className="space-y-3">
                {models.data!.models.map((m) => (
                  <ModelBindingRow
                    key={m.id}
                    providerId={selectedProvider.id}
                    modelId={m.id}
                    current={byKey.get(bindingKey(selectedProvider.id, m.id))?.market_slug ?? ""}
                    onChanged={invalidate}
                    toast={toast}
                  />
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </Card>
  );
}

function ModelBindingRow({
  providerId,
  modelId,
  current,
  onChanged,
  toast,
}: {
  providerId: string;
  modelId: string;
  current: string;
  onChanged: () => void;
  toast: ReturnType<typeof useToast>;
}) {
  const [slug, setSlug] = useState(current);
  useEffect(() => {
    setSlug(current);
  }, [current]);

  const save = useMutation({
    mutationFn: async (value: string): Promise<MarketBinding | { deleted: boolean }> =>
      value.trim() === ""
        ? api.deleteMarketBinding(providerId, modelId)
        : api.setMarketBinding(providerId, modelId, value.trim()),
    onSuccess: () => {
      onChanged();
      toast.success("Saved", `${modelId} binding updated.`);
    },
    onError: (e) => toast.error("Save failed", (e as Error).message),
  });

  const dirty = slug.trim() !== current;

  return (
    <div className="flex items-center gap-2">
      <span className="w-48 shrink-0 truncate text-sm" title={modelId}>
        {modelId}
      </span>
      <Input
        value={slug}
        onChange={(e) => setSlug(e.target.value)}
        placeholder="market slug"
        className="flex-1"
      />
      <Button onClick={() => save.mutate(slug)} disabled={save.isPending || !dirty}>
        {save.isPending ? "Saving…" : "Save"}
      </Button>
      <Button
        variant="ghost"
        onClick={() => {
          setSlug("");
          save.mutate("");
        }}
        disabled={save.isPending || current === ""}
      >
        Clear
      </Button>
    </div>
  );
}
