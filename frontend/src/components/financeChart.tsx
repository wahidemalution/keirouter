// Shared pieces for the finance charts (AI gateway and hosting), so both use
// the same series colours, axis style and legend.

// Series colours. The first is the brand blue; the second and third are the
// next two slots of a categorical order validated for colour-vision
// deficiency in both themes (dataviz validate_palette: blue · orange · aqua,
// all-pairs). The light orange/aqua sit under 3:1 on the card, so every chart
// using them ships a legend, a tooltip and a table view.
export const VIZ_VARS =
  "[--viz-rev:var(--color-accent-500)] [--viz-cost:#ea7a3a] [--viz-profit:#1baf7a] dark:[--viz-cost:#e06f30] dark:[--viz-profit:#199e70]";
export const C_REV = "var(--viz-rev)";
export const C_COST = "var(--viz-cost)";
export const C_PROFIT = "var(--viz-profit)";

export const axisTick = { fontSize: 11.5, fill: "var(--text-muted)", fontFamily: "var(--font-sans)" } as const;

export function LegendItem({ color, label, line }: { color: string; label: string; line?: boolean }) {
  return (
    <li className="inline-flex items-center gap-1.5">
      {line ? (
        <span className="relative inline-block h-2 w-4" aria-hidden="true">
          <span className="absolute inset-x-0 top-1/2 h-0.5 -translate-y-1/2 rounded-full" style={{ background: color }} />
          <span className="absolute left-1/2 top-1/2 h-2 w-2 -translate-x-1/2 -translate-y-1/2 rounded-full" style={{ background: color }} />
        </span>
      ) : (
        <span className="h-2 w-2 rounded-[2px]" style={{ background: color }} aria-hidden="true" />
      )}
      {label}
    </li>
  );
}
