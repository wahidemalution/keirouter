import type { ReactNode } from "react";
import { AlertTriangle } from "lucide-react";
import { cn } from "@/lib/utils";

// FieldMessage is the inline hint or error under a chain editor field. Errors
// carry an icon so they never rely on color alone.
export function FieldMessage({ id, tone = "bad", children }: { id?: string; tone?: "bad" | "muted"; children: ReactNode }) {
  return (
    <p id={id} className={cn("flex items-start gap-1.5 text-[12px] leading-5", tone === "bad" ? "text-bad" : "text-fg-muted")}>
      {tone === "bad" && <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" strokeWidth={1.75} aria-hidden="true" />}
      {children}
    </p>
  );
}
