import { useMemo, useState } from "react";
import { Bar, CartesianGrid, ComposedChart, Line, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { TrendingUp } from "lucide-react";
import { cn } from "@/lib/utils";
import type { FinanceRange, FinanceSeries, FinanceSeriesPoint } from "../../lib/api";
import { EmptyState, SegmentedControl } from "../../components/ui";
import { C_COST, C_PROFIT, C_REV, LegendItem, VIZ_VARS, axisTick } from "../../components/financeChart";
import { Panel, TH, THEAD, count, usd } from "./shared";
import { fmtDate, fmtTime } from "../../lib/serverTime";

type Row = FinanceSeriesPoint & { label: string; full: string };

const BUCKET_TEXT: Record<FinanceRange, string> = {
  today: "Per hour today",
  "7d": "Per day, last 7 days",
  "30d": "Per day, last 30 days",
  all: "Per month, all time",
};

function labels(start: string, granularity: FinanceSeries["granularity"]): { label: string; full: string } {
  const d = new Date(start);
  if (granularity === "hour") {
    const h = fmtTime(d, { hour: "2-digit", minute: "2-digit", hour12: false }, "en-GB");
    return { label: h, full: `${fmtDate(d, { day: "numeric", month: "short" }, "en-US")}, ${h}` };
  }
  if (granularity === "day") {
    return {
      label: fmtDate(d, { day: "numeric", month: "short" }, "en-US"),
      full: fmtDate(d, { weekday: "short", day: "numeric", month: "short", year: "numeric" }, "en-US"),
    };
  }
  return {
    label: fmtDate(d, { month: "short", year: "2-digit" }, "en-US"),
    full: fmtDate(d, { month: "long", year: "numeric" }, "en-US"),
  };
}

/** Short USD for axis ticks: $0.40, $12, $1.2K. */
function usdCompact(n: number): string {
  const sign = n < 0 ? "-" : "";
  const a = Math.abs(n);
  if (a >= 1e6) return `${sign}$${(a / 1e6).toLocaleString("en-US", { maximumFractionDigits: 1 })}M`;
  if (a >= 1e3) return `${sign}$${(a / 1e3).toLocaleString("en-US", { maximumFractionDigits: 1 })}K`;
  if (a >= 10 || a === 0) return `${sign}$${Math.round(a)}`;
  return `${sign}$${a.toFixed(2)}`;
}

export function FinanceChart({ series, range }: { series?: FinanceSeries; range: FinanceRange }) {
  const [view, setView] = useState<"chart" | "table">("chart");
  const rows: Row[] = useMemo(
    () => (series?.points ?? []).map((p) => ({ ...p, ...labels(p.start, series!.granularity) })),
    [series],
  );
  const hasData = rows.some((r) => r.requests > 0);
  const hasLoss = rows.some((r) => r.revenue_usd < 0);

  return (
    <Panel
      icon={TrendingUp}
      title="Billed vs provider cost"
      subtitle={`${BUCKET_TEXT[range]}. The gap between the bars is revenue.`}
      action={
        <SegmentedControl<"chart" | "table">
          label="View"
          value={view}
          onChange={setView}
          options={[
            { value: "chart", label: "Chart" },
            { value: "table", label: "Table" },
          ]}
        />
      }
    >
      {!hasData ? (
        <EmptyState
          icon={TrendingUp}
          size="sm"
          title="No billed requests in this range"
          hint="The chart fills in as requests are billed. Pick a longer time range above to see earlier usage."
        />
      ) : view === "chart" ? (
        <div className={cn("px-4 pb-3 pt-4", VIZ_VARS)}>
          <ul className="mb-3 flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-fg-muted" aria-label="Chart legend">
            <LegendItem color={C_REV} label="Billed" />
            <LegendItem color={C_COST} label="Provider cost" />
            <LegendItem color={C_PROFIT} label="Revenue" line />
          </ul>
          <div className="h-[280px] w-full" role="img" aria-label={chartSummary(rows)}>
            <ResponsiveContainer width="100%" height="100%">
              <ComposedChart data={rows} margin={{ top: 8, right: 8, left: 0, bottom: 0 }} barGap={2} barCategoryGap="24%">
                <CartesianGrid vertical={false} stroke="var(--border)" />
                <XAxis dataKey="label" tick={axisTick} tickLine={false} axisLine={{ stroke: "var(--border-strong)" }} dy={6} minTickGap={16} interval="preserveStartEnd" />
                <YAxis tick={axisTick} tickLine={false} axisLine={false} tickFormatter={usdCompact} width={56} />
                {hasLoss && <ReferenceLine y={0} stroke="var(--border-strong)" />}
                <Tooltip cursor={{ fill: "var(--bg-subtle)", opacity: 0.7 }} content={<ChartTooltip />} />
                <Bar dataKey="billed_usd" name="Billed" fill={C_REV} radius={[4, 4, 0, 0]} maxBarSize={24} isAnimationActive={false} />
                <Bar dataKey="cost_usd" name="Provider cost" fill={C_COST} radius={[4, 4, 0, 0]} maxBarSize={24} isAnimationActive={false} />
                <Line
                  dataKey="revenue_usd"
                  name="Revenue"
                  type="monotone"
                  stroke={C_PROFIT}
                  strokeWidth={2}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  dot={rows.length <= 31 ? { r: 3, fill: C_PROFIT, stroke: "var(--bg-elevated)", strokeWidth: 2 } : false}
                  activeDot={{ r: 5, fill: C_PROFIT, stroke: "var(--bg-elevated)", strokeWidth: 2 }}
                  isAnimationActive={false}
                />
              </ComposedChart>
            </ResponsiveContainer>
          </div>
        </div>
      ) : (
        <SeriesTable rows={rows} />
      )}
    </Panel>
  );
}

function chartSummary(rows: Row[]): string {
  const billed = rows.reduce((s, r) => s + r.billed_usd, 0);
  const cost = rows.reduce((s, r) => s + r.cost_usd, 0);
  const best = rows.reduce((a, b) => (b.revenue_usd > a.revenue_usd ? b : a));
  return `Billed versus provider cost from ${rows[0].full} to ${rows[rows.length - 1].full}. Billed ${usd(billed)}, provider cost ${usd(cost)}, revenue ${usd(billed - cost)}. Highest revenue ${usd(best.revenue_usd)} on ${best.full}. Switch to Table view for every number.`;
}

function ChartTooltip({ active, payload }: { active?: boolean; payload?: { payload: Row }[] }) {
  const row = active ? payload?.[0]?.payload : undefined;
  if (!row) return null;
  const items = [
    { label: "Billed", color: C_REV, value: usd(row.billed_usd) },
    { label: "Provider cost", color: C_COST, value: usd(row.cost_usd) },
    { label: "Revenue", color: C_PROFIT, value: usd(row.revenue_usd) },
  ];
  return (
    <div className={cn("rounded-lg border border-line bg-surface px-3 py-2 text-[12.5px] shadow-[var(--shadow-pop)]", VIZ_VARS)}>
      <p className="mb-1 font-medium text-fg">{row.full}</p>
      {items.map((it) => (
        <p key={it.label} className="flex items-center gap-2 text-fg-muted">
          <span className="h-2 w-2 shrink-0 rounded-[2px]" style={{ background: it.color }} aria-hidden="true" />
          {it.label}
          <span className="ml-auto pl-4 font-medium tabular-nums text-fg">{it.value}</span>
        </p>
      ))}
      <p className="mt-1 border-t border-line pt-1 text-[11.5px] tabular-nums text-fg-faint">Requests: {count(row.requests)}</p>
    </div>
  );
}

function SeriesTable({ rows }: { rows: Row[] }) {
  return (
    <div className="overflow-x-auto" role="region" aria-label="Billed and provider cost table" tabIndex={0}>
      <table className="w-full min-w-[560px] text-[13px]">
        <thead>
          <tr className={THEAD}>
            <th scope="col" className={TH}>Period</th>
            <th scope="col" className={cn(TH, "text-right")}>Billed</th>
            <th scope="col" className={cn(TH, "text-right")}>Provider cost</th>
            <th scope="col" className={cn(TH, "text-right")}>Revenue</th>
            <th scope="col" className={cn(TH, "text-right")}>Requests</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {[...rows].reverse().map((r) => (
            <tr key={r.start} className="tabular-nums">
              <th scope="row" className="whitespace-nowrap px-4 py-2 text-left font-normal text-fg">{r.full}</th>
              <td className="px-4 py-2 text-right">{usd(r.billed_usd)}</td>
              <td className="px-4 py-2 text-right">{usd(r.cost_usd)}</td>
              <td className={cn("px-4 py-2 text-right font-medium", r.revenue_usd < 0 ? "text-bad" : "text-fg")}>{usd(r.revenue_usd)}</td>
              <td className="px-4 py-2 text-right text-fg-muted">{count(r.requests)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
