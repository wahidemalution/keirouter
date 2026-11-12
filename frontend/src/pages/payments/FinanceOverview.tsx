import { Activity, Banknote, Coins, Package, Percent, Receipt, Wallet, Wrench } from "lucide-react";
import type { FinanceCustomRange, FinanceOverview as Overview, FinanceRange } from "../../lib/api";
import { ErrorBanner, Kpi, KpiGrid, LoadingRegion, Skeleton } from "../../components/ui";
import { count, idr, usd } from "./shared";

export const RANGE_LABEL: Record<FinanceRange, string> = {
  today: "Today",
  "7d": "7 days",
  "30d": "30 days",
  all: "All time",
  custom: "Custom",
};

// A YYYY-MM-DD is already a server-zone day, so format it as-is in UTC
// rather than shifting it through the browser's zone.
function dayText(day: string): string {
  return new Date(`${day}T00:00:00Z`).toLocaleDateString("en-US", { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}

/** The range in words for headings and labels: "last 30 days", "Oct 1, 2026 to Oct 15, 2026". */
export function rangeText(range: FinanceRange, custom: FinanceCustomRange): string {
  if (range === "custom") {
    const from = dayText(custom.from);
    return custom.from === custom.to ? from : `${from} to ${dayText(custom.to)}`;
  }
  return range === "today" ? "today" : range === "all" ? "all time" : `last ${RANGE_LABEL[range].toLowerCase()}`;
}

export function FinanceOverview({
  overview,
  label,
  loading,
  error,
}: {
  overview?: Overview;
  /** The range in words, from rangeText. */
  label: string;
  loading: boolean;
  error: string | null;
}) {
  if (loading && !overview) {
    return (
      <LoadingRegion label="Loading finance summary">
        <Skeleton className="h-[300px] w-full rounded-2xl" />
      </LoadingRegion>
    );
  }
  if (!overview) {
    return <ErrorBanner message={`Couldn't load the finance summary. ${error ?? ""}`.trim()} />;
  }

  const o = overview;
  const cost = o.cost_exact_usd + o.cost_estimated_usd;

  return (
    <div className="space-y-3">
      {error && <ErrorBanner message={`The last refresh failed, so the numbers below may be out of date. ${error}`} />}
      <KpiGrid cols={3} label={`Money received from users, ${label}`}>
        <Kpi
          icon={Banknote}
          label="Total received"
          value={idr(o.credit_in_idr + o.token_in_idr)}
          hint={`${count(o.paid_orders + o.token_orders)} paid orders`}
        />
        <Kpi
          icon={Wallet}
          label="PAYG credit"
          value={idr(o.credit_in_idr)}
          hint={`${usd(o.credit_in_usd)} credit from ${count(o.paid_orders)} orders`}
        />
        <Kpi icon={Package} label="Token packages" value={idr(o.token_in_idr)} hint={`${count(o.token_orders)} orders`} />
      </KpiGrid>
      <KpiGrid cols={5} label={`Gateway revenue and provider cost, ${label}`}>
        <Kpi icon={Coins} label="Unused user credit" value={usd(o.outstanding_credit_usd)} hint="PAYG balance across all users right now" />
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
