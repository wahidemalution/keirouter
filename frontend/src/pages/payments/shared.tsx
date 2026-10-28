import { useId, type ReactNode } from "react";
import { type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { SectionTitle } from "../../components/ui";
import { fmtDateTime, serverDateISO } from "../../lib/serverTime";

// Every Payments query polls at the same pace so the numbers on the page move together.
export const REFETCH_MS = 15_000;

// Query keys shared by the sections. Mutations invalidate these prefixes.
export const QK = {
  finance: ["finance"] as const,
};

export function usd(n: number): string {
  const sign = n < 0 ? "-" : "";
  return `${sign}$${Math.abs(n).toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

export function idr(n: number): string {
  return new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 }).format(n);
}

export function count(n: number): string {
  return n.toLocaleString("en-US");
}

/** "Oct 15, 2026", or "-" when the value is empty or invalid. */
export function dateID(value: string, withTime = false): string {
  if (!value) return "-";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "-";
  return fmtDateTime(d, {
    day: "numeric",
    month: "short",
    year: "numeric",
    ...(withTime ? { hour: "2-digit", minute: "2-digit", hour12: false } : {}),
  });
}

/** Today's date as YYYY-MM-DD in the server zone, for <input type="date">. */
export function todayISO(): string {
  return serverDateISO(Date.now());
}

/** RFC3339 to YYYY-MM-DD in the server zone; "" when empty. */
export function toDateInput(value: string): string {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  return serverDateISO(d);
}

export function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

export const TH = "px-4 py-2 font-medium";
export const THEAD = "border-b border-line bg-subtle text-left text-[12px] text-fg-faint";

/** A bordered section with a SectionTitle header row, matching Usage.tsx panels. */
export function Panel({
  icon,
  title,
  subtitle,
  action,
  children,
  className,
  id,
}: {
  icon: LucideIcon;
  title: string;
  subtitle?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  id?: string;
}) {
  const headingId = useId();
  return (
    <section
      id={id}
      aria-labelledby={headingId}
      className={cn("min-w-0 scroll-mt-6 overflow-hidden rounded-2xl border border-line bg-surface shadow-[var(--shadow-card)]", className)}
    >
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
        <SectionTitle id={headingId} icon={icon} title={title} subtitle={subtitle} />
        {action}
      </div>
      {children}
    </section>
  );
}
