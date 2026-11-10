import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { CheckCircle2, Coins, CreditCard, Eye, EyeOff, KeyRound, Receipt, XCircle } from "lucide-react";
import {
  createPortalTokenOrder,
  fetchPortalStatus,
  getPortalTokenPackages,
  getPortalTokenStatus,
  listPortalTokenOrders,
  PortalAPIError,
  revealPortalTokenKey,
  type PortalTokenOrder,
  type TokenPackageOffer,
} from "../../lib/api";
import { Badge, Button, EmptyState, ErrorCard, Spinner } from "../../components/ui";
import { TurnstileWidget, useTurnstileSiteKey } from "../../components/TurnstileWidget";
import { useToast } from "../../components/Toast";
import { formatTokens } from "../../lib/format";
import { portal } from "../../lib/portalRoutes";
import { safeHttpUrl } from "../../lib/safeUrl";
import { IdField, PageHeader, PanelCard, TD, TH, THEAD, formatDateTime } from "./components";

const STATUS_QK = ["portal-token-status"] as const;
const PACKAGES_QK = ["portal-token-packages"] as const;
const ORDERS_QK = ["portal-token-orders"] as const;
const CONFIRM_POLL_MS = 60_000;

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric" }).format(d);
}

function formatIDR(n: number): string {
  return new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 }).format(n);
}

const ORDER_TONE: Record<PortalTokenOrder["status"], "success" | "warning" | "danger" | "neutral"> = {
  completed: "success",
  manual: "success",
  pending: "warning",
  failed: "danger",
  expired: "neutral",
  cancelled: "neutral",
};

/** paid_at arrives as a zero time until the order is paid. */
function paidAtLabel(v?: string | null): string {
  if (!v || new Date(v).getFullYear() < 2000) return "—";
  return formatDateTime(v);
}

/**
 * Discount of a tokens package against the per-token price of the smallest
 * tokens package; 0 for that package itself and for days packages.
 */
function discountPct(p: TokenPackageOffer, base?: TokenPackageOffer): number {
  if (p.kind !== "tokens" || !base || p.id === base.id || p.tokens <= 0 || base.tokens <= 0) return 0;
  const pct = Math.round((1 - p.price_idr / p.tokens / (base.price_idr / base.tokens)) * 100);
  return pct > 0 ? pct : 0;
}

function PackageCard({
  pkg, selected, discount, onSelect,
}: {
  pkg: TokenPackageOffer;
  selected: boolean;
  discount: number;
  onSelect: () => void;
}) {
  const isDays = pkg.kind === "days";
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={`flex min-w-0 flex-col items-start gap-1 rounded-xl border p-4 text-left transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500 ${
        selected
          ? "border-accent-500 bg-accent-500/10 ring-1 ring-inset ring-accent-500"
          : "border-line bg-surface hover:border-line-strong hover:bg-hover"
      }`}
    >
      <span className="flex w-full flex-wrap items-center gap-2">
        <span className="text-[14px] font-semibold text-fg">{pkg.name}</span>
        {pkg.featured && <Badge tone="accent">Most popular</Badge>}
        {discount > 0 && <Badge tone="success">Save {discount}%</Badge>}
      </span>
      <span className="text-[22px] font-semibold tabular-nums tracking-[-0.015em] text-fg">
        {isDays ? `+${pkg.days} days` : `${formatTokens(pkg.tokens)} tokens`}
      </span>
      <span className="text-[13px] tabular-nums text-fg-muted">{formatIDR(pkg.price_idr)}</span>
      {isDays && <span className="text-[12px] text-fg-muted">Extends your current package</span>}
    </button>
  );
}

export function PortalTokensPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [params] = useSearchParams();
  const returnStatus = params.get("status");

  const { data: portalStatus, isLoading: portalLoading } = useQuery({
    queryKey: ["portal-status"],
    queryFn: fetchPortalStatus,
    retry: false,
  });
  const hasKey = !!portalStatus?.has_key;

  // The webhook can land a few seconds after the return, so poll briefly.
  const [pollExpired, setPollExpired] = useState(false);
  useEffect(() => {
    if (returnStatus !== "success") return;
    const t = setTimeout(() => setPollExpired(true), CONFIRM_POLL_MS);
    return () => clearTimeout(t);
  }, [returnStatus]);
  const polling = returnStatus === "success" && !pollExpired ? 4000 : false;

  const tokenStatus = useQuery({
    queryKey: STATUS_QK,
    queryFn: getPortalTokenStatus,
    retry: false,
    enabled: hasKey,
    refetchInterval: polling,
  });
  const offers = useQuery({ queryKey: PACKAGES_QK, queryFn: getPortalTokenPackages, retry: false });
  const orders = useQuery({
    queryKey: ORDERS_QK,
    queryFn: listPortalTokenOrders,
    retry: false,
    enabled: hasKey,
    refetchInterval: polling,
  });

  useEffect(() => {
    if (returnStatus === "success" || returnStatus === "cancel") {
      qc.invalidateQueries({ queryKey: STATUS_QK });
      qc.invalidateQueries({ queryKey: ORDERS_QK });
    }
  }, [returnStatus, qc]);

  const [pickedId, setPickedId] = useState<string | null>(null);
  const [noActive, setNoActive] = useState(false);
  const siteKey = useTurnstileSiteKey();
  const [turnstileToken, setTurnstileToken] = useState("");
  const [resetSignal, setResetSignal] = useState(0);

  const [shown, setShown] = useState(false);
  const reveal = useMutation({ mutationFn: revealPortalTokenKey, onSuccess: () => setShown(true) });

  const buy = useMutation({
    mutationFn: (packageId: string) =>
      createPortalTokenOrder({
        package_id: packageId,
        idempotency_key: crypto.randomUUID(),
        turnstile_token: turnstileToken || undefined,
      }),
    onSuccess: (o) => {
      setTurnstileToken("");
      setResetSignal((n) => n + 1);
      const link = safeHttpUrl(o.payment_link_url);
      if (link) {
        window.location.href = link;
      } else {
        toast.error("Invalid payment link");
        buy.reset();
      }
    },
    onError: (e: Error) => {
      setTurnstileToken("");
      setResetSignal((n) => n + 1);
      const code = e instanceof PortalAPIError ? e.code : undefined;
      if (code === "no_active_package") {
        setNoActive(true);
        setPickedId(null);
        qc.invalidateQueries({ queryKey: STATUS_QK });
        return;
      }
      if (code === "token_key_disabled") {
        setPickedId(null);
        qc.invalidateQueries({ queryKey: STATUS_QK });
        return;
      }
      toast.error("Payment failed", e.message);
    },
  });

  // Back from the payment page restores this page from the bfcache with the
  // buy mutation still in its success state.
  const { reset: resetBuy } = buy;
  useEffect(() => {
    const onShow = (e: PageTransitionEvent) => {
      if (!e.persisted) return;
      resetBuy();
      setResetSignal((n) => n + 1);
    };
    window.addEventListener("pageshow", onShow);
    return () => window.removeEventListener("pageshow", onShow);
  }, [resetBuy]);

  if (portalLoading) return <Spinner />;
  if (!portalStatus) return <ErrorCard message="Failed to load portal status" />;

  if (!hasKey) {
    return (
      <div className="space-y-6">
        <PageHeader icon={Coins} title="Token Packages" />
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

  if (tokenStatus.isLoading || offers.isLoading) return <Spinner />;
  if (tokenStatus.isError || !tokenStatus.data) {
    return <ErrorCard message={tokenStatus.error instanceof Error ? tokenStatus.error.message : "Failed to load token package"} />;
  }
  if (offers.isError || !offers.data) {
    return <ErrorCard message={offers.error instanceof Error ? offers.error.message : "Failed to load packages"} />;
  }

  const st = tokenStatus.data;
  const onSale = offers.data.on_sale;
  const tokenPkgs = offers.data.packages.filter((p) => p.kind === "tokens");
  const daysPkgs = st.active ? offers.data.packages.filter((p) => p.kind === "days") : [];
  const base = tokenPkgs.reduce<TokenPackageOffer | undefined>((a, p) => (!a || p.tokens < a.tokens ? p : a), undefined);
  const picked = [...tokenPkgs, ...daysPkgs].find((p) => p.id === pickedId) ?? null;
  const remaining = Math.max(0, st.limit_tokens - st.used_tokens);
  const orderList = orders.data ?? [];

  const toggleKey = () => {
    if (shown) {
      setShown(false);
      reveal.reset();
      return;
    }
    reveal.mutate();
  };
  const revealedKey = shown ? reveal.data : undefined;
  const keyToggle = (
    <button
      type="button"
      onClick={toggleKey}
      disabled={reveal.isPending}
      className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-fg-muted transition-colors hover:bg-hover hover:text-fg disabled:opacity-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500"
      title={shown ? "Hide" : "Show"}
      aria-label={shown ? "Hide token key" : "Show token key"}
    >
      {shown ? <EyeOff className="h-4 w-4" aria-hidden="true" /> : <Eye className="h-4 w-4" aria-hidden="true" />}
    </button>
  );

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Coins}
        title="Token Packages"
        subtitle="Buy tokens for a separate key that works with the models listed below."
      />

      {returnStatus === "success" && (
        <div role="status" className="flex items-start gap-3 rounded-2xl border border-ok/30 bg-ok/10 px-4 py-3">
          <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-ok" strokeWidth={1.75} aria-hidden="true" />
          <p className="text-[13px] text-fg">Payment received. Your package updates here as soon as it is confirmed.</p>
        </div>
      )}
      {returnStatus === "cancel" && (
        <div role="status" className="flex items-start gap-3 rounded-2xl border border-line bg-subtle px-4 py-3">
          <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-fg-muted" strokeWidth={1.75} aria-hidden="true" />
          <p className="text-[13px] text-fg-muted">Payment cancelled. No charge was made.</p>
        </div>
      )}

      <PanelCard
        icon={KeyRound}
        title="Your token key"
        action={
          !st.exists ? (
            <Badge>No package</Badge>
          ) : st.disabled ? (
            <Badge tone="danger">Disabled</Badge>
          ) : st.active ? (
            <Badge tone="success">Active</Badge>
          ) : (
            <Badge tone="danger">Expired</Badge>
          )
        }
      >
        {!st.exists ? (
          <p className="px-4 py-4 text-[13px] text-fg-muted sm:px-5">
            You have no token package yet. Buy one below and your key appears here.
          </p>
        ) : (
          <>
            {st.disabled && (
              <p role="status" className="mx-4 mt-4 rounded-lg border border-bad/30 bg-bad/10 px-3 py-2.5 text-[12.5px] text-fg sm:mx-5 sm:mt-5">
                An admin disabled this key. Requests with it are refused and you cannot buy packages for it. Ask the admin to
                enable it again.
              </p>
            )}
            <div className="grid gap-4 p-4 sm:grid-cols-2 sm:p-5">
              {st.active ? (
                <>
                  <div className="min-w-0">
                    <p className="text-[12.5px] text-fg-muted">Tokens remaining</p>
                    <p className="mt-0.5 text-[22px] font-semibold tabular-nums tracking-[-0.015em] text-fg">{formatTokens(remaining)}</p>
                  </div>
                  <div className="min-w-0">
                    <p className="text-[12.5px] text-fg-muted">Expires</p>
                    <p className="mt-0.5 text-[22px] font-semibold tabular-nums tracking-[-0.015em] text-fg">{formatDate(st.expires_at)}</p>
                  </div>
                </>
              ) : (
                <div className="min-w-0 sm:col-span-2">
                  <p className="text-[13px] text-fg">
                    This package expired on <span className="tabular-nums">{formatDate(st.expires_at)}</span>. Tokens left
                    at expiry are forfeited. Buy a new package to use the key again.
                  </p>
                </div>
              )}
            </div>
            <div className="space-y-2 border-t border-line p-4 sm:p-5">
              {revealedKey ? (
                <IdField value={revealedKey}>{keyToggle}</IdField>
              ) : (
                <div className="flex items-center gap-1 rounded-lg border border-line bg-subtle py-1 pl-3 pr-1">
                  <code className="min-w-0 flex-1 break-all py-1 font-mono text-[13px] text-fg">{st.key_display}</code>
                  {keyToggle}
                </div>
              )}
              {reveal.isError && <p role="alert" className="text-[12.5px] text-bad">{(reveal.error as Error).message}</p>}
            </div>
          </>
        )}
        {st.models.length > 0 && (
          <div className="border-t border-line p-4 sm:p-5">
            <p className="text-[12.5px] text-fg-muted">This key only works with these models</p>
            <ul className="mt-2 flex flex-wrap gap-1.5">
              {st.models.map((m) => (
                <li key={m} className="rounded-md border border-line bg-subtle px-2 py-0.5 font-mono text-[12px] text-fg">{m}</li>
              ))}
            </ul>
          </div>
        )}
      </PanelCard>

      <PanelCard
        icon={CreditCard}
        title="Buy tokens"
        subtitle={st.active && !st.disabled ? "Tokens extend the limit. Days packages extend the expiry." : undefined}
      >
        {st.disabled ? (
          <EmptyState title="Buying is paused while your key is disabled" hint="Once the admin enables the key, you can buy here again." />
        ) : !onSale ? (
          <EmptyState title="Token packages are not available right now" hint="Check back later." />
        ) : tokenPkgs.length === 0 && daysPkgs.length === 0 ? (
          <EmptyState title="No packages for sale" />
        ) : (
          <div className="grid grid-cols-1 lg:grid-cols-[1fr_320px]">
            <div className="space-y-5 p-4 sm:p-5">
              {noActive && (
                <p role="alert" className="rounded-lg border border-warn/30 bg-warn/10 px-3 py-2.5 text-[12.5px] text-fg">
                  A days package needs an active token package. Buy a tokens package first.
                </p>
              )}
              {tokenPkgs.length > 0 && (
                <fieldset className="space-y-2">
                  <legend className="mb-2 text-[12.5px] font-medium text-fg">Tokens</legend>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                    {tokenPkgs.map((p) => (
                      <PackageCard
                        key={p.id}
                        pkg={p}
                        selected={pickedId === p.id}
                        discount={discountPct(p, base)}
                        onSelect={() => setPickedId(p.id)}
                      />
                    ))}
                  </div>
                </fieldset>
              )}
              {daysPkgs.length > 0 && (
                <fieldset className="space-y-2">
                  <legend className="mb-2 text-[12.5px] font-medium text-fg">Extra days</legend>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                    {daysPkgs.map((p) => (
                      <PackageCard key={p.id} pkg={p} selected={pickedId === p.id} discount={0} onSelect={() => setPickedId(p.id)} />
                    ))}
                  </div>
                </fieldset>
              )}
            </div>

            <div className="flex flex-col border-t border-line bg-subtle p-4 sm:p-5 lg:border-l lg:border-t-0">
              <dl className="space-y-3">
                <div className="flex items-baseline justify-between gap-3">
                  <dt className="text-[13px] text-fg-muted">Package</dt>
                  <dd className="min-w-0 truncate text-[13px] font-medium text-fg">{picked ? picked.name : "—"}</dd>
                </div>
                <div className="flex items-baseline justify-between gap-3">
                  <dt className="text-[13px] text-fg-muted">You pay</dt>
                  <dd className="text-[18px] font-semibold tabular-nums text-fg">{picked ? formatIDR(picked.price_idr) : "—"}</dd>
                </div>
              </dl>
              <TurnstileWidget
                siteKey={siteKey}
                onToken={setTurnstileToken}
                resetSignal={resetSignal}
                className="mt-4 flex justify-center"
              />
              <Button
                className="mt-4 w-full"
                onClick={() => picked && buy.mutate(picked.id)}
                disabled={!picked || buy.isPending || buy.isSuccess || (!!siteKey && !turnstileToken)}
              >
                <CreditCard aria-hidden="true" />
                {buy.isPending ? "Starting payment…" : "Buy package"}
              </Button>
            </div>
          </div>
        )}
      </PanelCard>

      <PanelCard icon={Receipt} title="Token orders" count={orderList.length}>
        {orders.isError ? (
          <EmptyState title="Could not load token orders" />
        ) : orderList.length === 0 ? (
          <EmptyState title="No token orders yet" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[560px] text-[13px]">
              <thead className={THEAD}>
                <tr>
                  <th scope="col" className={`${TH} text-left`}>Date</th>
                  <th scope="col" className={`${TH} text-left`}>Package</th>
                  <th scope="col" className={`${TH} text-right`}>Amount</th>
                  <th scope="col" className={`${TH} text-left`}>Status</th>
                  <th scope="col" className={`${TH} text-left`}>Paid</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {orderList.map((o) => (
                  <tr key={o.id} className="transition-colors hover:bg-hover">
                    <td className={`${TD} whitespace-nowrap tabular-nums text-fg-muted`}>{formatDateTime(o.created_at)}</td>
                    <td className={`${TD} text-fg`}>{o.package_name || "—"}</td>
                    <td className={`${TD} text-right tabular-nums text-fg`}>{formatIDR(o.amount_idr)}</td>
                    <td className={TD}><Badge tone={ORDER_TONE[o.status] ?? "neutral"}>{o.status}</Badge></td>
                    <td className={`${TD} whitespace-nowrap tabular-nums text-fg-muted`}>{paidAtLabel(o.paid_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </PanelCard>
    </div>
  );
}
