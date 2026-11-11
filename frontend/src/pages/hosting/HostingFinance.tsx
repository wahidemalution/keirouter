import { useMemo, useState, type FormEvent } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bar, CartesianGrid, ComposedChart, Line, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import {
  AlertTriangle,
  Calculator,
  Check,
  Coins,
  Loader2,
  Pencil,
  PiggyBank,
  Plus,
  Receipt,
  Repeat,
  Scale,
  Server,
  TrendingUp,
  Trash2,
  Wallet,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { C_COST, C_PROFIT, C_REV, LegendItem, VIZ_VARS, axisTick } from "../../components/financeChart";
import {
  api,
  type CostCurrency,
  type CostPayment,
  type CostPaymentCategory,
  type CostPaymentInput,
  type CostPaymentMethod,
  type HostingFinance,
  type HostingFinanceMonth,
} from "../../lib/api";
import { useToast } from "../../components/Toast";
import { useConfirm } from "../../components/ui/confirm-dialog";
import {
  Badge,
  Button,
  EmptyState,
  ErrorBanner,
  Field,
  Input,
  Kpi,
  KpiGrid,
  LoadingRegion,
  Modal,
  ModalFooter,
  SegmentedControl,
  Select,
  Skeleton,
  Textarea,
} from "../../components/ui";
import { Panel, TH, THEAD, count, dateID, errorText, idr, toDateInput, todayISO } from "../payments/shared";
import { QK_HOSTING, idrCompact, monthLabel } from "./shared";

type Range = "6" | "12" | "24";

const CATEGORY_LABEL: Record<CostPaymentCategory, string> = {
  vps: "VPS",
  upstream_ai: "Upstream AI",
  domain: "Domain",
  software: "Software",
  other: "Other",
};

const CATEGORY_TONE: Record<CostPaymentCategory, "accent" | "secondary" | "neutral" | "warning"> = {
  vps: "accent",
  upstream_ai: "secondary",
  domain: "neutral",
  software: "warning",
  other: "neutral",
};

const METHOD_LABEL: Record<CostPaymentMethod, string> = {
  debit_card: "Debit card",
  credit_card: "Credit card",
  bank_transfer: "Bank transfer",
  crypto: "Crypto",
};

const usd = (n: number) => new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(n);

function signedIdr(n: number): string {
  return n > 0 ? `+${idr(n)}` : idr(n);
}

export function FinanceTab() {
  const [range, setRange] = useState<Range>("12");
  const q = useQuery({
    queryKey: [...QK_HOSTING.finance, range],
    queryFn: () => api.hostingFinance(Number(range)),
    placeholderData: keepPreviousData,
  });
  const f = q.data;

  if (q.isLoading && !f) {
    return (
      <LoadingRegion label="Loading hosting finance" className="space-y-4">
        <Skeleton className="h-[188px] w-full rounded-2xl" />
        <Skeleton className="h-[340px] w-full rounded-2xl" />
      </LoadingRegion>
    );
  }
  if (!f) {
    return <ErrorBanner message={`Couldn't load hosting finance. ${q.isError ? errorText(q.error) : ""}`.trim()} />;
  }

  return (
    <div className="space-y-6">
      {q.isError && <ErrorBanner message={`Couldn't refresh the data. The numbers below may be out of date. ${errorText(q.error)}`} />}
      <RefundWarning refunds={f.refunds_needed ?? []} />
      <CurrentMonth f={f} />
      <MonthlyChart f={f} range={range} onRange={setRange} fetching={q.isFetching} />
    </div>
  );
}

// ─── Current month ──────────────────────────────────────────────────────────

function CurrentMonth({ f }: { f: HostingFinance }) {
  const c = f.current;
  const month = monthLabel(c.month);
  const gap = c.break_even_instances - c.running_instances;
  return (
    <div className="space-y-2">
      <KpiGrid cols={4} label={`Hosting finance, ${month}`}>
        <Kpi icon={Wallet} label={`Revenue, ${month}`} value={idr(c.revenue_idr)} hint="Gross, before SumoPod fees" />
        <Kpi icon={Receipt} label="Costs" value={idr(c.cost_idr)} hint="Operating costs this month" />
        <Kpi
          icon={PiggyBank}
          label="Profit"
          value={signedIdr(c.profit_idr)}
          tone={c.profit_idr < 0 ? "bad" : c.profit_idr > 0 ? "ok" : "section"}
          valueClassName={c.profit_idr < 0 ? "text-bad" : c.profit_idr > 0 ? "text-ok" : ""}
          hint={c.profit_idr < 0 ? "Loss this month" : c.profit_idr > 0 ? "Profit this month" : "Break-even"}
        />
        <Kpi
          icon={Repeat}
          label="MRR"
          value={idr(c.mrr_idr)}
          hint={c.price_idr > 0 ? `${count(c.running_instances)} running, avg ${idr(c.price_idr)}` : "No price set"}
        />
      </KpiGrid>
      <KpiGrid cols={3} label="Capacity and break-even">
        <Kpi icon={Server} label="Running instances" value={count(c.running_instances)} hint="Active, plus expired ones not yet stopped" />
        <Kpi
          icon={Calculator}
          label="Cost per instance"
          value={c.running_instances > 0 ? idr(c.cost_per_instance_idr) : "-"}
          hint={c.running_instances > 0 ? "This month's costs ÷ running instances" : "No running instances yet"}
        />
        <Kpi
          icon={Scale}
          label="Break-even"
          value={c.price_idr > 0 ? `${count(c.break_even_instances)} ${c.break_even_instances === 1 ? "instance" : "instances"}` : "-"}
          tone={c.price_idr > 0 && c.cost_idr > 0 ? (gap <= 0 ? "ok" : "warn") : "section"}
          hint={
            c.price_idr <= 0
              ? "Set a price in Hosting setup"
              : c.cost_idr <= 0
                ? "No costs this month yet"
                : gap <= 0
                  ? "Costs covered this month"
                  : `${count(gap)} more instances to break even`
          }
        />
      </KpiGrid>
      <p className="text-[12px] text-fg-muted">
        Revenue is the gross value of paid orders. SumoPod fees aren't deducted, so actual profit is slightly lower.
      </p>
    </div>
  );
}

// ─── Monthly chart ──────────────────────────────────────────────────────────

type Row = HostingFinanceMonth & { label: string };

function MonthlyChart({ f, range, onRange, fetching }: { f: HostingFinance; range: Range; onRange: (r: Range) => void; fetching: boolean }) {
  const [view, setView] = useState<"chart" | "table">("chart");
  const rows: Row[] = useMemo(() => f.months.map((m) => ({ ...m, label: monthLabel(m.month) })), [f.months]);
  const hasData = rows.some((r) => r.revenue_idr !== 0 || r.cost_idr !== 0);
  const hasLoss = rows.some((r) => r.profit_idr < 0);
  const t = f.totals;

  return (
    <Panel
      icon={TrendingUp}
      title="Revenue vs costs"
      subtitle={
        <span className="tabular-nums">
          Last {range} months · revenue {idr(t.revenue_idr)} · costs {idr(t.cost_idr)} · profit{" "}
          <span className={cn("font-medium", t.profit_idr < 0 ? "text-bad" : t.profit_idr > 0 ? "text-ok" : "text-fg")}>{signedIdr(t.profit_idr)}</span>
        </span>
      }
      action={
        <div className="flex flex-wrap items-center gap-2">
          {fetching && <Loader2 className="h-4 w-4 animate-spin text-fg-faint" aria-label="Loading" />}
          <SegmentedControl<Range>
            label="Month range"
            value={range}
            onChange={onRange}
            options={[
              { value: "6", label: "6 months" },
              { value: "12", label: "12 months" },
              { value: "24", label: "24 months" },
            ]}
          />
          <SegmentedControl<"chart" | "table">
            label="View"
            value={view}
            onChange={setView}
            options={[
              { value: "chart", label: "Chart" },
              { value: "table", label: "Table" },
            ]}
          />
        </div>
      }
    >
      {!hasData ? (
        <EmptyState
          icon={TrendingUp}
          size="sm"
          title="No revenue or costs yet"
          hint="The chart fills in once a hosting order is paid or a payment is recorded on the Operating costs tab."
        />
      ) : view === "chart" ? (
        <div className={cn("px-4 pb-3 pt-4", VIZ_VARS)}>
          <ul className="mb-3 flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-fg-muted" aria-label="Chart legend">
            <LegendItem color={C_REV} label="Revenue" />
            <LegendItem color={C_COST} label="Costs" />
            <LegendItem color={C_PROFIT} label="Profit" line />
          </ul>
          <div className="h-[280px] w-full" role="img" aria-label={chartSummary(rows)}>
            <ResponsiveContainer width="100%" height="100%">
              <ComposedChart data={rows} margin={{ top: 8, right: 8, left: 0, bottom: 0 }} barGap={2} barCategoryGap="24%">
                <CartesianGrid vertical={false} stroke="var(--border)" />
                <XAxis dataKey="label" tick={axisTick} tickLine={false} axisLine={{ stroke: "var(--border-strong)" }} dy={6} minTickGap={16} interval="preserveStartEnd" />
                <YAxis tick={axisTick} tickLine={false} axisLine={false} tickFormatter={idrCompact} width={72} />
                {hasLoss && <ReferenceLine y={0} stroke="var(--border-strong)" />}
                <Tooltip cursor={{ fill: "var(--bg-subtle)", opacity: 0.7 }} content={<FinanceTooltip />} />
                <Bar dataKey="revenue_idr" name="Revenue" fill={C_REV} radius={[4, 4, 0, 0]} maxBarSize={24} isAnimationActive={false} />
                <Bar dataKey="cost_idr" name="Costs" fill={C_COST} radius={[4, 4, 0, 0]} maxBarSize={24} isAnimationActive={false} />
                <Line
                  dataKey="profit_idr"
                  name="Profit"
                  type="monotone"
                  stroke={C_PROFIT}
                  strokeWidth={2}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  dot={{ r: 4, fill: C_PROFIT, stroke: "var(--bg-elevated)", strokeWidth: 2 }}
                  activeDot={{ r: 5, fill: C_PROFIT, stroke: "var(--bg-elevated)", strokeWidth: 2 }}
                  isAnimationActive={false}
                />
              </ComposedChart>
            </ResponsiveContainer>
          </div>
        </div>
      ) : (
        <MonthlyTable rows={rows} />
      )}
    </Panel>
  );
}

function chartSummary(rows: Row[]): string {
  if (!rows.length) return "Monthly revenue, costs and profit chart.";
  const last = rows[rows.length - 1];
  const best = rows.reduce((a, b) => (b.profit_idr > a.profit_idr ? b : a));
  return `Monthly revenue, costs and profit from ${rows[0].label} to ${last.label}. This month: revenue ${idr(last.revenue_idr)}, costs ${idr(last.cost_idr)}, profit ${idr(last.profit_idr)}. Highest profit ${idr(best.profit_idr)} in ${best.label}. Switch to Table view for full numbers.`;
}

function FinanceTooltip({ active, payload, label }: any) {
  if (!active || !payload?.length) return null;
  const row = payload[0]?.payload as Row | undefined;
  if (!row) return null;
  const items = [
    { label: "Revenue", color: C_REV, value: idr(row.revenue_idr) },
    { label: "Costs", color: C_COST, value: idr(row.cost_idr) },
    { label: "Profit", color: C_PROFIT, value: signedIdr(row.profit_idr) },
  ];
  return (
    <div className={cn("rounded-lg border border-line bg-surface px-3 py-2 text-[12.5px] shadow-[var(--shadow-pop)]", VIZ_VARS)}>
      <p className="mb-1 font-medium text-fg">{label}</p>
      {items.map((it) => (
        <p key={it.label} className="flex items-center gap-2 text-fg-muted">
          <span className="h-2 w-2 shrink-0 rounded-[2px]" style={{ background: it.color }} aria-hidden="true" />
          {it.label}
          <span className="ml-auto pl-4 font-medium tabular-nums text-fg">{it.value}</span>
        </p>
      ))}
      <p className="mt-1 border-t border-line pt-1 text-[11.5px] tabular-nums text-fg-faint">
        Orders: {count(row.orders)}{row.refund_idr > 0 ? ` · refund ${idr(row.refund_idr)}` : ""}
      </p>
    </div>
  );
}

function MonthlyTable({ rows }: { rows: Row[] }) {
  return (
    <div className="overflow-x-auto" role="region" aria-label="Monthly revenue and costs table" tabIndex={0}>
      <table className="w-full min-w-[640px] text-[13px]">
        <thead>
          <tr className={THEAD}>
            <th scope="col" className={TH}>Month</th>
            <th scope="col" className={cn(TH, "text-right")}>Revenue</th>
            <th scope="col" className={cn(TH, "text-right")}>Costs</th>
            <th scope="col" className={cn(TH, "text-right")}>Profit</th>
            <th scope="col" className={cn(TH, "text-right")}>Orders</th>
            <th scope="col" className={cn(TH, "text-right")}>Refund</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {[...rows].reverse().map((r) => (
            <tr key={r.month} className="tabular-nums">
              <th scope="row" className="whitespace-nowrap px-4 py-2 text-left font-normal text-fg">{r.label}</th>
              <td className="px-4 py-2 text-right">{idr(r.revenue_idr)}</td>
              <td className="px-4 py-2 text-right">{idr(r.cost_idr)}</td>
              <td className={cn("px-4 py-2 text-right font-medium", r.profit_idr < 0 ? "text-bad" : r.profit_idr > 0 ? "text-ok" : "text-fg")}>{signedIdr(r.profit_idr)}</td>
              <td className="px-4 py-2 text-right text-fg-muted">{count(r.orders)}</td>
              <td className="px-4 py-2 text-right text-fg-muted">{r.refund_idr > 0 ? idr(r.refund_idr) : "-"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ─── Refunds ────────────────────────────────────────────────────────────────

function RefundWarning({ refunds }: { refunds: NonNullable<HostingFinance["refunds_needed"]> }) {
  if (refunds.length === 0) return null;
  const total = refunds.reduce((s, r) => s + r.amount_idr, 0);
  return (
    <section role="alert" className="rounded-2xl border border-warn/30 bg-warn/10 px-4 py-3">
      <div className="flex items-start gap-2.5">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" strokeWidth={1.75} aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <h2 className="text-[13px] font-semibold text-fg">
            Refunds needed: {count(refunds.length)} ({idr(total)})
          </h2>
          <p className="mt-0.5 text-[12.5px] text-fg-muted">These orders were paid, but their instance was deleted before use. Refund them through SumoPod.</p>
          <ul className="mt-2 divide-y divide-warn/20 text-[12.5px]">
            {refunds.map((r) => (
              <li key={r.order_id} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-1.5">
                <span className="min-w-0 break-all font-mono text-[12px] text-fg">{r.order_id}</span>
                <span className="flex flex-wrap items-baseline gap-x-3 tabular-nums text-fg-muted">
                  <span>Paid {r.paid_at ? dateID(r.paid_at, true) : "-"}</span>
                  {r.instance_id && <span className="font-mono text-[11.5px]">instance {r.instance_id}</span>}
                  <span className="font-medium text-fg">{idr(r.amount_idr)}</span>
                </span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </section>
  );
}

// ─── Operating costs ────────────────────────────────────────────────────────

/** The Operating costs tab: a history of paid costs. Costs ride on the
 * hosting finance response, so a change refreshes the hosting chart through
 * the same query key. */
export function CostsTab() {
  const q = useQuery({ queryKey: [...QK_HOSTING.finance, "costs"], queryFn: () => api.hostingFinance(1) });
  if (q.isLoading && !q.data) {
    return (
      <LoadingRegion label="Loading operating costs">
        <Skeleton className="h-[240px] w-full rounded-2xl" />
      </LoadingRegion>
    );
  }
  if (!q.data) {
    return <ErrorBanner message={`Couldn't load operating costs. ${q.isError ? errorText(q.error) : ""}`.trim()} />;
  }
  return (
    <div className="space-y-6">
      {q.isError && <ErrorBanner message={`Couldn't refresh the costs. The list below may be out of date. ${errorText(q.error)}`} />}
      <CostsPanel costs={q.data.costs} monthTotal={q.data.current.cost_idr} />
    </div>
  );
}

function CostsPanel({ costs, monthTotal }: { costs: CostPayment[]; monthTotal: number }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [editing, setEditing] = useState<CostPayment | "new" | null>(null);

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteCostPayment(id),
    onSuccess: () => {
      toast.success("Payment deleted");
      qc.invalidateQueries({ queryKey: QK_HOSTING.finance });
    },
    onError: (e) => toast.error("Couldn't delete payment", errorText(e)),
  });

  const onDelete = async (c: CostPayment) => {
    const ok = await confirm({
      title: "Delete payment?",
      description: (
        <>
          <span className="font-medium text-fg">{c.name}</span> ({idr(c.amount_idr)}, paid {dateID(c.paid_at)}) is removed from that month's
          costs, so its profit changes.
        </>
      ),
      confirmLabel: "Delete payment",
      cancelLabel: "Cancel",
      tone: "danger",
    });
    if (ok) remove.mutate(c.id);
  };

  return (
    <Panel
      icon={Coins}
      title="Operating costs"
      subtitle={costs.length ? `Payment history. This month: ${idr(monthTotal)} · ${count(costs.length)} payments in total.` : "Payment history."}
      action={
        <Button onClick={() => setEditing("new")}>
          <Plus aria-hidden="true" />
          Record payment
        </Button>
      }
    >
      {costs.length === 0 ? (
        <EmptyState
          icon={Coins}
          size="sm"
          title="No payments yet"
          hint="Record each VPS bill or upstream AI top-up when you pay it. It counts in that month's costs."
        />
      ) : (
        <div className="overflow-x-auto" role="region" aria-label="Operating cost payments table" tabIndex={0}>
          <table className="w-full min-w-[880px] text-[13px]">
            <thead>
              <tr className={THEAD}>
                <th scope="col" className={TH}>Paid</th>
                <th scope="col" className={TH}>Name</th>
                <th scope="col" className={TH}>Category</th>
                <th scope="col" className={TH}>Method</th>
                <th scope="col" className={cn(TH, "text-right")}>Amount</th>
                <th scope="col" className={TH}>Note</th>
                <th scope="col" className={cn(TH, "text-right")}>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {costs.map((c) => (
                <tr key={c.id} className="align-top transition-colors hover:bg-hover">
                  <td className="whitespace-nowrap px-4 py-2.5 tabular-nums text-fg-muted">{dateID(c.paid_at)}</td>
                  <td className="px-4 py-2.5">
                    <p className="font-medium text-fg">{c.name}</p>
                    {c.provider && <p className="text-[11.5px] text-fg-muted">{c.provider}</p>}
                  </td>
                  <td className="px-4 py-2.5">
                    <Badge tone={CATEGORY_TONE[c.category] ?? "neutral"}>{CATEGORY_LABEL[c.category] ?? c.category}</Badge>
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5 text-fg-muted">{METHOD_LABEL[c.payment_method] ?? c.payment_method}</td>
                  <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">
                    {c.currency === "USD" && c.amount_usd !== null ? (
                      <>
                        <span className="font-medium text-fg">{usd(c.amount_usd)}</span>
                        <p className="text-[11.5px] text-fg-muted">
                          {idr(c.amount_idr)} at {idr(c.usd_idr_rate ?? 0)}/USD
                        </p>
                      </>
                    ) : (
                      <span className="font-medium text-fg">{idr(c.amount_idr)}</span>
                    )}
                  </td>
                  <td className="max-w-[240px] break-words px-4 py-2.5 text-[12.5px] text-fg-muted">{c.note || "-"}</td>
                  <td className="px-4 py-2.5">
                    <div className="flex justify-end gap-1">
                      <Button variant="ghost" onClick={() => setEditing(c)} title="Edit payment" aria-label={`Edit ${c.name}`}>
                        <Pencil aria-hidden="true" />
                      </Button>
                      <Button
                        variant="ghost"
                        onClick={() => onDelete(c)}
                        disabled={remove.isPending && remove.variables === c.id}
                        title="Delete payment"
                        aria-label={`Delete ${c.name}`}
                      >
                        {remove.isPending && remove.variables === c.id ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Trash2 aria-hidden="true" />}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && <CostModal cost={editing === "new" ? null : editing} onClose={() => setEditing(null)} />}
    </Panel>
  );
}

function CostModal({ cost, onClose }: { cost: CostPayment | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState(cost?.name ?? "");
  const [category, setCategory] = useState<CostPaymentCategory>(cost?.category ?? "vps");
  const [provider, setProvider] = useState(cost?.provider ?? "");
  const [currency, setCurrency] = useState<CostCurrency>(cost?.currency ?? "USD");
  const [amount, setAmount] = useState(cost ? String(cost.currency === "USD" ? cost.amount_usd ?? "" : cost.amount_idr) : "");
  const [method, setMethod] = useState<CostPaymentMethod>(cost?.payment_method ?? "debit_card");
  const [note, setNote] = useState(cost?.note ?? "");
  const today = todayISO();
  const [paidDate, setPaidDate] = useState(cost ? toDateInput(cost.paid_at) : today);
  const [touched, setTouched] = useState(false);
  // The live rate only previews a new USD payment; the server locks the rate on save.
  const rateQ = useQuery({ queryKey: ["currency-settings"], queryFn: () => api.currencySettings(), enabled: !cost && currency === "USD" });

  const amountNum = Number(amount);
  const amountValid = amount.trim() !== "" && Number.isFinite(amountNum) && amountNum > 0 && (currency === "IDR" ? Number.isInteger(amountNum) : Math.round(amountNum * 100) > 0);
  const needsProvider = category === "upstream_ai";
  const nameError = touched && !name.trim() ? "Enter a name." : null;
  const providerError = touched && needsProvider && !provider.trim() ? "Enter the provider you paid." : null;
  const dateValid = /^\d{4}-\d{2}-\d{2}$/.test(paidDate) && paidDate >= "2000-01-01" && paidDate <= today;
  const dateError = touched && !dateValid ? (paidDate > today ? "Pick today or an earlier day." : "Pick the day it was paid.") : null;
  const backDated = !cost && dateValid && paidDate !== today;
  const amountError = touched && !amountValid ? (currency === "IDR" ? "Enter whole rupiah above 0." : "Enter a dollar amount of at least 0.01.") : null;

  const rate = cost ? cost.usd_idr_rate ?? 0 : rateQ.data?.effective_rate ?? 0;
  let amountHint: string | undefined;
  if (currency === "USD" && !amountError) {
    if (cost) amountHint = amountValid ? `${idr(Math.round(amountNum * rate))} at the saved rate of ${idr(rate)}/USD.` : `Converted at the saved rate of ${idr(rate)}/USD.`;
    else if (rateQ.isLoading) amountHint = "Loading today's rate…";
    else if (rate > 0)
      amountHint = `${amountValid ? `About ${idr(Math.round(amountNum * rate))}` : "Converted"} at today's rate of ${idr(rate)}/USD. The rate is locked when you save${backDated ? ", even for an earlier paid date" : ""}.`;
    else amountHint = "No USD to IDR rate is available yet. Refresh it or set an override in Settings, Currency.";
  } else if (currency === "IDR" && !amountError && amountValid) {
    amountHint = idr(amountNum);
  }

  const save = useMutation({
    mutationFn: (body: CostPaymentInput) => (cost ? api.updateCostPayment(cost.id, body) : api.createCostPayment(body)),
    onSuccess: () => {
      toast.success(cost ? "Payment updated" : "Payment recorded");
      qc.invalidateQueries({ queryKey: QK_HOSTING.finance });
      onClose();
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (!name.trim() || !amountValid || !dateValid || (needsProvider && !provider.trim()) || save.isPending) return;
    save.mutate({
      name: name.trim(),
      category,
      provider: provider.trim(),
      currency,
      amount: amountNum,
      payment_method: method,
      note: note.trim(),
      paid_date: paidDate,
    });
  };

  return (
    <Modal
      open
      onClose={save.isPending ? () => {} : onClose}
      title={cost ? "Edit payment" : "Record payment"}
      subtitle={cost ? `Paid ${dateID(cost.paid_at)}` : "It counts in the month of its paid date."}
    >
      <form onSubmit={onSubmit} noValidate>
        <div className="space-y-4 px-5 py-4">
          <Field label="Name" error={nameError} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="VPS Contabo 8 GB" autoFocus />
          </Field>
          <Field label="Paid on" error={dateError} hint={dateError ? undefined : "Defaults to today. Pick an earlier day for a past bill."} required>
            <Input type="date" value={paidDate} min="2000-01-01" max={today} onChange={(e) => setPaidDate(e.target.value)} className="tabular-nums" />
          </Field>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label="Category">
              <Select value={category} onChange={(e) => setCategory(e.target.value as CostPaymentCategory)}>
                {(Object.keys(CATEGORY_LABEL) as CostPaymentCategory[]).map((k) => (
                  <option key={k} value={k}>
                    {CATEGORY_LABEL[k]}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Payment method">
              <Select value={method} onChange={(e) => setMethod(e.target.value as CostPaymentMethod)}>
                {(Object.keys(METHOD_LABEL) as CostPaymentMethod[]).map((k) => (
                  <option key={k} value={k}>
                    {METHOD_LABEL[k]}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          {needsProvider && (
            <Field label="Provider" error={providerError} required>
              <Input value={provider} onChange={(e) => setProvider(e.target.value)} placeholder="OpenRouter" maxLength={120} />
            </Field>
          )}
          <div className="space-y-1.5">
            <span id="cost-currency-label" className="block text-[12.5px] font-medium text-fg">
              Currency
            </span>
            <SegmentedControl<CostCurrency>
              aria-labelledby="cost-currency-label"
              value={currency}
              onChange={setCurrency}
              options={[
                { value: "USD", label: "USD", disabled: Boolean(cost) && cost?.currency !== "USD" },
                { value: "IDR", label: "IDR", disabled: Boolean(cost) && cost?.currency !== "IDR" },
              ]}
            />
            {cost && <p className="text-[12px] text-fg-muted">The currency of a saved payment is fixed. Delete it and record a new one to change it.</p>}
          </div>
          <Field label={currency === "USD" ? "Amount (USD)" : "Amount (IDR)"} error={amountError} hint={amountHint} required>
            <Input
              type="number"
              inputMode="decimal"
              min={currency === "USD" ? "0.01" : "1"}
              step={currency === "USD" ? "0.01" : "1"}
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              placeholder={currency === "USD" ? "12.50" : "250000"}
              className="tabular-nums"
            />
          </Field>
          <Field label="Note" optional>
            <Textarea value={note} onChange={(e) => setNote(e.target.value)} rows={2} placeholder="e.g. invoice number or the card used" />
          </Field>
          {save.isError && <ErrorBanner message={`Couldn't save payment. ${errorText(save.error)}`} />}
        </div>
        <ModalFooter>
          <Button type="button" variant="ghost" onClick={onClose} disabled={save.isPending}>
            Cancel
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Check aria-hidden="true" />}
            {cost ? "Save" : "Record"}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  );
}
