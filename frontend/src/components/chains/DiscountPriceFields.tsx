import { useId, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button, Field, Input } from "../ui";
import { FieldMessage } from "./FieldMessage";
import { CACHE_READ_FACTOR, CACHE_WRITE_FACTOR, type DraftChainStep, discountStepPreview } from "./chainUtils";

const usd = (v: number) => `$${Number(v.toFixed(8)).toString()}`;

const autoHint = (input: string, factor: number) => {
  const n = Number(input);
  return input.trim() !== "" && Number.isFinite(n) && n > 0 ? `auto: ${Number((n * factor).toFixed(8))}` : "auto";
};

const priceText = (v: number | null) => (v === null ? "" : String(v));

// DiscountPriceFields prices a step as a discount off the models.dev list
// price. The operator fetches the list price (then may edit it), sets the
// customer discount and optionally their own cost multiplier, and sees the
// exact per-1M customer price and cost the gateway will bill.
export function DiscountPriceFields({ step, onChange }: { step: DraftChainStep; onChange: (next: Partial<DraftChainStep>) => void }) {
  const uid = useId();
  const previewId = `${uid}-preview`;
  const [filledFrom, setFilledFrom] = useState("");
  const preview = discountStepPreview(step);
  const model = step.model.trim();

  const fetchPrice = useMutation({
    mutationFn: () => api.modelReference(model),
    onSuccess: (ref) => {
      onChange({
        staticInputPerM: String(ref.input_per_m),
        staticOutputPerM: String(ref.output_per_m),
        staticCacheReadPerM: priceText(ref.cache_read_per_m),
        staticCacheWritePerM: priceText(ref.cache_write_per_m),
      });
      setFilledFrom(ref.provider ? `${model} (${ref.provider} listing)` : model);
    },
  });

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          variant="secondary"
          onClick={() => fetchPrice.mutate()}
          disabled={!model || fetchPrice.isPending}
          aria-busy={fetchPrice.isPending || undefined}
          className="min-h-11 sm:min-h-9"
        >
          {fetchPrice.isPending ? "Fetching price…" : "Fetch models.dev price"}
        </Button>
        <span role="status" className="text-[12px] leading-5 text-fg-muted">
          {!model
            ? "Pick a model first."
            : fetchPrice.isSuccess && filledFrom
              ? `List price filled from models.dev: ${filledFrom}.`
              : ""}
        </span>
      </div>
      {fetchPrice.isError && (
        <FieldMessage>
          {(fetchPrice.error instanceof Error && fetchPrice.error.message ? fetchPrice.error.message : "Could not load the models.dev price").replace(/\.?$/, ".")} Enter the list price by hand.
        </FieldMessage>
      )}

      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <Field label="List input per 1M (USD)">
          <Input inputMode="decimal" value={step.staticInputPerM} onChange={(event) => onChange({ staticInputPerM: event.target.value })} placeholder="0.28" aria-describedby={previewId} />
        </Field>
        <Field label="List output per 1M (USD)">
          <Input inputMode="decimal" value={step.staticOutputPerM} onChange={(event) => onChange({ staticOutputPerM: event.target.value })} placeholder="0.42" aria-describedby={previewId} />
        </Field>
        <Field label="List cache read per 1M (USD)" optional>
          <Input inputMode="decimal" value={step.staticCacheReadPerM} onChange={(event) => onChange({ staticCacheReadPerM: event.target.value })} placeholder={autoHint(step.staticInputPerM, CACHE_READ_FACTOR)} aria-describedby={previewId} />
        </Field>
        <Field label="List cache write per 1M (USD)" optional>
          <Input inputMode="decimal" value={step.staticCacheWritePerM} onChange={(event) => onChange({ staticCacheWritePerM: event.target.value })} placeholder={autoHint(step.staticInputPerM, CACHE_WRITE_FACTOR)} aria-describedby={previewId} />
        </Field>
        <Field label="Customer discount %" hint="90 means the customer pays 10% of the list price.">
          <Input inputMode="decimal" value={step.discountPercent} onChange={(event) => onChange({ discountPercent: event.target.value })} placeholder="90" aria-describedby={previewId} />
        </Field>
        <Field label="Cost multiplier" optional hint="Your upstream cost as a fraction of list, e.g. 0.01. Empty: cost equals the customer price.">
          <Input inputMode="decimal" value={step.costMultiplier} onChange={(event) => onChange({ costMultiplier: event.target.value })} placeholder="0.01" aria-describedby={previewId} />
        </Field>
      </div>

      <div id={previewId} aria-live="polite" className="space-y-1 rounded-lg border border-line bg-subtle px-3 py-2">
        {!preview.ok ? (
          <FieldMessage>{preview.error}</FieldMessage>
        ) : (
          <>
            <p className="text-[13px] text-fg">
              Customer pays <span className="font-medium tabular-nums">{usd(preview.customer.inputPerM)}</span> input · <span className="font-medium tabular-nums">{usd(preview.customer.outputPerM)}</span> output per 1M ({preview.discount}% off list, no markup)
            </p>
            <p className="text-[12px] leading-5 text-fg-muted">
              Cache read <span className="tabular-nums">{usd(preview.customer.cacheReadPerM)}</span> · cache write <span className="tabular-nums">{usd(preview.customer.cacheWritePerM)}</span> per 1M.
            </p>
            <p className="text-[12px] leading-5 text-fg-muted">
              Your cost <span className="tabular-nums">{usd(preview.cost.inputPerM)}</span> input · <span className="tabular-nums">{usd(preview.cost.outputPerM)}</span> output · <span className="tabular-nums">{usd(preview.cost.cacheReadPerM)}</span> cache read · <span className="tabular-nums">{usd(preview.cost.cacheWritePerM)}</span> cache write per 1M.
            </p>
            {preview.loss && <FieldMessage>The cost multiplier is above what the customer pays, so every request on this step loses money.</FieldMessage>}
          </>
        )}
      </div>
    </div>
  );
}
