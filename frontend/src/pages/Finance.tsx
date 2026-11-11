import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { Coins, Network, ReceiptText, Server } from "lucide-react";
import { api, type FinanceCustomRange, type FinanceRange } from "../lib/api";
import { dashboard } from "../lib/dashboardRoutes";
import { PageHeader } from "../components/Layout";
import { SegmentedControl, Skeleton, TabBar, TabPanel } from "../components/ui";
import { FinanceOverview, RANGE_LABEL, rangeText } from "./payments/FinanceOverview";
import { FinanceSeriesTable } from "./payments/FinanceSeriesTable";
import { CustomRangeForm } from "./payments/CustomRangeForm";
import { PortalOrders } from "./payments/PortalOrders";
import { CostsTab, FinanceTab as HostingFinance } from "./hosting/HostingFinance";
import { QK, REFETCH_MS, errorText, todayISO } from "./payments/shared";
import { serverDateISO } from "../lib/serverTime";

const RANGES = (Object.keys(RANGE_LABEL) as FinanceRange[]).map((value) => ({ value, label: RANGE_LABEL[value] }));
const RANGE_STORAGE_KEY = "kr.payments.range";
const CUSTOM_STORAGE_KEY = "kr.payments.customRange";
const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;

// /finance is the AI gateway tab; the others have their own URL so the
// sidebar, bookmarks and links can open them directly.
const TABS = ["gateway", "hosting", "costs", "orders"] as const;
type Tab = (typeof TABS)[number];
const TABS_ID = "finance-tabs";

const DESCRIPTION: Record<Tab, string> = {
  gateway: "AI gateway money: credit sold, revenue and provider cost.",
  hosting: "Hosting money: revenue from paid orders against what the servers cost.",
  costs: "VPS rental or purchase, domains and other hosting costs. Hosting profit is counted against these.",
  orders: "Every portal payment, for credit top-ups and hosting. Approve one here if the gateway missed it.",
};

function initialRange(): FinanceRange {
  try {
    const saved = localStorage.getItem(RANGE_STORAGE_KEY);
    if (RANGES.some((r) => r.value === saved)) return saved as FinanceRange;
  } catch {
    // storage unavailable: use the default
  }
  return "30d";
}

// The saved custom range, or the last 30 days when nothing valid is saved.
function initialCustom(): FinanceCustomRange {
  try {
    const saved = JSON.parse(localStorage.getItem(CUSTOM_STORAGE_KEY) ?? "null") as Partial<FinanceCustomRange> | null;
    if (saved && DAY_RE.test(saved.from ?? "") && DAY_RE.test(saved.to ?? "") && saved.from! <= saved.to!) {
      return { from: saved.from!, to: saved.to! };
    }
  } catch {
    // storage unavailable or malformed: use the default
  }
  return { from: serverDateISO(Date.now() - 29 * 86_400_000), to: todayISO() };
}

function save(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // ignore
  }
}

export function FinancePage() {
  const params = useParams();
  const tab = (params.tab ?? "gateway") as Tab;
  const navigate = useNavigate();
  const [range, setRangeState] = useState<FinanceRange>(initialRange);
  const setRange = (r: FinanceRange) => {
    setRangeState(r);
    save(RANGE_STORAGE_KEY, r);
  };
  const [custom, setCustomState] = useState<FinanceCustomRange>(initialCustom);
  const setCustom = (c: FinanceCustomRange) => {
    setCustomState(c);
    save(CUSTOM_STORAGE_KEY, JSON.stringify(c));
  };

  const finance = useQuery({
    queryKey: range === "custom" ? [...QK.finance, range, custom.from, custom.to] : [...QK.finance, range],
    queryFn: () => api.financeReport(range, custom),
    refetchInterval: REFETCH_MS,
    enabled: tab === "gateway",
  });

  if (params.tab === "gateway" || !TABS.includes(tab)) return <Navigate to={dashboard("/finance")} replace />;

  const financeError = finance.isError ? errorText(finance.error) : null;
  const go = (t: Tab) => navigate(dashboard(t === "gateway" ? "/finance" : `/finance/${t}`));

  return (
    <div className="space-y-6">
      <PageHeader
        title="Finance"
        description={DESCRIPTION[tab]}
        action={tab === "gateway" ? <SegmentedControl value={range} onChange={setRange} options={RANGES} label="Time range" /> : undefined}
      />
      <TabBar
        idPrefix={TABS_ID}
        label="Finance sections"
        active={tab}
        onChange={go}
        tabs={[
          { value: "gateway", label: "AI gateway", icon: Network },
          { value: "hosting", label: "Hosting", icon: Server },
          { value: "costs", label: "Operating costs", icon: Coins },
          { value: "orders", label: "Orders", icon: ReceiptText },
        ]}
      />
      <TabPanel idPrefix={TABS_ID} value={tab} className="space-y-6">
        {tab === "gateway" ? (
          <>
            {range === "custom" && <CustomRangeForm key={`${custom.from}:${custom.to}`} value={custom} onApply={setCustom} />}
            <FinanceOverview overview={finance.data?.overview} label={rangeText(range, custom)} loading={finance.isLoading} error={financeError} />
            {finance.data ? (
              <FinanceSeriesTable series={finance.data.series} label={rangeText(range, custom)} />
            ) : finance.isLoading ? (
              <Skeleton className="h-[372px] w-full rounded-2xl" />
            ) : null}
          </>
        ) : tab === "hosting" ? (
          <HostingFinance />
        ) : tab === "costs" ? (
          <CostsTab />
        ) : (
          <PortalOrders />
        )}
      </TabPanel>
    </div>
  );
}
