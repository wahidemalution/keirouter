import { useState, type FormEvent } from "react";
import { Check } from "lucide-react";
import type { FinanceCustomRange } from "../../lib/api";
import { Button, Field, Input } from "../../components/ui";
import { todayISO } from "./shared";

// Keep in step with finance.MaxCustomDays on the backend.
const MAX_DAYS = 3660;

function daysBetween(from: string, to: string): number {
  return Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000) + 1;
}

/** Picks the first and last day of a custom finance range. Applies on submit so typing a date doesn't refetch. */
export function CustomRangeForm({ value, onApply }: { value: FinanceCustomRange; onApply: (r: FinanceCustomRange) => void }) {
  const [from, setFrom] = useState(value.from);
  const [to, setTo] = useState(value.to);
  const [touched, setTouched] = useState(false);
  const today = todayISO();

  const fromError = !from ? "Pick the first day." : from > today ? "Can't be after today." : undefined;
  const toError = !to
    ? "Pick the last day."
    : to > today
      ? "Can't be after today."
      : from && to < from
        ? "Must be on or after the first day."
        : from && daysBetween(from, to) > MAX_DAYS
          ? "Keep the range to 10 years or less."
          : undefined;
  const unchanged = from === value.from && to === value.to;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (fromError || toError) return;
    onApply({ from, to });
  };

  return (
    <form onSubmit={submit} noValidate className="flex flex-wrap items-start gap-3" aria-label="Custom date range">
      <Field label="From" error={touched ? fromError : undefined} className="w-full sm:w-44">
        <Input type="date" value={from} max={to && to < today ? to : today} onChange={(e) => setFrom(e.target.value)} />
      </Field>
      <Field label="To" error={touched ? toError : undefined} className="w-full sm:w-44">
        <Input type="date" value={to} min={from || undefined} max={today} onChange={(e) => setTo(e.target.value)} />
      </Field>
      <Button type="submit" variant="secondary" className="w-full sm:mt-[22px] sm:w-auto" disabled={unchanged}>
        <Check aria-hidden="true" />
        Show this range
      </Button>
    </form>
  );
}
