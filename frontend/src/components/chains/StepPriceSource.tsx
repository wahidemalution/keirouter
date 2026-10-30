import { useId } from "react";
import { cn } from "@/lib/utils";
import { Field, Input } from "../ui";
import { FieldMessage } from "./FieldMessage";
import { CACHE_READ_FACTOR, CACHE_WRITE_FACTOR, type DraftChainStep, type StepPriceSource as Source, staticStepPreview } from "./chainUtils";

const focusRing = "peer-focus-visible:ring-2 peer-focus-visible:ring-accent-500";

const SOURCES: { value: Source; label: string }[] = [
  { value: "market", label: "Market slug" },
  { value: "static", label: "Static price" },
];

// usd prints a per-1M price without trailing noise: 0.65 -> $0.65, 0.0625 -> $0.0625.
const usd = (v: number) => `$${Number(v.toFixed(6)).toString()}`;

// autoHint is the placeholder for an empty cache field: what the gateway
// derives from the input price.
const autoHint = (input: string, factor: number) => {
  const n = Number(input);
  return input.trim() !== "" && Number.isFinite(n) && n > 0 ? `auto: ${Number((n * factor).toFixed(6))}` : "auto";
};

// StepPriceSource picks how one route step is billed: by its market slug, or
// by a fixed provider price plus markup. For a static price it shows what the
// customer pays per 1M tokens, recomputed on every keystroke, so the operator
// sees the billed rate before saving. inheritedMarkup is null while the
// global markup is still loading.
export function StepPriceSource({
  step,
  stepNumber,
  inheritedMarkup,
  inheritedLabel,
  onChange,
}: {
  step: DraftChainStep;
  stepNumber: number;
  inheritedMarkup: number | null;
  inheritedLabel: string;
  onChange: (next: Partial<DraftChainStep>) => void;
}) {
  const uid = useId();
  const previewId = `${uid}-preview`;
  const markupPending = inheritedMarkup === null && step.stepMarkup.trim() === "";
  const preview = staticStepPreview(step, inheritedMarkup ?? 0);
  const invalid = step.priceSource === "static" && !preview.ok;

  return (
    <div className="space-y-2">
      <fieldset>
        <legend className="mb-1.5 text-[12.5px] font-medium text-fg">Step {stepNumber} price source</legend>
        <div className="inline-flex rounded-xl border border-line bg-subtle p-0.5">
          {SOURCES.map((source) => {
            const active = step.priceSource === source.value;
            return (
              <label key={source.value} className="relative">
                <input
                  type="radio"
                  name={`${uid}-source`}
                  value={source.value}
                  checked={active}
                  onChange={() => onChange({ priceSource: source.value })}
                  className="peer sr-only"
                />
                <span
                  className={cn(
                    "inline-flex min-h-11 cursor-pointer items-center rounded-lg px-3 text-[12px] font-medium transition-colors sm:min-h-7",
                    focusRing,
                    active ? "bg-surface text-fg ring-1 ring-line-strong" : "text-fg-muted hover:text-fg",
                  )}
                >
                  {source.label}
                </span>
              </label>
            );
          })}
        </div>
      </fieldset>

      {step.priceSource === "market" ? (
        <Field label="Market slug" hint="Market slug for this step (optional): an inferhub slug, surplus:, surplus-trusted: or surplus-fast:">
          <Input value={step.marketSlug} onChange={(event) => onChange({ marketSlug: event.target.value })} placeholder="e.g. cbcn/deepseek-v4.1-flash" className="font-mono" />
        </Field>
      ) : (
        <div className="space-y-2">
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <Field label="Input per 1M (USD)">
              <Input inputMode="decimal" value={step.staticInputPerM} onChange={(event) => onChange({ staticInputPerM: event.target.value })} placeholder="0.5" aria-invalid={invalid || undefined} aria-describedby={previewId} />
            </Field>
            <Field label="Output per 1M (USD)">
              <Input inputMode="decimal" value={step.staticOutputPerM} onChange={(event) => onChange({ staticOutputPerM: event.target.value })} placeholder="2" aria-invalid={invalid || undefined} aria-describedby={previewId} />
            </Field>
            <Field label="Cache read per 1M (USD)" optional>
              <Input inputMode="decimal" value={step.staticCacheReadPerM} onChange={(event) => onChange({ staticCacheReadPerM: event.target.value })} placeholder={autoHint(step.staticInputPerM, CACHE_READ_FACTOR)} aria-describedby={previewId} />
            </Field>
            <Field label="Cache write per 1M (USD)" optional>
              <Input inputMode="decimal" value={step.staticCacheWritePerM} onChange={(event) => onChange({ staticCacheWritePerM: event.target.value })} placeholder={autoHint(step.staticInputPerM, CACHE_WRITE_FACTOR)} aria-describedby={previewId} />
            </Field>
            <Field label="Markup %" optional>
              <Input
                inputMode="decimal"
                value={step.stepMarkup}
                onChange={(event) => onChange({ stepMarkup: event.target.value })}
                placeholder={inheritedMarkup === null ? `${inheritedLabel}` : `${inheritedMarkup}% (${inheritedLabel})`}
                aria-describedby={previewId}
              />
            </Field>
          </div>
          <div id={previewId} aria-live="polite" className="rounded-lg border border-line bg-subtle px-3 py-2">
            {!preview.ok ? (
              <FieldMessage>{preview.error}</FieldMessage>
            ) : markupPending ? (
              <FieldMessage tone="muted">Loading the {inheritedLabel} to show the customer price.</FieldMessage>
            ) : (
              <>
                <p className="text-[13px] text-fg">
                  Customer pays <span className="font-medium tabular-nums">{usd(preview.inputPerM)}</span> input · <span className="font-medium tabular-nums">{usd(preview.outputPerM)}</span> output per 1M (markup {preview.markup}%)
                </p>
                <p className="text-[12px] leading-5 text-fg-muted">
                  Cache read <span className="tabular-nums">{usd(preview.cacheReadPerM)}</span> · cache write <span className="tabular-nums">{usd(preview.cacheWritePerM)}</span> per 1M. Billed only when a request reaches this step.
                </p>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
