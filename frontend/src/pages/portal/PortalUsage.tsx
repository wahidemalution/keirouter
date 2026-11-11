import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { BarChart3 } from "lucide-react";
import { fetchPortalStatus, fetchPortalTokenUsage, fetchPortalUsage, getPortalTokenStatus, type KeyUsageData } from "../../lib/api";
import { EmptyState, ErrorCard, Spinner } from "../../components/ui";
import { portal } from "../../lib/portalRoutes";
import {
  DateFilter, InsightsSection, PageHeader, RecentRequestsSection, ShowCostProvider, TrendSection,
} from "./components";
import { TOKEN_STATUS_QK } from "./tokenPackage";

/** Usage report of one key; the page around it decides which key and when. */
function UsageReport({
  title, subtitle, queryKey, fetchUsage, showCost = true,
}: {
  title: string;
  subtitle: string;
  queryKey: string;
  fetchUsage: (days: number) => Promise<KeyUsageData>;
  /** False for token packages, which are not priced in USD. */
  showCost?: boolean;
}) {
  const [days, setDays] = useState(30);
  const usage = useQuery({
    queryKey: [queryKey, days],
    queryFn: () => fetchUsage(days),
  });

  if (usage.isLoading) return <Spinner />;
  if (usage.isError) {
    const msg = usage.error instanceof Error ? usage.error.message : "Failed to load usage";
    return <ErrorCard message={msg} />;
  }

  const data = usage.data;
  if (!data) return <Spinner />;

  const daily = data.daily ?? [];
  const recent = data.recent ?? [];

  return (
    <ShowCostProvider value={showCost}>
      <div className="space-y-6">
        <PageHeader
          icon={BarChart3}
          title={title}
          subtitle={subtitle}
          action={<DateFilter days={days} onChange={setDays} />}
        />

        {daily.length > 0 && <TrendSection daily={daily} days={days} />}

        <InsightsSection d={data} />

        {recent.length > 0 && <RecentRequestsSection recent={recent} days={days} />}
      </div>
    </ShowCostProvider>
  );
}

function NoKeyState({ title, emptyTitle, hint, to, linkLabel }: {
  title: string;
  emptyTitle: string;
  hint: string;
  to: string;
  linkLabel: string;
}) {
  return (
    <div className="space-y-6">
      <PageHeader icon={BarChart3} title={title} />
      <EmptyState
        variant="card"
        icon={BarChart3}
        title={emptyTitle}
        hint={hint}
        action={<Link to={to} className="text-[13px] font-medium text-link hover:underline">{linkLabel}</Link>}
      />
    </div>
  );
}

export function PortalUsagePage() {
  const { data: status, isLoading } = useQuery({
    queryKey: ["portal-status"],
    queryFn: fetchPortalStatus,
    retry: false,
  });

  if (isLoading) return <Spinner />;
  if (!status) return <ErrorCard message="Failed to load portal status" />;

  if (status.has_key === false) {
    return (
      <NoKeyState
        title="Usage"
        emptyTitle="No API key yet"
        hint="Create an API key to start tracking usage."
        to={portal("/key")}
        linkLabel="Create an API key"
      />
    );
  }

  return (
    <UsageReport
      title="Usage"
      subtitle="Daily trend, token mix and every recent request."
      queryKey="portal-usage"
      fetchUsage={fetchPortalUsage}
    />
  );
}

export function PortalTokenUsagePage() {
  const status = useQuery({ queryKey: TOKEN_STATUS_QK, queryFn: getPortalTokenStatus, retry: false });

  if (status.isLoading) return <Spinner />;
  if (status.isError || !status.data) {
    return <ErrorCard message={status.error instanceof Error ? status.error.message : "Failed to load token package"} />;
  }

  if (!status.data.exists) {
    return (
      <NoKeyState
        title="Token usage"
        emptyTitle="No token package yet"
        hint="Usage shows up here once you buy a package and send requests with its key."
        to={portal("/token/buy")}
        linkLabel="Buy a package"
      />
    );
  }

  return (
    <UsageReport
      title="Token usage"
      subtitle="Requests made with your token key."
      queryKey="portal-token-usage"
      fetchUsage={fetchPortalTokenUsage}
      showCost={false}
    />
  );
}
