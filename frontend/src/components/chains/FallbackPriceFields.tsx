import { useId } from "react";
import { Field, Input } from "../ui";
import { FieldMessage } from "./FieldMessage";
import { CACHE_WRITE_FACTOR, type FallbackPriceDraft, type FallbackPricePreview } from "./chainUtils";

const usd = (v: number) => `$${Number(v.toFixed(6)).toString()}`;

const scaledHint = (input: string, factor: number, fallback: string) => {
  const n = Number(input);
  return input.trim() !== "" && Number.isFinite(n) && n > 0 ? `auto: ${Number((n * factor).toFixed(6))}` : fallback;
};

// FallbackPriceFields sets what a request served by the final fallback is
// billed. The chain price and the global markup never apply to it, so the
// preview shows the exact customer price before saving.
export function FallbackPriceFields({
  draft,
  preview,
  autoKnown,
  onChange,
}: {
  draft: FallbackPriceDraft;
  preview: FallbackPricePreview;
  /** False while the picked model differs from the saved one, so no models.dev price is loaded for it yet. */
  autoKnown: boolean;
  onChange: (next: Partial<FallbackPriceDraft>) => void;
}) {
  const uid = useId();
  const previewId = `${uid}-preview`;
  const invalid = !preview.ok;

  return (
    <div className="space-y-2">
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <Field label="Input per 1M (USD)" optional>
          <Input inputMode="decimal" value={draft.inputPerM} onChange={(event) => onChange({ inputPerM: event.target.value })} placeholder="empty: models.dev" aria-invalid={invalid || undefined} aria-describedby={previewId} />
        </Field>
        <Field label="Output per 1M (USD)" optional>
          <Input inputMode="decimal" value={draft.outputPerM} onChange={(event) => onChange({ outputPerM: event.target.value })} placeholder="empty: models.dev" aria-invalid={invalid || undefined} aria-describedby={previewId} />
        </Field>
        <Field label="Cache read per 1M (USD)" optional>
          <Input inputMode="decimal" value={draft.cacheReadPerM} onChange={(event) => onChange({ cacheReadPerM: event.target.value })} placeholder="empty: input price" aria-describedby={previewId} />
        </Field>
        <Field label="Cache write per 1M (USD)" optional>
          <Input inputMode="decimal" value={draft.cacheWritePerM} onChange={(event) => onChange({ cacheWritePerM: event.target.value })} placeholder={scaledHint(draft.inputPerM, CACHE_WRITE_FACTOR, "auto")} aria-describedby={previewId} />
        </Field>
        <Field label="Markup %" optional>
          <Input inputMode="decimal" value={draft.markup} onChange={(event) => onChange({ markup: event.target.value })} placeholder="0 (no markup)" aria-describedby={previewId} />
        </Field>
      </div>
      <div id={previewId} aria-live="polite" className="rounded-lg border border-line bg-surface px-3 py-2">
        {!preview.ok ? (
          <FieldMessage>{preview.error}</FieldMessage>
        ) : preview.source === "none" ? (
          <FieldMessage tone={autoKnown ? "bad" : "muted"}>
            {autoKnown
              ? "models.dev has no price for this model, so the fallback will not run. Enter an input and output price."
              : "Save to look up the models.dev price for this model, or enter a price."}
          </FieldMessage>
        ) : (
          <>
            <p className="text-[13px] text-fg">
              Customer pays <span className="font-medium tabular-nums">{usd(preview.inputPerM)}</span> input · <span className="font-medium tabular-nums">{usd(preview.outputPerM)}</span> output per 1M (markup {preview.markup}%)
            </p>
            <p className="text-[12px] leading-5 text-fg-muted">
              {preview.source === "models.dev" ? "From the models.dev price. " : ""}Cache read <span className="tabular-nums">{usd(preview.cacheReadPerM)}</span> · cache write <span className="tabular-nums">{usd(preview.cacheWritePerM)}</span> per 1M. The chain price and global markup do not apply.
            </p>
          </>
        )}
      </div>
    </div>
  );
}
