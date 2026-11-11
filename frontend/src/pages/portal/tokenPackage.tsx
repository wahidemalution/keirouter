import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Coins, Layers } from "lucide-react";
import { fetchPortalStatus, type PortalTokenOrder, type TokenPackageOffer } from "../../lib/api";
import { EmptyState, ErrorCard, Spinner } from "../../components/ui";
import { portal } from "../../lib/portalRoutes";
import { PageHeader, PanelCard, formatDateTime } from "./components";

export const TOKEN_STATUS_QK = ["portal-token-status"] as const;
export const TOKEN_PACKAGES_QK = ["portal-token-packages"] as const;
export const TOKEN_ORDERS_QK = ["portal-token-orders"] as const;

/** Primary-button look for a router link. */
export const LINK_BUTTON =
  "inline-flex min-h-9 items-center justify-center gap-1.5 rounded-lg border border-transparent bg-action px-3 py-1.5 text-[13px] font-medium text-action-fg shadow-[var(--shadow-card)] transition-colors hover:bg-action-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500 focus-visible:ring-offset-2 focus-visible:ring-offset-canvas [@media(pointer:coarse)]:min-h-11 [&_svg]:size-4 [&_svg]:shrink-0";

export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric" }).format(d);
}

export function formatIDR(n: number): string {
  return new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 }).format(n);
}

export const ORDER_TONE: Record<PortalTokenOrder["status"], "success" | "warning" | "danger" | "neutral"> = {
  completed: "success",
  manual: "success",
  pending: "warning",
  failed: "danger",
  expired: "neutral",
  cancelled: "neutral",
};

/** paid_at arrives as a zero time until the order is paid. */
export function paidAtLabel(v?: string | null): string {
  if (!v || new Date(v).getFullYear() < 2000) return "—";
  return formatDateTime(v);
}

/**
 * Discount of a tokens package against the per-token price of the smallest
 * tokens package; 0 for that package itself and for days packages.
 */
export function discountPct(p: TokenPackageOffer, base?: TokenPackageOffer): number {
  if (p.kind !== "tokens" || !base || p.id === base.id || p.tokens <= 0 || base.tokens <= 0) return 0;
  const pct = Math.round((1 - p.price_idr / p.tokens / (base.price_idr / base.tokens)) * 100);
  return pct > 0 ? pct : 0;
}

/**
 * Token packages are bought by a portal user with a credit key, so the token
 * pages render their content only once that key exists.
 */
export function RequireCreditKey({ title, children }: { title: string; children: ReactNode }) {
  const { data, isLoading } = useQuery({ queryKey: ["portal-status"], queryFn: fetchPortalStatus, retry: false });

  if (isLoading) return <Spinner />;
  if (!data) return <ErrorCard message="Failed to load portal status" />;
  if (!data.has_key) {
    return (
      <div className="space-y-6">
        <PageHeader icon={Coins} title={title} />
        <EmptyState
          variant="card"
          icon={Coins}
          title="No API key yet"
          hint="Create an API key before you buy a token package."
          action={<Link to={portal("/key")} className="text-[13px] font-medium text-link hover:underline">Create an API key</Link>}
        />
      </div>
    );
  }
  return <>{children}</>;
}

export function AllowedModelsList({ models }: { models: string[] }) {
  return (
    <ul className="mt-2 flex flex-wrap gap-1.5">
      {models.map((m) => (
        <li key={m} className="rounded-md border border-line bg-subtle px-2 py-0.5 font-mono text-[12px] text-fg">{m}</li>
      ))}
    </ul>
  );
}

/** Allowed models on their own, for pages without the token key card. */
export function AllowedModelsCard({ models }: { models: string[] }) {
  if (models.length === 0) return null;
  return (
    <PanelCard icon={Layers} title="Models">
      <div className="p-4 sm:p-5">
        <p className="text-[12.5px] text-fg-muted">The token key only works with these models</p>
        <AllowedModelsList models={models} />
      </div>
    </PanelCard>
  );
}
