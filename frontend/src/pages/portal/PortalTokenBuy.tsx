import { useEffect, useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, CreditCard, Receipt, ShoppingCart } from "lucide-react";
import {
  createPortalTokenOrder,
  getPortalTokenPackages,
  getPortalTokenStatus,
  listPortalTokenOrders,
  PortalAPIError,
  type PortalTokenOrder,
  type TokenPackageOffer,
} from "../../lib/api";
import { Badge, Button, EmptyState, ErrorCard, Spinner } from "../../components/ui";
import { TurnstileWidget, useTurnstileSiteKey } from "../../components/TurnstileWidget";
import { useToast } from "../../components/Toast";
import { formatTokens } from "../../lib/format";
import { safeHttpUrl } from "../../lib/safeUrl";
import { PageHeader, PanelCard, TD, TH, THEAD, formatDateTime } from "./components";
import {
  ORDER_TONE, RequireCreditKey, TOKEN_ORDERS_QK, TOKEN_PACKAGES_QK, TOKEN_STATUS_QK, discountPct, formatIDR, paidAtLabel,
} from "./tokenPackage";

const TITLE = "Buy Package";

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
        {pkg.featured && <Badge tone="accent">Recommended</Badge>}
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

export function PortalTokenBuyPage() {
  return (
    <RequireCreditKey title={TITLE}>
      <TokenBuy />
    </RequireCreditKey>
  );
}

function TokenBuy() {
  const qc = useQueryClient();
  const toast = useToast();

  const tokenStatus = useQuery({ queryKey: TOKEN_STATUS_QK, queryFn: getPortalTokenStatus, retry: false });
  const offers = useQuery({ queryKey: TOKEN_PACKAGES_QK, queryFn: getPortalTokenPackages, retry: false });
  const orders = useQuery({ queryKey: TOKEN_ORDERS_QK, queryFn: listPortalTokenOrders, retry: false });

  const [pickedId, setPickedId] = useState<string | null>(null);
  const [noActive, setNoActive] = useState(false);
  const siteKey = useTurnstileSiteKey();
  const [turnstileToken, setTurnstileToken] = useState("");
  const [resetSignal, setResetSignal] = useState(0);
  const termsId = useId();
  const [accepted, setAccepted] = useState(false);
  const [termsNotice, setTermsNotice] = useState<string | null>(null);

  const buy = useMutation({
    mutationFn: (packageId: string) =>
      createPortalTokenOrder({
        package_id: packageId,
        terms_version: offers.data?.terms_version ?? "",
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
        qc.invalidateQueries({ queryKey: TOKEN_STATUS_QK });
        return;
      }
      if (code === "terms_not_accepted") {
        setAccepted(false);
        setTermsNotice("The token package terms were updated. Read them again, then tick the box to continue.");
        qc.invalidateQueries({ queryKey: TOKEN_PACKAGES_QK });
        return;
      }
      if (code === "token_key_disabled") {
        setPickedId(null);
        qc.invalidateQueries({ queryKey: TOKEN_STATUS_QK });
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

  return (
    <div className="space-y-6">
      <PageHeader
        icon={ShoppingCart}
        title={TITLE}
        subtitle="Tokens go to your token key, separate from your AI PAYG credit."
      />

      <PanelCard
        icon={CreditCard}
        title="Buy tokens"
        subtitle={st.active && !st.disabled ? "Tokens extend the limit. Days packages extend the expiry." : undefined}
      >
        {st.disabled ? (
          <EmptyState title="Buying is paused while your key is disabled" hint="Once the admin enables the key, you can buy here again." />
        ) : tokenPkgs.length === 0 && daysPkgs.length === 0 ? (
          onSale ? (
            <EmptyState title="No packages for sale" />
          ) : (
            <EmptyState title="Token packages are not available right now" hint="Check back later." />
          )
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
              {!onSale && (
                <p role="status" className="mb-4 rounded-lg border border-line bg-surface px-3 py-2.5 text-[12.5px] text-fg">
                  Payment is not available yet, so packages cannot be bought right now.
                </p>
              )}
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
              <div className="mt-4 rounded-lg border border-line bg-surface px-3 py-2.5">
                <label htmlFor={termsId} className="flex cursor-pointer items-start gap-2.5 text-[13px] text-fg">
                  <input
                    id={termsId}
                    type="checkbox"
                    className="mt-0.5 h-4 w-4 shrink-0 rounded border-input accent-accent-500"
                    checked={accepted}
                    disabled={!onSale}
                    onChange={(e) => {
                      setAccepted(e.target.checked);
                      if (e.target.checked) setTermsNotice(null);
                    }}
                  />
                  <span>
                    I have read and agree to the{" "}
                    <a href="/ketentuan#paket-token" target="_blank" rel="noopener noreferrer" className="font-medium text-link hover:underline">
                      Paket Token terms
                    </a>
                    .
                  </span>
                </label>
                <p className="mt-1.5 pl-[26px] text-[12px] text-fg-muted">
                  Tokens left when the package expires are forfeited.{offers.data.terms_version ? ` Version ${offers.data.terms_version}.` : ""}
                </p>
              </div>
              {termsNotice && (
                <div role="alert" className="mt-3 flex items-start gap-2.5 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2.5">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warn" strokeWidth={1.75} aria-hidden="true" />
                  <p className="text-[12.5px] text-fg">{termsNotice}</p>
                </div>
              )}
              {onSale && (
                <TurnstileWidget
                  siteKey={siteKey}
                  onToken={setTurnstileToken}
                  resetSignal={resetSignal}
                  className="mt-4 flex justify-center"
                />
              )}
              <Button
                className="mt-4 w-full"
                onClick={() => picked && buy.mutate(picked.id)}
                disabled={!onSale || !picked || !accepted || buy.isPending || buy.isSuccess || (!!siteKey && !turnstileToken)}
              >
                <CreditCard aria-hidden="true" />
                {buy.isPending ? "Starting payment…" : "Buy package"}
              </Button>
            </div>
          </div>
        )}
      </PanelCard>

      <TokenOrdersCard isError={orders.isError} orders={orders.data ?? []} />
    </div>
  );
}

function TokenOrdersCard({ isError, orders }: { isError: boolean; orders: PortalTokenOrder[] }) {
  return (
    <PanelCard icon={Receipt} title="Token orders" count={orders.length}>
      {isError ? (
        <EmptyState title="Could not load token orders" />
      ) : orders.length === 0 ? (
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
              {orders.map((o) => (
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
  );
}
