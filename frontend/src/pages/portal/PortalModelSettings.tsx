import { useId, useMemo, useState, useSyncExternalStore, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { BadgePercent, Layers, Search, SlidersHorizontal } from "lucide-react";
import {
  fetchPortalModelSettings, fetchPortalStatus, resetPortalModelDiscount, setPortalDiscountFloor,
  setPortalFinalFallback, setPortalModelDiscount, setPortalModelProvider,
  type PortalModelSettings, type PortalModelSettingsRow,
} from "../../lib/api";
import { Badge, Button, EmptyState, ErrorCard, Input, Spinner } from "../../components/ui";
import {
  FallbackControl, ProvidersButton, ProvidersPanel, TOUCH, errorText, useRowSettings,
  type RowSettingsSource,
} from "../../components/modelSettings/ProviderControls";
import { modelGroup } from "../../lib/modelGroup";
import { portal } from "../../lib/portalRoutes";
import { PageHeader, PanelCard } from "./components";

const EXAMPLE_LIST_PRICE = 10;
const QUERY_KEY = ["portal-model-settings"];
const ROW_SOURCE: RowSettingsSource = {
  queryKey: QUERY_KEY,
  setProvider: setPortalModelProvider,
  setFallback: setPortalFinalFallback,
};
const WIDE_QUERY = "(min-width: 40rem)"; // Tailwind's sm breakpoint

function parsePct(value: string, max: number): number | null {
  const n = Number(value);
  return value.trim() !== "" && Number.isInteger(n) && n >= 0 && n <= max ? n : null;
}

// The table needs room for four columns; below sm each model is a card.
function subscribeWide(onChange: () => void) {
  const mq = window.matchMedia(WIDE_QUERY);
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}
function useWideLayout(): boolean {
  return useSyncExternalStore(subscribeWide, () => window.matchMedia(WIDE_QUERY).matches);
}

// DefaultDiscountCard sets the minimum discount for every model without its
// own setting. The worked example turns the percentage into money.
function DefaultDiscountCard({ current, max }: { current: number; max: number }) {
  const queryClient = useQueryClient();
  const [value, setValue] = useState(String(current));
  const save = useMutation({
    mutationFn: setPortalDiscountFloor,
    onSuccess: (res) => {
      setValue(String(res.min_discount_pct));
      queryClient.invalidateQueries({ queryKey: QUERY_KEY });
      queryClient.invalidateQueries({ queryKey: ["portal-key"] });
    },
  });

  const parsed = parsePct(value, max);
  const dirty = parsed !== null && parsed !== current;
  const examplePct = parsed !== null && parsed > 0 ? parsed : 30;
  const examplePays = (EXAMPLE_LIST_PRICE * (100 - examplePct)) / 100;

  return (
    <PanelCard
      icon={BadgePercent}
      title="Default minimum discount"
      subtitle="Used for every model below that doesn't have its own number. Models marked No discount guarantee are skipped."
      action={current > 0 ? <Badge tone="success">At least {current}% off</Badge> : <Badge>Off</Badge>}
    >
      <form
        className="space-y-2 p-4 sm:p-5"
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty && parsed !== null) save.mutate(parsed);
        }}
      >
        <label htmlFor="discount-default" className="block text-[12.5px] font-medium text-fg">
          Minimum discount (%)
        </label>
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <Input
            id="discount-default"
            type="number"
            inputMode="numeric"
            min={0}
            max={max}
            step={1}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-invalid={parsed === null}
            aria-describedby="discount-default-help"
            className={`tabular-nums sm:w-32 ${TOUCH}`}
          />
          <Button type="submit" disabled={!dirty || save.isPending} className={TOUCH}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
        <p id="discount-default-help" className="text-[12px] text-fg-muted">
          {parsed !== null ? `0 turns it off. Whole numbers from 0 to ${max}.` : `Enter a whole number from 0 to ${max}.`}
        </p>
        {save.isError && <p role="alert" className="text-[12.5px] text-bad">{(save.error as Error).message}</p>}
        {save.isSuccess && !dirty && <p role="status" className="text-[12.5px] text-fg-muted">Saved.</p>}
        <div className="mt-3 space-y-1.5 rounded-md border border-line bg-subtle p-3 text-[12.5px] text-fg-muted">
          <p className="font-medium text-fg">How it works</p>
          <p>
            Pick how much cheaper than the official price a request must be. Example: the official price is{" "}
            ${EXAMPLE_LIST_PRICE.toFixed(2)} per 1M tokens. At {examplePct}%, you never pay more than{" "}
            <span className="font-medium tabular-nums text-fg">${examplePays.toFixed(2)}</span> per 1M tokens.
          </p>
          <p>
            If no one sells the model that cheaply right now, we stop the request (error 422) and you pay nothing. Try
            again in 15 seconds, or lower the number.
          </p>
          <p>A higher number means you pay less, but more requests get stopped. Set 0 to turn it off.</p>
        </div>
      </form>
    </PanelCard>
  );
}

// DiscountEditor edits one model's own minimum, or returns it to the default.
// The parent keys it on the saved values so the input resets after a save.
function DiscountEditor({
  row, defaultPct, max, label,
}: {
  row: PortalModelSettingsRow;
  defaultPct: number;
  max: number;
  /** Visible label; the table passes none and relies on the column header. */
  label?: string;
}) {
  const queryClient = useQueryClient();
  const inputId = useId();
  const helpId = `${inputId}-help`;
  const [value, setValue] = useState(String(row.override_pct ?? row.effective_pct));
  const done = () => queryClient.invalidateQueries({ queryKey: QUERY_KEY });
  const save = useMutation({ mutationFn: (pct: number) => setPortalModelDiscount(row.model, pct), onSuccess: done });
  const reset = useMutation({
    mutationFn: () => resetPortalModelDiscount(row.model),
    onSuccess: () => {
      setValue(String(defaultPct));
      done();
    },
  });

  if (!row.supported) {
    return (
      <div className="space-y-1">
        {label && <p className="text-[12px] font-medium text-fg-muted">{label}</p>}
        <Badge tone="warning">No discount guarantee</Badge>
        <p className="text-[12px] text-fg-muted">Runs at its set price.</p>
      </div>
    );
  }

  const parsed = parsePct(value, max);
  const own = row.override_pct !== null;
  const dirty = parsed !== null && (own ? parsed !== row.override_pct : parsed !== defaultPct);
  const error = save.error ?? reset.error;

  return (
    <div className="space-y-1.5">
      {label ? (
        <label htmlFor={inputId} className="block text-[12px] font-medium text-fg-muted">{label}</label>
      ) : (
        <label htmlFor={inputId} className="sr-only">Minimum discount for {row.model} (%)</label>
      )}
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty && parsed !== null) save.mutate(parsed);
        }}
      >
        <Input
          id={inputId}
          type="number"
          inputMode="numeric"
          min={0}
          max={max}
          step={1}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          aria-invalid={parsed === null}
          aria-describedby={helpId}
          className={`w-20 tabular-nums ${TOUCH}`}
        />
        <span className="text-[12.5px] text-fg-muted" aria-hidden="true">%</span>
        <Button type="submit" variant="secondary" disabled={!dirty || save.isPending} className={TOUCH}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </form>
      <div id={helpId} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-fg-muted">
        {parsed === null ? (
          <span>Whole number from 0 to {max}.</span>
        ) : own ? (
          <>
            <span>Own setting.</span>
            <button
              type="button"
              disabled={reset.isPending}
              onClick={() => reset.mutate()}
              className={`rounded font-medium text-link hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500 disabled:opacity-50 ${TOUCH} [@media(pointer:coarse)]:px-1`}
            >
              {reset.isPending ? "Resetting…" : `Use default (${defaultPct}%)`}
            </button>
          </>
        ) : (
          <span>(default)</span>
        )}
      </div>
      {error && <p role="alert" className="text-[12.5px] text-bad">{errorText(error)}</p>}
    </div>
  );
}

interface RowProps {
  row: PortalModelSettingsRow;
  defaultPct: number;
  max: number;
  open: boolean;
  onToggle: () => void;
}

function discountKey(row: PortalModelSettingsRow, defaultPct: number) {
  return `${row.model}-${row.override_pct ?? "d"}-${defaultPct}`;
}

function ModelTableRow({ row, defaultPct, max, open, onToggle }: RowProps) {
  const settings = useRowSettings(row, ROW_SOURCE);
  const panelId = useId();
  return (
    <>
      <tr className="align-top">
        <td className="px-4 py-3">
          <span className="break-all font-mono text-[13px] font-medium text-fg">{row.model}</span>
        </td>
        <td className="px-4 py-3">
          <DiscountEditor key={discountKey(row, defaultPct)} row={row} defaultPct={defaultPct} max={max} />
        </td>
        <td className="px-4 py-3">
          <ProvidersButton row={row} settings={settings} open={open} panelId={panelId} onToggle={onToggle} />
        </td>
        <td className="px-4 py-3">
          <FallbackControl row={row} settings={settings} />
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={4} className="px-4 pb-4 pt-0">
            <ProvidersPanel row={row} settings={settings} id={panelId} />
          </td>
        </tr>
      )}
    </>
  );
}

function ModelCard({ row, defaultPct, max, open, onToggle }: RowProps) {
  const settings = useRowSettings(row, ROW_SOURCE);
  const panelId = useId();
  return (
    <li className="space-y-3 px-4 py-4">
      <h3 className="break-all font-mono text-[13px] font-medium text-fg">{row.model}</h3>
      <DiscountEditor key={discountKey(row, defaultPct)} row={row} defaultPct={defaultPct} max={max} label="Min. discount" />
      <div className="flex items-center justify-between gap-3">
        <span className="text-[12px] font-medium text-fg-muted">Providers on</span>
        <ProvidersButton row={row} settings={settings} open={open} panelId={panelId} onToggle={onToggle} />
      </div>
      <div className="flex items-center justify-between gap-3">
        <span className="text-[12px] font-medium text-fg-muted">Final fallback</span>
        <FallbackControl row={row} settings={settings} />
      </div>
      {open && <ProvidersPanel row={row} settings={settings} id={panelId} />}
    </li>
  );
}

function GroupButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`inline-flex min-h-9 items-center gap-1.5 rounded-lg border px-3 text-[13px] font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500 focus-visible:ring-offset-2 focus-visible:ring-offset-surface ${TOUCH} ${
        active ? "border-transparent bg-action text-action-fg" : "border-line bg-surface text-fg-muted hover:border-line-strong hover:text-fg"
      }`}
    >
      {children}
    </button>
  );
}

function ModelsPanel({ data }: { data: PortalModelSettings }) {
  const { default_pct: defaultPct, max_pct: max, models } = data;
  const wide = useWideLayout();
  const [query, setQuery] = useState("");
  const [group, setGroup] = useState<string | null>(null);
  const [openModels, setOpenModels] = useState<Set<string>>(() => new Set());

  const groups = useMemo(() => {
    const byId = new Map<string, { id: string; label: string; count: number }>();
    for (const m of models) {
      const g = modelGroup(m.model);
      const entry = byId.get(g.id) ?? { ...g, count: 0 };
      entry.count++;
      byId.set(g.id, entry);
    }
    return [...byId.values()].sort((a, b) =>
      a.id === "other" ? 1 : b.id === "other" ? -1 : a.label.localeCompare(b.label),
    );
  }, [models]);

  // A group that left the list (after a refetch) stops filtering.
  const activeGroup = group && groups.some((g) => g.id === group) ? group : null;
  const q = query.trim().toLowerCase();
  const visible = models.filter(
    (m) => (!activeGroup || modelGroup(m.model).id === activeGroup) && (!q || m.model.toLowerCase().includes(q)),
  );

  const toggleOpen = (model: string) =>
    setOpenModels((prev) => {
      const next = new Set(prev);
      if (next.has(model)) next.delete(model);
      else next.add(model);
      return next;
    });

  const rowProps = (m: PortalModelSettingsRow): RowProps => ({
    row: m,
    defaultPct,
    max,
    open: openModels.has(m.model),
    onToggle: () => toggleOpen(m.model),
  });

  return (
    <PanelCard
      icon={Layers}
      title="Models"
      subtitle="Set each model's minimum discount, which providers may serve it, and whether the final fallback runs when they all fail."
      count={models.length}
    >
      {models.length === 0 ? (
        <EmptyState
          title="No models for this key yet"
          hint="When your key can call a model, its settings show up here."
        />
      ) : (
        <>
          <div className="space-y-3 border-b border-line px-4 py-3 sm:px-5">
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
            {groups.length > 1 && (
              <div role="group" aria-label="Filter by model family" className="flex flex-wrap gap-2">
                <GroupButton active={activeGroup === null} onClick={() => setGroup(null)}>All</GroupButton>
                {groups.map((g) => (
                  <GroupButton key={g.id} active={activeGroup === g.id} onClick={() => setGroup(g.id)}>
                    {g.label}
                    <span className="tabular-nums">{g.count}</span>
                  </GroupButton>
                ))}
              </div>
            )}
          </div>
          {visible.length === 0 ? (
            <EmptyState
              size="sm"
              icon={Search}
              title="No models match"
              hint="Try another name, or show every model."
              action={
                <Button
                  variant="secondary"
                  className={TOUCH}
                  onClick={() => {
                    setQuery("");
                    setGroup(null);
                  }}
                >
                  Show all models
                </Button>
              }
            />
          ) : wide ? (
            <div className="overflow-x-auto">
              <table className="w-full text-[13px]">
                <thead className="border-b border-line bg-subtle text-left text-[12px] text-fg-muted">
                  <tr>
                    <th scope="col" className="px-4 py-2.5 font-medium">Model</th>
                    <th scope="col" className="px-4 py-2.5 font-medium whitespace-nowrap">Min. discount</th>
                    <th scope="col" className="px-4 py-2.5 font-medium whitespace-nowrap">Providers on</th>
                    <th scope="col" className="px-4 py-2.5 font-medium whitespace-nowrap">Final fallback</th>
                  </tr>
                </thead>
                {visible.map((m) => (
                  <tbody key={m.model} className="border-b border-line last:border-b-0">
                    <ModelTableRow {...rowProps(m)} />
                  </tbody>
                ))}
              </table>
            </div>
          ) : (
            <ul className="divide-y divide-line">
              {visible.map((m) => (
                <ModelCard key={m.model} {...rowProps(m)} />
              ))}
            </ul>
          )}
        </>
      )}
    </PanelCard>
  );
}

export function PortalModelSettingsPage() {
  const status = useQuery({ queryKey: ["portal-status"], queryFn: fetchPortalStatus, retry: false });
  const settings = useQuery({ queryKey: QUERY_KEY, queryFn: fetchPortalModelSettings, enabled: !!status.data?.has_key });

  if (status.isLoading) return <Spinner />;
  if (!status.data) return <ErrorCard message="Failed to load portal status" />;

  if (status.data.has_key === false) {
    return (
      <div className="space-y-6">
        <PageHeader icon={SlidersHorizontal} title="Model Settings" />
        <EmptyState
          variant="card"
          icon={SlidersHorizontal}
          title="No API key yet"
          hint="Create an API key to choose discounts, providers and the final fallback for its models."
          action={<Link to={portal("/key")} className="text-[13px] font-medium text-link hover:underline">Create an API key</Link>}
        />
      </div>
    );
  }

  if (settings.isLoading) return <Spinner label="Loading model settings" />;
  if (!settings.data) {
    return (
      <div className="space-y-6">
        <PageHeader icon={SlidersHorizontal} title="Model Settings" />
        <ErrorCard
          message={settings.error instanceof Error ? settings.error.message : "Failed to load model settings"}
          action={
            <Button variant="secondary" className={TOUCH} disabled={settings.isFetching} onClick={() => settings.refetch()}>
              {settings.isFetching ? "Trying again…" : "Try again"}
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        icon={SlidersHorizontal}
        title="Model Settings"
        subtitle="Choose the smallest discount your requests must get and which providers serve each model."
      />
      <DefaultDiscountCard key={`default-${settings.data.default_pct}`} current={settings.data.default_pct} max={settings.data.max_pct} />
      <ModelsPanel data={settings.data} />
    </div>
  );
}
