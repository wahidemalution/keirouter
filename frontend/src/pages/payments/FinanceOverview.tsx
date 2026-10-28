import { Activity, Coins, Percent, Receipt, Wallet, Wrench } from "lucide-react";
import type { FinanceOverview as Overview, FinanceRange } from "../../lib/api";
import { ErrorBanner, Kpi, KpiGrid, LoadingRegion, Skeleton } from "../../components/ui";
import { count, idr, usd } from "./shared";

export const RANGE_LABEL: Record<FinanceRange, string> = {
  today: "Today",
  "7d": "7 days",
  "30d": "30 days",
  all: "All time",
};

export function FinanceOverview({
  overview,
  range,
  loading,
  error,
}: {
  overview?: Overview;
  range: FinanceRange;
  loading: boolean;
  error: string | null;
}) {
  if (loading && !overview) {
    return (
      <LoadingRegion label="Loading finance summary">
        <Skeleton className="h-[188px] w-full rounded-2xl" />
      </LoadingRegion>
    );
  }
  if (!overview) {
    return <ErrorBanner message={`Couldn't load the finance summary. ${error ?? ""}`.trim()} />;
  }

  const o = overview;
  const cost = o.cost_exact_usd + o.cost_estimated_usd;
  const rangeText = RANGE_LABEL[range].toLowerCase();

  return (
    <div className="space-y-2">
      {error && <ErrorBanner message={`The last refresh failed, so the numbers below may be out of date. ${error}`} />}
      <KpiGrid cols={3} label={`Credit, revenue and provider cost, ${rangeText}`}>
        <Kpi
          icon={Wallet}
          label="Credit received"
          value={idr(o.credit_in_idr)}
          hint={`${usd(o.credit_in_usd)} from ${count(o.paid_orders)} orders`}
        />
        <Kpi icon={Coins} label="Unused user credit" value={usd(o.outstanding_credit_usd)} hint="Current balance across all users" />
        <Kpi
          icon={Receipt}
          label="Revenue"
          value={usd(o.revenue_usd)}
          tone={o.revenue_usd < 0 ? "bad" : "section"}
          valueClassName={o.revenue_usd < 0 ? "text-bad" : ""}
          hint={`Markup on ${usd(o.billed_usd)} billed · ${count(o.requests)} requests`}
        />
        <Kpi
          icon={Wrench}
          label="Provider cost"
          value={usd(cost)}
          hint={o.cost_estimated_usd > 0 ? `Upstream price, including ${usd(o.cost_estimated_usd)} estimated` : "Upstream price before markup"}
        />
        <Kpi
          icon={Percent}
          label="Margin"
          value={`${o.margin_pct.toLocaleString("en-US", { maximumFractionDigits: 1 })}%`}
          hint="Revenue divided by provider cost"
        />
        <Kpi icon={Activity} label="Active requests" value={count(o.in_flight)} hint="Running right now" />
      </KpiGrid>
      {o.cost_estimated_usd > 0 && (
        <p className="text-[12px] text-fg-muted">
          Part of the provider cost ({usd(o.cost_estimated_usd)}) is the sell price minus markup, because the upstream price wasn't recorded. Revenue
          and margin use this figure.
        </p>
      )}
    </div>
  );
}
