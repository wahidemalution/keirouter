import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { CheckCircle2, Coins, CreditCard, Eye, EyeOff, KeyRound, XCircle } from "lucide-react";
import { getPortalTokenStatus, revealPortalTokenKey } from "../../lib/api";
import { Badge, EmptyState, ErrorCard, Spinner } from "../../components/ui";
import { fmtInt } from "../../lib/format";
import { portal } from "../../lib/portalRoutes";
import { IdField, PageHeader, PanelCard } from "./components";
import {
  AllowedModelsCard, LINK_BUTTON, RequireCreditKey, TOKEN_ORDERS_QK, TOKEN_STATUS_QK, formatDate,
} from "./tokenPackage";

const CONFIRM_POLL_MS = 60_000;
const TITLE = "Token Package";

export function PortalTokensPage() {
  return (
    <RequireCreditKey title={TITLE}>
      <TokenOverview />
    </RequireCreditKey>
  );
}

function TokenOverview() {
  const qc = useQueryClient();
  const [params] = useSearchParams();
  const returnStatus = params.get("status");

  // The webhook can land a few seconds after the return, so poll briefly.
  const [pollExpired, setPollExpired] = useState(false);
  useEffect(() => {
    if (returnStatus !== "success") return;
    const t = setTimeout(() => setPollExpired(true), CONFIRM_POLL_MS);
    return () => clearTimeout(t);
  }, [returnStatus]);
  const polling = returnStatus === "success" && !pollExpired ? 4000 : false;

  const tokenStatus = useQuery({
    queryKey: TOKEN_STATUS_QK,
    queryFn: getPortalTokenStatus,
    retry: false,
    refetchInterval: polling,
  });
  useEffect(() => {
    if (returnStatus === "success" || returnStatus === "cancel") {
      qc.invalidateQueries({ queryKey: TOKEN_STATUS_QK });
      qc.invalidateQueries({ queryKey: TOKEN_ORDERS_QK });
    }
  }, [returnStatus, qc]);

  const [shown, setShown] = useState(false);
  const reveal = useMutation({ mutationFn: revealPortalTokenKey, onSuccess: () => setShown(true) });

  if (tokenStatus.isLoading) return <Spinner />;
  if (tokenStatus.isError || !tokenStatus.data) {
    return <ErrorCard message={tokenStatus.error instanceof Error ? tokenStatus.error.message : "Failed to load token package"} />;
  }

  const st = tokenStatus.data;
  const remaining = Math.max(0, st.limit_tokens - st.used_tokens);

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
        title={TITLE}
        subtitle="A separate key with its own token balance, apart from your AI PAYG credit."
        action={
          st.exists && !st.disabled ? (
            <Link to={portal("/token/buy")} className={LINK_BUTTON}>
              <CreditCard aria-hidden="true" />
              Buy package
            </Link>
          ) : undefined
        }
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

      {!st.exists ? (
        <>
          <EmptyState
            variant="card"
            icon={Coins}
            title="No token package yet"
            hint="Buy a package and your token key appears here, with its balance and expiry."
            action={
              <Link to={portal("/token/buy")} className={LINK_BUTTON}>
                <CreditCard aria-hidden="true" />
                Buy a package
              </Link>
            }
          />
          <AllowedModelsCard models={st.models} />
        </>
      ) : (
        <PanelCard
          icon={KeyRound}
          title="Your token key"
          action={
            st.disabled ? (
              <Badge tone="danger">Disabled</Badge>
            ) : st.active ? (
              <Badge tone="success">Active</Badge>
            ) : (
              <Badge tone="danger">Expired</Badge>
            )
          }
        >
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
                  <p className="mt-0.5 text-[22px] font-semibold tabular-nums tracking-[-0.015em] text-fg">{fmtInt.format(remaining)}</p>
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
                  at expiry are forfeited.{" "}
                  {st.disabled ? (
                    "Buy a new package to use the key again."
                  ) : (
                    <>
                      <Link to={portal("/token/buy")} className="font-medium text-link hover:underline">
                        Buy a new package
                      </Link>{" "}
                      to use the key again.
                    </>
                  )}
                </p>
              </div>
            )}
          </div>
          <div className="space-y-2 border-t border-line p-4 sm:p-5">
            <p className="text-[12.5px] text-fg-muted">Base URL</p>
            <IdField value={`${window.location.origin}/v1`} />
            <p className="pt-2 text-[12.5px] text-fg-muted">API key</p>
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
        </PanelCard>
      )}

      {st.exists && <AllowedModelsCard models={st.models} />}
    </div>
  );
}
