// Every date and time in the admin dashboard is shown in the server's
// timezone, not the viewer's, so "today", chart buckets and log times match
// the host. The dashboard shell sets the zone from gateway info before any
// page renders; until then (and in the user portal) the browser zone is used.

let zone: string | undefined;
let iana = "";

function validZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/** An Intl timeZone for a fixed UTC offset in seconds. */
function offsetZone(offsetS: number): string | undefined {
  if (offsetS === 0) return "UTC";
  if (offsetS % 3600 === 0) {
    // Etc/GMT signs are inverted: UTC+7 is Etc/GMT-7.
    const etc = `Etc/GMT${offsetS > 0 ? "-" : "+"}${Math.abs(offsetS) / 3600}`;
    if (validZone(etc)) return etc;
  }
  const a = Math.abs(offsetS);
  const hhmm = `${offsetS > 0 ? "+" : "-"}${String(Math.floor(a / 3600)).padStart(2, "0")}:${String(Math.floor((a % 3600) / 60)).padStart(2, "0")}`;
  return validZone(hhmm) ? hhmm : undefined;
}

/** Sets the server zone from gateway info. An IANA name wins over the offset. */
export function setServerZone(tzIana: string | undefined, offsetS: number) {
  iana = tzIana && validZone(tzIana) ? tzIana : "";
  zone = iana || offsetZone(offsetS);
}

/** The server's IANA zone, or "" when only its offset is known. Sent as the
 * `tz` query parameter; "" makes the server use its own zone. */
export function serverIana(): string {
  return iana;
}

/** Spread into Intl / toLocale* options to format in the server zone. */
export function tzOpt(): { timeZone?: string } {
  return zone ? { timeZone: zone } : {};
}

type DateIn = Date | number | string;
const toDate = (d: DateIn) => (d instanceof Date ? d : new Date(d));

/** Date and time in the server zone (toLocaleString). */
export function fmtDateTime(d: DateIn, opts?: Intl.DateTimeFormatOptions, locale?: string): string {
  return toDate(d).toLocaleString(locale, { ...opts, ...tzOpt() });
}

/** Date only in the server zone (toLocaleDateString). */
export function fmtDate(d: DateIn, opts?: Intl.DateTimeFormatOptions, locale?: string): string {
  return toDate(d).toLocaleDateString(locale, { ...opts, ...tzOpt() });
}

/** Time only in the server zone (toLocaleTimeString). */
export function fmtTime(d: DateIn, opts?: Intl.DateTimeFormatOptions, locale?: string): string {
  return toDate(d).toLocaleTimeString(locale, { ...opts, ...tzOpt() });
}

export interface ZonedParts {
  year: number;
  month: number; // 1-12
  day: number;
  hour: number;
  minute: number;
  second: number;
}

/** Calendar fields of an instant in the server zone. */
export function zonedParts(d: DateIn): ZonedParts {
  const parts = new Intl.DateTimeFormat("en-US", {
    ...tzOpt(),
    year: "numeric",
    month: "numeric",
    day: "numeric",
    hour: "numeric",
    minute: "numeric",
    second: "numeric",
    hourCycle: "h23",
  }).formatToParts(toDate(d));
  const get = (t: Intl.DateTimeFormatPartTypes) => Number(parts.find((p) => p.type === t)?.value ?? 0);
  return { year: get("year"), month: get("month"), day: get("day"), hour: get("hour") % 24, minute: get("minute"), second: get("second") };
}

/** The instant of midnight starting the server-zone day that contains ms. */
export function startOfServerDay(ms: number): number {
  const p = zonedParts(ms);
  return ms - ((p.hour * 60 + p.minute) * 60 + p.second) * 1000 - (((ms % 1000) + 1000) % 1000);
}

const pad = (n: number) => String(n).padStart(2, "0");

/** YYYY-MM-DD of an instant in the server zone. */
export function serverDateISO(d: DateIn): string {
  const p = zonedParts(d);
  return `${p.year}-${pad(p.month)}-${pad(p.day)}`;
}
