import { useMemo } from "react";
import { TrendingUp } from "lucide-react";
import { cn } from "@/lib/utils";
import type { FinanceSeries, FinanceSeriesPoint } from "../../lib/api";
import { EmptyState } from "../../components/ui";
import { Panel, TH, THEAD, count, usd } from "./shared";
import { fmtDate, fmtTime } from "../../lib/serverTime";

type Row = FinanceSeriesPoint & { full: string };

const BUCKET_TEXT: Record<FinanceSeries["granularity"], string> = {
  hour: "Per hour",
  day: "Per day",
  month: "Per month",
};

function periodLabel(start: string, granularity: FinanceSeries["granularity"]): string {
  const d = new Date(start);
  if (granularity === "hour") {
    const h = fmtTime(d, { hour: "2-digit", minute: "2-digit", hour12: false }, "en-GB");
    return `${fmtDate(d, { day: "numeric", month: "short" }, "en-US")}, ${h}`;
  }
  if (granularity === "day") return fmtDate(d, { weekday: "short", day: "numeric", month: "short", year: "numeric" }, "en-US");
  return fmtDate(d, { month: "long", year: "numeric" }, "en-US");
}

/** Billed, provider cost and revenue per bucket for the selected range, newest first, with a totals row. */
export function FinanceSeriesTable({ series, label }: { series?: FinanceSeries; label: string }) {
  const rows: Row[] = useMemo(
    () => (series?.points ?? []).map((p) => ({ ...p, full: periodLabel(p.start, series!.granularity) })).reverse(),
    [series],
  );
  const total = useMemo(
    () =>
      rows.reduce(
        (t, r) => ({ billed: t.billed + r.billed_usd, cost: t.cost + r.cost_usd, revenue: t.revenue + r.revenue_usd, requests: t.requests + r.requests }),
        { billed: 0, cost: 0, revenue: 0, requests: 0 },
      ),
    [rows],
  );
  const granularity = series?.granularity ?? "day";

  return (
    <Panel icon={TrendingUp} title="Billed vs provider cost" subtitle={`${BUCKET_TEXT[granularity]}, ${label}. Revenue is billed minus provider cost.`}>
      {total.requests === 0 ? (
        <EmptyState
          icon={TrendingUp}
          size="sm"
          title="No billed requests in this range"
          hint="Rows appear as requests are billed. Pick a longer time range above to see earlier usage."
        />
      ) : (
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
              {rows.map((r) => (
                <tr key={r.start} className="tabular-nums">
                  <th scope="row" className="whitespace-nowrap px-4 py-2 text-left font-normal text-fg">{r.full}</th>
                  <td className="px-4 py-2 text-right">{usd(r.billed_usd)}</td>
                  <td className="px-4 py-2 text-right">{usd(r.cost_usd)}</td>
                  <td className={cn("px-4 py-2 text-right font-medium", r.revenue_usd < 0 ? "text-bad" : "text-fg")}>{usd(r.revenue_usd)}</td>
                  <td className="px-4 py-2 text-right text-fg-muted">{count(r.requests)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr className="border-t-2 border-line-strong bg-subtle font-semibold tabular-nums text-fg">
                <th scope="row" className="px-4 py-2.5 text-left">Total</th>
                <td className="px-4 py-2.5 text-right">{usd(total.billed)}</td>
                <td className="px-4 py-2.5 text-right">{usd(total.cost)}</td>
                <td className={cn("px-4 py-2.5 text-right", total.revenue < 0 && "text-bad")}>{usd(total.revenue)}</td>
                <td className="px-4 py-2.5 text-right">{count(total.requests)}</td>
              </tr>
            </tfoot>
          </table>
        </div>
      )}
    </Panel>
  );
}
