import { useId, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Lock } from "lucide-react";
import type { PortalModelProvider, PortalModelSettingsRow } from "../../lib/api";
import { Badge, Toggle } from "../ui";
import { useConfirm } from "../ui/confirm-dialog";
import { formatCostUSD } from "../../lib/format";

// Provider and final-fallback controls shared by the portal's Model Settings
// page and the admin key Routing tab. Each page names its own query key and
// write calls through a RowSettingsSource.

// Shared controls are 36px tall for a mouse; on touch screens they grow to 44px.
export const TOUCH = "[@media(pointer:coarse)]:min-h-11";

function formatPerM(v: number): string {
  if (v >= 0.01) return `$${v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 4 })}`;
  return formatCostUSD(v);
}

// priceText renders "$in / $out per 1M", or null when neither price is known.
export function priceText(input: number | null, output: number | null): string | null {
  if (input == null && output == null) return null;
  const side = (v: number | null) => (v == null ? "n/a" : formatPerM(v));
  return `${side(input)} / ${side(output)} per 1M`;
}

export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : "Something went wrong. Try again.";
}

export function fallbackOn(row: PortalModelSettingsRow): boolean {
  return row.has_fallback && row.final_fallback;
}

/** Where a page keeps its settings in the query cache and how it writes them. */
export interface RowSettingsSource {
  queryKey: readonly unknown[];
  setProvider: (model: string, provider: string, enabled: boolean) => Promise<PortalModelSettingsRow>;
  setFallback: (model: string, enabled: boolean) => Promise<PortalModelSettingsRow>;
}

// useRowSettings holds one model's provider and fallback writes. Both answer
// with the updated row, which goes straight into the cache before a refetch.
export function useRowSettings(row: PortalModelSettingsRow, source: RowSettingsSource) {
  const queryClient = useQueryClient();
  const confirm = useConfirm();
  const apply = (updated: PortalModelSettingsRow) => {
    queryClient.setQueryData<{ models: PortalModelSettingsRow[] }>(source.queryKey, (old) =>
      old ? { ...old, models: old.models.map((m) => (m.model === row.model ? updated : m)) } : old,
    );
    queryClient.invalidateQueries({ queryKey: source.queryKey });
  };
  const refetch = () => queryClient.invalidateQueries({ queryKey: source.queryKey });
  const provider = useMutation({
    mutationFn: (v: { id: string; enabled: boolean }) => source.setProvider(row.model, v.id, v.enabled),
    onSuccess: apply,
    onError: refetch,
  });
  const fallback = useMutation({
    mutationFn: (enabled: boolean) => source.setFallback(row.model, enabled),
    onSuccess: apply,
    onError: refetch,
  });

  const enabledCount = row.providers.filter((p) => p.enabled).length;
  // One write per row at a time. The switches stay enabled while a write runs:
  // disabling the focused switch would drop keyboard focus to the page.
  const busy = provider.isPending || fallback.isPending;

  async function toggleProvider(p: PortalModelProvider, next: boolean) {
    if (busy) return;
    // Turning off the last provider with no fallback leaves nothing to serve.
    if (!next && p.enabled && enabledCount === 1 && !fallbackOn(row)) {
      const ok = await confirm({
        title: "Turn off every provider?",
        description: (
          <>
            Requests for <span className="break-all font-mono text-fg">{row.model}</span> will fail until you turn a
            provider or the final fallback back on.
          </>
        ),
        confirmLabel: "Turn off",
        cancelLabel: "Keep it on",
        tone: "danger",
      });
      if (!ok) return;
    }
    fallback.reset();
    provider.mutate({ id: p.id, enabled: next });
  }

  async function setFallback(on: boolean) {
    if (busy) return;
    // With every provider off, the fallback is the only route left.
    if (!on && enabledCount === 0) {
      const ok = await confirm({
        title: "Turn off the final fallback?",
        description: (
          <>
            Every provider for <span className="break-all font-mono text-fg">{row.model}</span> is off, so requests will
            fail until you turn a provider or the final fallback back on.
          </>
        ),
        confirmLabel: "Turn off",
        cancelLabel: "Keep it on",
        tone: "danger",
      });
      if (!ok) return;
    }
    provider.reset();
    fallback.mutate(on);
  }

  return {
    enabledCount,
    toggleProvider,
    providerError: provider.error,
    setFallback,
    fallbackError: fallback.error,
  };
}

export type RowSettings = ReturnType<typeof useRowSettings>;

// ToggleTarget widens a switch to a 44px square tap area. A label forwards
// its clicks to the switch it names, so the padding is live.
export function ToggleTarget({ htmlFor, children }: { htmlFor: string; children: ReactNode }) {
  return (
    <label htmlFor={htmlFor} className="flex min-h-11 min-w-11 shrink-0 cursor-pointer items-center has-[:disabled]:cursor-not-allowed justify-center">
      {children}
    </label>
  );
}

// ProvidersButton shows "2 of 3" and opens the row's provider list.
export function ProvidersButton({
  row, settings, open, panelId, onToggle,
}: {
  row: PortalModelSettingsRow;
  settings: RowSettings;
  open: boolean;
  panelId: string;
  onToggle: () => void;
}) {
  const none = settings.enabledCount === 0 && !fallbackOn(row);
  return (
    <div className="space-y-0.5">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={onToggle}
        className={`-ml-2 inline-flex min-h-9 items-center gap-1.5 rounded-lg px-2 text-[13px] font-medium tabular-nums text-fg transition-colors hover:bg-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500 ${TOUCH}`}
      >
        {settings.enabledCount} of {row.providers.length}
        <span className="sr-only"> providers on for {row.model}</span>
        <ChevronDown
          className={`h-4 w-4 text-fg-muted transition-transform duration-150 ${open ? "rotate-180" : ""}`}
          aria-hidden="true"
        />
      </button>
      {none && <p className="text-[12px] font-medium text-bad">Every request fails</p>}
    </div>
  );
}

export function FallbackControl({ row, settings }: { row: PortalModelSettingsRow; settings: RowSettings }) {
  const toggleId = useId();
  if (!row.has_fallback) return <span className="text-[12.5px] text-fg-muted">None</span>;
  return (
    <div className="space-y-0.5">
      <div className="-ml-1 flex items-center gap-1">
        <ToggleTarget htmlFor={toggleId}>
          <Toggle
            id={toggleId}
            checked={row.final_fallback}
            onChange={(on) => void settings.setFallback(on)}
            label={`Final fallback for ${row.model}`}
          />
        </ToggleTarget>
        <span className="text-[12.5px] text-fg-muted" aria-hidden="true">{row.final_fallback ? "On" : "Off"}</span>
      </div>
      {settings.fallbackError && <p role="alert" className="text-[12px] text-bad">{errorText(settings.fallbackError)}</p>}
    </div>
  );
}

function providerStatus(p: PortalModelProvider, lockedLabel: string): ReactNode {
  if (p.locked) {
    return (
      <Badge>
        <Lock className="h-3 w-3" aria-hidden="true" />
        {lockedLabel}
      </Badge>
    );
  }
  return p.available ? <Badge tone="success">Available</Badge> : <Badge tone="warning">No offer right now</Badge>;
}

function ProviderItem({
  p, model, settings, lockedLabel = "Locked by admin",
}: {
  p: PortalModelProvider;
  model: string;
  settings: RowSettings;
  lockedLabel?: string;
}) {
  const toggleId = useId();
  const nameId = `${toggleId}-name`;
  const infoId = `${toggleId}-info`;
  const price = p.available ? priceText(p.input_per_m, p.output_per_m) : null;
  return (
    <li className="flex items-center justify-between gap-3 py-2">
      <div className="min-w-0 space-y-1">
        <p id={nameId} className="break-words text-[13px] font-medium text-fg">
          {p.name}
          <span className="sr-only"> for {model}</span>
        </p>
        <div id={infoId} className="flex flex-wrap items-center gap-x-2 gap-y-1">
          {providerStatus(p, lockedLabel)}
          {price && <span className="text-[12px] tabular-nums text-fg-muted">{price}</span>}
        </div>
      </div>
      <ToggleTarget htmlFor={toggleId}>
        <Toggle
          id={toggleId}
          checked={p.enabled}
          onChange={(next) => void settings.toggleProvider(p, next)}
          disabled={p.locked}
          aria-labelledby={nameId}
          aria-describedby={infoId}
        />
      </ToggleTarget>
    </li>
  );
}

// ProvidersPanel is the open row: one switch per provider, then the
// fallback's normal price.
export function ProvidersPanel({
  row, settings, id, lockedLabel,
}: {
  row: PortalModelSettingsRow;
  settings: RowSettings;
  id: string;
  /** Badge text for a provider the row cannot switch on; the portal says who locked it. */
  lockedLabel?: string;
}) {
  const fallbackPrice = priceText(row.fallback_input_per_m || null, row.fallback_output_per_m || null);
  return (
    <div id={id} className="space-y-2 rounded-lg border border-line bg-subtle px-3 py-1 sm:px-4">
      {row.providers.length > 0 ? (
        <ul className="divide-y divide-line">
          {row.providers.map((p) => (
            <ProviderItem key={p.id} p={p} model={row.model} settings={settings} lockedLabel={lockedLabel} />
          ))}
        </ul>
      ) : (
        <p className="py-2 text-[12.5px] text-fg-muted">This model has no providers to choose from.</p>
      )}
      {settings.providerError && (
        <p role="alert" className="pb-1 text-[12.5px] text-bad">{errorText(settings.providerError)}</p>
      )}
      {row.has_fallback && (
        <div className="space-y-0.5 border-t border-line py-2.5 text-[12.5px] text-fg-muted">
          <p>
            <span className="font-medium text-fg">Final fallback: </span>
            {fallbackPrice
              ? `Runs last, at the normal price of ${fallbackPrice}, only when every provider above fails.`
              : "Runs last, at the normal price, only when every provider above fails."}
          </p>
          {row.final_fallback && row.final_fallback_kept && <p className="text-[12px]">On from before.</p>}
        </div>
      )}
    </div>
  );
}
