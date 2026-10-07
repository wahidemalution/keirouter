import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CreditCard, Check } from "lucide-react";
import { api, type PaymentOrderAdmin } from "../lib/api";
import { formatUSD } from "../lib/format";
import { PageHeader } from "../components/Layout";
import { useToast } from "../components/Toast";
import { Card, Button, Badge, Spinner, ErrorBanner, EmptyState, Modal } from "../components/ui";

function formatIDR(n: number): string {
  return new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 }).format(n);
}

const STATUS_TONE: Record<string, "success" | "warning" | "danger" | "neutral"> = {
  completed: "success",
  manual: "success",
  pending: "warning",
  failed: "danger",
  expired: "neutral",
};

export function PaymentsPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [approving, setApproving] = useState<PaymentOrderAdmin | null>(null);
  const [reason, setReason] = useState("");

  const ordersQuery = useQuery({ queryKey: ["payment-orders"], queryFn: api.paymentOrders });
  const summaryQuery = useQuery({ queryKey: ["payment-summary"], queryFn: api.paymentSummary });
  const economicsQuery = useQuery({ queryKey: ["payment-economics"], queryFn: api.paymentEconomics });

  const approve = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => api.approvePaymentOrder(id, reason),
    onSuccess: () => {
      toast.success("Payment approved", "Credit was applied to the user's key.");
      qc.invalidateQueries({ queryKey: ["payment-orders"] });
      qc.invalidateQueries({ queryKey: ["payment-summary"] });
      setApproving(null);
      setReason("");
    },
    onError: (e: Error) => toast.error("Approval failed", e.message),
  });

  const orders = ordersQuery.data?.orders ?? [];
  const summary = summaryQuery.data;
  const econ = economicsQuery.data;

  return (
    <div className="space-y-6">
      <PageHeader title="Payments" description="Payment-gateway top-ups: revenue, history, and manual approval." />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Card className="p-5">
          <p className="text-[11px] font-semibold uppercase tracking-widest text-[var(--text-muted)]">Total received</p>
          <p className="mt-1 text-2xl font-display font-semibold tabular-nums">{summary ? formatIDR(summary.total_idr) : "—"}</p>
        </Card>
        <Card className="p-5">
          <p className="text-[11px] font-semibold uppercase tracking-widest text-[var(--text-muted)]">Credit granted</p>
          <p className="mt-1 text-2xl font-display font-semibold tabular-nums">{summary ? formatUSD(summary.total_credit_usd) : "—"}</p>
        </Card>
        <Card className="p-5">
          <p className="text-[11px] font-semibold uppercase tracking-widest text-[var(--text-muted)]">Pending</p>
          <p className="mt-1 text-2xl font-display font-semibold tabular-nums">{summary?.count_by_status?.pending ?? 0}</p>
        </Card>
      </div>

      <div>
        <h2 className="text-sm font-semibold text-[var(--text)]">Usage economics</h2>
        <p className="mt-0.5 text-[12px] text-[var(--text-muted)]">
          Upstream is your pre-markup cost to providers; profit is what users were charged minus that cost.
        </p>
        <div className="mt-3 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Card className="p-5">
            <p className="text-[11px] font-semibold uppercase tracking-widest text-[var(--text-muted)]">Charged (billed)</p>
            <p className="mt-1 text-2xl font-display font-semibold tabular-nums">{econ ? formatUSD(econ.billed_usd) : "—"}</p>
          </Card>
          <Card className="p-5">
            <p className="text-[11px] font-semibold uppercase tracking-widest text-[var(--text-muted)]">Upstream cost (pre-markup)</p>
            <p className="mt-1 text-2xl font-display font-semibold tabular-nums">{econ ? formatUSD(econ.upstream_usd) : "—"}</p>
          </Card>
          <Card className="p-5">
            <p className="text-[11px] font-semibold uppercase tracking-widest text-[var(--text-muted)]">Profit</p>
            <p className="mt-1 text-2xl font-display font-semibold tabular-nums">{econ ? formatUSD(econ.profit_usd) : "—"}</p>
          </Card>
          <Card className="p-5">
            <p className="text-[11px] font-semibold uppercase tracking-widest text-[var(--text-muted)]">Margin</p>
            <p className="mt-1 text-2xl font-display font-semibold tabular-nums">{econ ? `${econ.margin_pct.toFixed(1)}%` : "—"}</p>
          </Card>
        </div>
        {econ && econ.unpriced_usd > 0 && (
          <p className="mt-2 text-[12px] text-[var(--text-muted)]">
            {formatUSD(econ.unpriced_usd)} of billed usage has no known upstream cost (non-market routes) and is excluded from profit/margin.
          </p>
        )}
      </div>

      {ordersQuery.isError && <ErrorBanner message={(ordersQuery.error as Error).message} />}

      <Card className="overflow-hidden">
        {ordersQuery.isLoading ? (
          <div className="flex items-center justify-center py-16"><Spinner /></div>
        ) : orders.length === 0 ? (
          <EmptyState title="No payments yet" hint="Portal top-ups appear here as soon as a user starts a payment." />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="border-b border-[var(--border)] bg-[var(--bg-subtle)]/50">
                <tr className="text-[11px] uppercase tracking-wide text-[var(--text-muted)]">
                  <th className="px-5 py-3 text-left font-semibold">User</th>
                  <th className="px-5 py-3 text-left font-semibold">Key</th>
                  <th className="px-5 py-3 text-right font-semibold">Amount</th>
                  <th className="px-5 py-3 text-right font-semibold">Credit</th>
                  <th className="px-5 py-3 text-left font-semibold">Status</th>
                  <th className="px-5 py-3 text-right font-semibold">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--border)]">
                {orders.map((o) => (
                  <tr key={o.order_id} className="hover:bg-[var(--bg-subtle)]/30">
                    <td className="px-5 py-3">
                      <p className="text-[var(--text)]">{o.user_email || o.google_sub || "—"}</p>
                      <p className="font-mono text-[11px] text-[var(--text-muted)]">{o.provider_payment_id}</p>
                    </td>
                    <td className="px-5 py-3 font-mono text-[12px] text-[var(--text-muted)]">{o.key_display || o.key_id}</td>
                    <td className="px-5 py-3 text-right tabular-nums">{formatIDR(o.amount_idr)}</td>
                    <td className="px-5 py-3 text-right tabular-nums font-semibold">{formatUSD(o.credit_usd)}</td>
                    <td className="px-5 py-3"><Badge tone={STATUS_TONE[o.status] ?? "neutral"}>{o.status}</Badge></td>
                    <td className="px-5 py-3 text-right">
                      {o.status === "pending" && (
                        <Button variant="ghost" onClick={() => setApproving(o)} title="Approve manually">
                          <Check size={15} /> Approve
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {approving && (
        <Modal open onClose={() => setApproving(null)} title="Approve payment">
          <div className="px-6 py-5">
            <p className="text-sm text-[var(--text-muted)]">
              Credit {formatUSD(approving.credit_usd)} to {approving.user_email || approving.key_id}. This cannot be undone automatically.
            </p>
            <textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Reason (e.g. bank transfer verified)"
              className="mt-3 w-full rounded-lg border border-[var(--border)] bg-[var(--bg-subtle)] px-3 py-2 text-sm"
              rows={3}
            />
            <div className="mt-4 flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setApproving(null)}>Cancel</Button>
              <Button onClick={() => approve.mutate({ id: approving.order_id, reason })} disabled={approve.isPending}>
                <CreditCard size={15} /> Approve &amp; credit
              </Button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}
