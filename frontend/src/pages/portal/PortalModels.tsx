import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Layers, Tag } from "lucide-react";
import { fetchPortalModels, fetchPortalStatus, fetchPortalUsage, type PortalModelPrice } from "../../lib/api";
import { familySlug } from "../../lib/providerResolve";
import { Badge, EmptyState, ErrorCard, Spinner } from "../../components/ui";
import { portal } from "../../lib/portalRoutes";
import { ModelSection, PageHeader, PanelCard, ProviderIcon, TD, TH, THEAD } from "./components";

// Labels for model families, used when a chain has no display provider.
const FAMILY_LABELS: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  gemini: "Google",
  deepseek: "DeepSeek",
  kimi: "Moonshot",
  qwen: "Qwen",
  minimax: "MiniMax",
  glm: "Z.ai",
  xai: "xAI",
  mistral: "Mistral",
  nvidia: "NVIDIA",
  perplexity: "Perplexity",
  qoder: "Qoder",
  "xiaomi-mimo": "Xiaomi",
  cohere: "Cohere",
};

type Group = { id: string; label: string; models: PortalModelPrice[] };

// A chain groups under its display provider. "combo" means none was set, so
// the model name decides the family (gpt -> OpenAI, glm -> Z.ai).
function groupOf(m: PortalModelPrice): { id: string; label: string } {
  if (m.provider_id && m.provider_id !== "combo") return { id: m.provider_id, label: m.provider || m.provider_id };
  const fam = familySlug(m.name);
  if (fam) return { id: fam, label: FAMILY_LABELS[fam] ?? fam };
  return { id: "other", label: "Other" };
}

function groupModels(models: PortalModelPrice[]): Group[] {
  const map = new Map<string, Group>();
  for (const m of models) {
    const g = groupOf(m);
    const entry = map.get(g.id) ?? { ...g, models: [] };
    entry.models.push(m);
    map.set(g.id, entry);
  }
  // Bigger groups first, "Other" last.
  return [...map.values()].sort((a, b) => {
    if (a.id === "other") return 1;
    if (b.id === "other") return -1;
    return b.models.length - a.models.length || a.label.localeCompare(b.label);
  });
}

const fmtPerM = (n: number) =>
  `$${n.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: n > 0 && n < 0.01 ? 6 : 4 })}`;

export function PortalModelsPage() {
  const { data: status, isLoading: statusLoading } = useQuery({
    queryKey: ["portal-status"],
    queryFn: fetchPortalStatus,
    retry: false,
  });
  const hasKey = !!status?.has_key;

  const prices = useQuery({ queryKey: ["portal-models"], queryFn: fetchPortalModels, enabled: hasKey });
  const usage = useQuery({
    queryKey: ["portal-usage", 30],
    queryFn: () => fetchPortalUsage(30),
    enabled: hasKey,
  });

  if (statusLoading) return <Spinner />;
  if (!status) return <ErrorCard message="Failed to load portal status" />;

  if (status.has_key === false) {
    return (
      <div className="space-y-6">
        <PageHeader icon={Layers} title="Models" />
        <EmptyState
          variant="card"
          icon={Layers}
          title="No API key yet"
          hint="Create an API key to see your models and their prices."
          action={<Link to={portal("/key")} className="text-[13px] font-medium text-link hover:underline">Create an API key</Link>}
        />
      </div>
    );
  }

  const usedModels = usage.data?.models ?? [];

  return (
    <div className="space-y-6">
      <PageHeader icon={Layers} title="Models" subtitle="Models this key may call, their best price, and what it used in the last 30 days." />

      {prices.isLoading ? (
        <Spinner />
      ) : prices.isError ? (
        <ErrorCard message={prices.error instanceof Error ? prices.error.message : "Failed to load models"} />
      ) : (prices.data?.models.length ?? 0) === 0 ? (
        <EmptyState variant="card" icon={Layers} title="No models available" hint="This key has no models it may call yet." />
      ) : (
        <PriceSection models={prices.data!.models} />
      )}

      {usage.isError ? (
        <ErrorCard message={usage.error instanceof Error ? usage.error.message : "Failed to load usage"} />
      ) : (
        usedModels.length > 0 && <ModelSection models={usedModels} />
      )}
    </div>
  );
}

function PriceSection({ models }: { models: PortalModelPrice[] }) {
  const groups = useMemo(() => groupModels(models), [models]);
  const [active, setActive] = useState<string | null>(null);
  const shown = active ? groups.filter((g) => g.id === active) : groups;

  return (
    <PanelCard
      icon={Tag}
      title="Models and prices"
      count={models.length}
      subtitle="Best price per 1M tokens. Market-priced models show today's cheapest offer."
    >
      {groups.length > 1 && (
        <div className="flex flex-wrap gap-2 border-b border-line px-4 py-3 sm:px-5" role="group" aria-label="Filter by provider">
          <FilterChip active={active === null} onClick={() => setActive(null)} count={models.length}>All</FilterChip>
          {groups.map((g) => (
            <FilterChip key={g.id} active={active === g.id} onClick={() => setActive(g.id)} count={g.models.length}>
              {g.label}
            </FilterChip>
          ))}
        </div>
      )}
      {/* Phones get stacked cards: four price columns do not fit 375px. */}
      <div className="sm:hidden">
        {shown.map((g) => (
          <section key={g.id} aria-label={g.label} className="border-b border-line last:border-b-0">
            <h3 className="bg-subtle px-4 py-2 text-[12px] font-medium text-fg-muted">
              {g.label} <span className="tabular-nums">· {g.models.length}</span>
            </h3>
            <ul className="divide-y divide-line">
              {g.models.map((m) => (
                <PriceCard key={m.name} m={m} iconId={g.id} />
              ))}
            </ul>
          </section>
        ))}
      </div>
      <div className="hidden overflow-x-auto sm:block">
        <table className="w-full min-w-[640px] text-[13px]">
          <thead className={THEAD}>
            <tr>
              <th scope="col" className={`${TH} text-left`}>Model</th>
              <th scope="col" className={`${TH} text-right`}>Best price, input</th>
              <th scope="col" className={`${TH} text-right`}>Best price, output</th>
              <th scope="col" className={`${TH} text-right`}>Discount</th>
            </tr>
          </thead>
          {shown.map((g) => (
            <tbody key={g.id} className="divide-y divide-line border-b border-line last:border-b-0">
              <tr className="bg-subtle">
                <th scope="rowgroup" colSpan={4} className="px-4 py-2 text-left text-[12px] font-medium text-fg-muted">
                  {g.label} <span className="tabular-nums">· {g.models.length}</span>
                </th>
              </tr>
              {g.models.map((m) => (
                <PriceRow key={m.name} m={m} iconId={g.id} />
              ))}
            </tbody>
          ))}
        </table>
      </div>
    </PanelCard>
  );
}

function PriceRow({ m, iconId }: { m: PortalModelPrice; iconId: string }) {
  const live = m.price_basis === "live";
  const pct = m.discount_up_to_pct ?? 0;
  return (
    <tr className="transition-colors hover:bg-hover">
      <td className={TD}>
        <div className="flex items-center gap-2.5">
          <ProviderIcon provider={iconId} className="h-7 w-7" />
          <span className="truncate font-mono text-[12.5px] text-fg">{m.name}</span>
        </div>
      </td>
      {m.sold_out ? (
        <td colSpan={3} className={`${TD} text-right text-fg-muted`}>Sold out</td>
      ) : (
        <>
          <PriceCell value={m.input_per_m} official={m.official_input_per_m} max={live ? m.max_input_per_m : null} />
          <PriceCell value={m.output_per_m} official={m.official_output_per_m} max={live ? m.max_output_per_m : null} />
          <td className={`${TD} text-right`}>
            {pct > 0 ? <DiscountBadge pct={pct} live={live} /> : <span className="text-fg-muted">None</span>}
          </td>
        </>
      )}
    </tr>
  );
}

function PriceCard({ m, iconId }: { m: PortalModelPrice; iconId: string }) {
  const live = m.price_basis === "live";
  const pct = m.discount_up_to_pct ?? 0;
  return (
    <li className="px-4 py-3">
      <div className="flex items-center gap-2.5">
        <ProviderIcon provider={iconId} className="h-7 w-7" />
        <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-fg">{m.name}</span>
        {!m.sold_out && pct > 0 && <DiscountBadge pct={pct} live={live} />}
      </div>
      {m.sold_out ? (
        <p className="mt-2 text-[12.5px] text-fg-muted">Sold out</p>
      ) : (
        <dl className="mt-2 grid grid-cols-2 gap-3">
          <div>
            <dt className="text-[11.5px] text-fg-muted">Best price, input</dt>
            <dd><PriceLines value={m.input_per_m} official={m.official_input_per_m} max={live ? m.max_input_per_m : null} /></dd>
          </div>
          <div>
            <dt className="text-[11.5px] text-fg-muted">Best price, output</dt>
            <dd><PriceLines value={m.output_per_m} official={m.official_output_per_m} max={live ? m.max_output_per_m : null} /></dd>
          </div>
        </dl>
      )}
    </li>
  );
}

function DiscountBadge({ pct, live }: { pct: number; live: boolean }) {
  return (
    <Badge tone="success" title={live ? "Best case: the cheapest offer against the official price" : "Off the official price"}>
      {live ? `Up to ${pct}% off` : `${pct}% off`}
    </Badge>
  );
}

function PriceCell(props: { value: number; official: number | null; max: number | null }) {
  return (
    <td className={`${TD} text-right align-top`}>
      <PriceLines {...props} />
    </td>
  );
}

// The best price, the official price it beats, and for market prices the
// most a request can cost now (skipped when it repeats the official price).
function PriceLines({ value, official, max }: { value: number; official: number | null; max: number | null }) {
  return (
    <>
      <div className="font-mono text-[12.5px] font-medium tabular-nums text-fg">{fmtPerM(value)}</div>
      {official != null && official > value && (
        <div className="font-mono text-[11.5px] tabular-nums text-fg-muted">
          <span className="sr-only">Official price </span>
          <s>{fmtPerM(official)}</s>
        </div>
      )}
      {max != null && max > value && max !== official && (
        <div className="text-[11.5px] tabular-nums text-fg-muted">max {fmtPerM(max)}</div>
      )}
    </>
  );
}

function FilterChip({ active, onClick, count, children }: { active: boolean; onClick: () => void; count: number; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`inline-flex min-h-11 items-center gap-1.5 rounded-lg sm:min-h-9 border px-3 text-[13px] font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500 ${
        active ? "border-line-strong bg-subtle text-fg" : "border-line bg-surface text-fg-muted hover:border-line-strong hover:text-fg"
      }`}
    >
      {children}
      <span className="tabular-nums text-[11.5px] text-fg-muted">{count}</span>
    </button>
  );
}
