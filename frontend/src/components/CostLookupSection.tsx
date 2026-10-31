import { useEffect, useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, AlertTriangle, CheckCircle2, Clock3, Loader2 } from "lucide-react";
import { api, type CostLookupPatch, type CostLookupReport, type CustomProvider } from "../lib/api";
import { cn } from "@/lib/utils";
import { formatCostUSD } from "../lib/format";
import { useToast } from "./Toast";
import { FormError, TextField } from "./connect/ConnectKit";
import { Button, SectionTitle, Skeleton, Toggle } from "./ui";
import { ICONS } from "../lib/icons";

// CostLookupSection lets an operator bill a Subrouter provider from its own
// usage log: requests are billed at the chain step price first, then the
// sweeper corrects each bill to Subrouter's charge times the step markup.
export function CostLookupSection({ providerId }: { providerId: string }) {
  const providers = useQuery({
    queryKey: ["custom-providers"],
    queryFn: api.listCustomProviders,
    // Until the first check has run, refresh so the status line updates by itself.
    refetchInterval: (q) => {
      const p = q.state.data?.providers.find((x) => x.id === providerId);
      return p?.cost_lookup === "subrouter" && !p.cost_lookup_status ? 5_000 : false;
    },
  });
  const provider = providers.data?.providers.find((p) => p.id === providerId);

  return (
    <section aria-labelledby="cost-lookup-heading" className="mt-5 overflow-hidden rounded-2xl border border-line bg-surface shadow-[var(--shadow-card)]">
      <div className="border-b border-line px-4 py-3">
        <SectionTitle
          id="cost-lookup-heading"
          icon={ICONS.spend}
          title="Bill from the Subrouter usage log"
          subtitle="Each request is billed at the step price first, then corrected to what Subrouter charged, times the step markup."
        />
      </div>
      {providers.isLoading && (
        <div className="space-y-2 p-4" aria-busy="true">
          <p className="text-[12.5px] text-fg-muted">Loading billing settings…</p>
          <Skeleton className="h-24 w-full rounded-xl" />
        </div>
      )}
      {providers.isError && (
        <p role="alert" className="flex items-start gap-2 p-4 text-[13px] text-fg">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-bad" aria-hidden="true" />
          Couldn't load billing settings: {(providers.error as Error).message}
        </p>
      )}
      {provider && <CostLookupForm provider={provider} />}
    </section>
  );
}

function CostLookupForm({ provider }: { provider: CustomProvider }) {
  const qc = useQueryClient();
  const toast = useToast();
  const formId = useId();
  const toggleLabelId = useId();
  const saved = {
    on: provider.cost_lookup === "subrouter",
    baseURL: provider.cost_lookup_base_url ?? "",
    userID: provider.cost_lookup_user_id ?? "",
    tokenName: provider.cost_lookup_token_name ?? "",
  };
  const [on, setOn] = useState(saved.on);
  const [baseURL, setBaseURL] = useState(saved.baseURL);
  const [userID, setUserID] = useState(saved.userID);
  const [token, setToken] = useState("");
  const [tokenName, setTokenName] = useState(saved.tokenName);
  const [error, setError] = useState("");
  const [showErrors, setShowErrors] = useState(false);

  useEffect(() => {
    setOn(saved.on);
    setBaseURL(saved.baseURL);
    setUserID(saved.userID);
    setTokenName(saved.tokenName);
    setToken("");
  }, [saved.on, saved.baseURL, saved.userID, saved.tokenName]);

  const patch: CostLookupPatch = {};
  if (on !== saved.on) patch.cost_lookup = on ? "subrouter" : "";
  if (baseURL.trim() !== saved.baseURL) patch.cost_lookup_base_url = baseURL.trim();
  if (userID.trim() !== saved.userID) patch.cost_lookup_user_id = userID.trim();
  if (tokenName.trim() !== saved.tokenName) patch.cost_lookup_token_name = tokenName.trim();
  if (token.trim() !== "") patch.cost_lookup_token = token.trim();
  const dirty = Object.keys(patch).length > 0;

  const userMissing = on && userID.trim() === "";
  const tokenMissing = on && !provider.cost_lookup_token_set && token.trim() === "";

  const save = useMutation({
    mutationFn: () => api.updateCustomProvider(provider.id, patch),
    onSuccess: () => {
      setToken("");
      setShowErrors(false);
      qc.invalidateQueries({ queryKey: ["custom-providers"] });
      qc.invalidateQueries({ queryKey: ["cost-lookup-report", provider.id] });
      toast.success(on ? "Subrouter billing saved" : "Subrouter billing turned off", on ? "The first check runs within 10 seconds." : undefined);
    },
    onError: (e: Error) => setError(e.message),
  });

  return (
    <>
      <form
        id={formId}
        noValidate
        className="space-y-4 p-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!dirty || save.isPending) return;
          if (userMissing || tokenMissing) {
            setShowErrors(true);
            const formEl = e.currentTarget;
            window.requestAnimationFrame(() => formEl.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
            return;
          }
          setError("");
          save.mutate();
        }}
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <p id={toggleLabelId} className="text-[13px] font-medium text-fg">
              Correct bills from the usage log
            </p>
            <p className="mt-0.5 text-[12.5px] leading-5 text-fg-muted">
              Needs a system access token from Subrouter (Personal Settings), not an sk- API key.
            </p>
          </div>
          <Toggle checked={on} onChange={(v) => setOn(v)} aria-labelledby={toggleLabelId} disabled={save.isPending} />
        </div>

        {on && (
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              label="Subrouter user ID"
              required
              value={userID}
              inputMode="numeric"
              onChange={(e) => {
                setUserID(e.target.value);
                setError("");
              }}
              className="font-mono"
              error={showErrors && userMissing ? "Enter the user ID shown in Subrouter's Personal Settings." : undefined}
            />
            <TextField
              label="System access token"
              required={!provider.cost_lookup_token_set}
              type="password"
              autoComplete="off"
              value={token}
              onChange={(e) => {
                setToken(e.target.value);
                setError("");
              }}
              placeholder={provider.cost_lookup_token_set ? "Saved. Leave empty to keep it." : undefined}
              error={showErrors && tokenMissing ? "Enter the system access token." : undefined}
              hint="Stored encrypted and never shown again."
            />
            <TextField
              label="Subrouter token name"
              optional
              value={tokenName}
              onChange={(e) => {
                setTokenName(e.target.value);
                setError("");
              }}
              hint="Counts only usage of this sk- key. Set it if the same Subrouter account is also used outside keirouter."
            />
            <TextField
              label="Usage log URL"
              optional
              value={baseURL}
              onChange={(e) => {
                setBaseURL(e.target.value);
                setError("");
              }}
              placeholder="https://subrouter.ai"
              className="font-mono"
              hint="Empty uses https://subrouter.ai."
            />
          </div>
        )}
        <FormError message={error} />
      </form>
      <div className="flex flex-col gap-2 border-t border-line bg-subtle px-4 py-3 sm:flex-row sm:items-center sm:justify-end sm:px-5">
        <span className="text-[12.5px] text-fg-muted sm:mr-auto" role="status" aria-live="polite">
          {save.isPending ? "Saving…" : dirty ? "Unsaved changes" : "Saved"}
        </span>
        <Button type="submit" form={formId} className="w-full sm:w-auto" disabled={!dirty || save.isPending}>
          {save.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
          Save billing settings
        </Button>
      </div>
      {saved.on && (
        <>
          <CostLookupStatus status={provider.cost_lookup_status ?? ""} />
          <CostLookupReportView providerId={provider.id} />
        </>
      )}
    </>
  );
}

function CostLookupStatus({ status }: { status: string }) {
  let icon = <Clock3 className="mt-0.5 h-4 w-4 shrink-0 text-fg-muted" aria-hidden="true" />;
  let text = "Waiting for the first check. It runs every 10 seconds.";
  if (status === "ok") {
    icon = <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-ok" aria-hidden="true" />;
    text = "Reading the usage log. Bills are corrected within about a minute of each request.";
  } else if (status === "unauthorized") {
    icon = <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-bad" aria-hidden="true" />;
    text = "Subrouter rejected the access token. Save a new system access token. Until then, requests keep the step price.";
  } else if (status.startsWith("error: ")) {
    icon = <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" aria-hidden="true" />;
    text = `Last check failed (${status.slice(7)}). It retries automatically; requests keep the step price meanwhile.`;
  }
  return (
    <p className="flex items-start gap-2 border-t border-line px-4 py-3 text-[13px] leading-5 text-fg" role="status">
      {icon}
      {text}
    </p>
  );
}

function utcDay(offsetDays: number): string {
  return new Date(Date.now() + offsetDays * 86_400_000).toISOString().slice(0, 10);
}

function CostLookupReportView({ providerId }: { providerId: string }) {
  const [which, setWhich] = useState<"yesterday" | "today">("yesterday");
  const day = which === "today" ? utcDay(0) : utcDay(-1);
  const report = useQuery({
    queryKey: ["cost-lookup-report", providerId, day],
    queryFn: () => api.costLookupReport(providerId, day),
    retry: false,
    // Each load asks Subrouter for the day's total; do not repeat it on focus.
    refetchOnWindowFocus: false,
    staleTime: 60_000,
  });

  return (
    <div className="border-t border-line p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-[13px] font-medium text-fg">Reconciliation for {day} (UTC)</h3>
        <div className="inline-flex rounded-lg border border-line p-0.5" role="group" aria-label="Report day">
          {(["yesterday", "today"] as const).map((v) => (
            <button
              key={v}
              type="button"
              aria-pressed={which === v}
              onClick={() => setWhich(v)}
              className={cn(
                "min-h-8 rounded-md px-3 text-[12.5px] transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500",
                which === v ? "bg-subtle font-medium text-fg" : "text-fg-muted hover:text-fg",
              )}
            >
              {v === "yesterday" ? "Yesterday" : "Today so far"}
            </button>
          ))}
        </div>
      </div>
      {report.isLoading && (
        <p className="flex items-center gap-2 text-[12.5px] text-fg-muted" aria-busy="true">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          Asking Subrouter for the day's total…
        </p>
      )}
      {report.isError && (
        <p role="alert" className="flex items-start gap-2 text-[13px] text-fg">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-bad" aria-hidden="true" />
          {(report.error as Error).message}
        </p>
      )}
      {report.data && <ReportBody r={report.data} />}
    </div>
  );
}

function ReportBody({ r }: { r: CostLookupReport }) {
  const chains = useQuery({ queryKey: ["chains"], queryFn: () => api.listChains(), staleTime: 30_000, enabled: (r.over_ceiling ?? []).length > 0 });
  const chainName = (id: string) => (id ? (chains.data?.chains.find((c) => c.id === id)?.name ?? id) : "no chain");
  const empty = r.subrouter_usd === 0 && r.keirouter_upstream_usd === 0 && r.reconciled + r.pending + r.unverified + r.orphan_count === 0;
  if (empty) return <p className="text-[13px] text-fg-muted">No Subrouter usage on this day.</p>;
  const groups = r.over_ceiling ?? [];
  return (
    <div className="space-y-3">
      {r.warn && (
        <p role="note" className="flex items-start gap-2 rounded-xl border border-warn/30 bg-warn/5 px-3 py-2.5 text-[12.5px] leading-5 text-fg">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" aria-hidden="true" />
          {r.gap_pct.toFixed(1)}% of what Subrouter charged has no matching cost in keirouter. Check the requests charged with no request row below.
        </p>
      )}
      <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-[13px] sm:grid-cols-2">
        <Row label="Subrouter charged" value={formatCostUSD(r.subrouter_usd)} />
        <Row label="Recorded upstream cost" value={formatCostUSD(r.keirouter_upstream_usd)} />
        <Row label="Not recorded" value={r.gap_pct > 0 ? `${r.gap_pct.toFixed(2)}%` : "None"} />
        <Row label="Reconciled requests" value={r.reconciled.toLocaleString("en-US")} />
        <Row label="Waiting for the log" value={r.pending.toLocaleString("en-US")} />
        <Row label="Unverified (kept step price)" value={r.unverified.toLocaleString("en-US")} />
        <Row label="Charged with no request row" value={`${r.orphan_count.toLocaleString("en-US")} (${formatCostUSD(r.orphan_usd)})`} />
      </dl>
      {groups.length > 0 && (
        <div className="text-[12.5px] leading-5 text-fg">
          <p className="font-medium">Cost above the step price</p>
          <p className="text-fg-muted">Billed at the real cost. Raise the step price of these chains so the first bill covers it.</p>
          <ul className="mt-1.5 space-y-1">
            {groups.map((g) => (
              <li key={`${g.chain_id}/${g.model}`} className="font-mono text-[12px]">
                {chainName(g.chain_id)} · {g.model}: {g.count}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-line/60 py-1">
      <dt className="text-fg-muted">{label}</dt>
      <dd className="font-mono tabular-nums text-fg">{value}</dd>
    </div>
  );
}
