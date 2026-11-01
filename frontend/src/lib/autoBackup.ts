// Pure helpers for the scheduled offsite backup card.

export interface AutoBackupRunLike {
  started_at: string;
  result: "success" | "failed" | "cancelled" | string;
  size: number;
  duration_ms: number;
  note: string;
  file: string;
}

export interface AutoBackupStatusLike {
  state: {
    last_success: string;
    last_attempt: string;
    last_failed: boolean;
    consecutive_failures: number;
    last_size: number;
    last_file: string;
  };
  next_due: string;
  running: boolean;
  bytes_sent: number;
  bytes_total: number;
  failing: boolean;
  history: AutoBackupRunLike[];
}

/** Go's zero time.Time, which the API sends for "never". */
export function isZeroTime(iso: string | undefined | null): boolean {
  return !iso || iso.startsWith("0001-01-01");
}

export function formatBytes(n: number): string {
  if (!n || n < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const rounded = v >= 100 || i === 0 ? Math.round(v) : Math.round(v * 10) / 10;
  return `${rounded} ${units[i]}`;
}

/** Upload time for sizeBytes at limitKBps, or null when unlimited or unknown. */
export function estimateUpload(sizeBytes: number, limitKBps: number): string | null {
  if (!sizeBytes || sizeBytes <= 0 || !limitKBps || limitKBps <= 0) return null;
  const seconds = sizeBytes / (limitKBps * 1024);
  if (seconds < 60) return "< 1 min";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `≈ ${minutes} min`;
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return m === 0 ? `≈ ${h} h` : `≈ ${h} h ${m} min`;
}

/** The one-line answer to "is my offsite backup healthy?". */
export function statusLine(
  s: AutoBackupStatusLike,
  fmtTime: (iso: string) => string,
): { tone: "neutral" | "danger"; text: string } {
  if (s.running) {
    if (s.bytes_total > 0) {
      return { tone: "neutral", text: `Uploading ${formatBytes(s.bytes_sent)} of ${formatBytes(s.bytes_total)}.` };
    }
    return { tone: "neutral", text: "Building the backup file." };
  }
  const last = s.history[0];
  if (s.failing && last) {
    return {
      tone: "danger",
      text: `Last ${s.state.consecutive_failures} runs failed: ${last.note}. Older backups are still at the destination.`,
    };
  }
  if (s.state.last_failed && last && last.result !== "success") {
    const next = isZeroTime(s.next_due) ? "" : ` Next try ${fmtTime(s.next_due)}.`;
    return { tone: "danger", text: `Last run failed: ${last.note}.${next}` };
  }
  if (isZeroTime(s.state.last_success)) {
    return { tone: "neutral", text: "No scheduled backup has run yet. Add a destination, then press Test connection." };
  }
  const next = isZeroTime(s.next_due) ? "" : ` Next run ${fmtTime(s.next_due)}.`;
  return {
    tone: "neutral",
    text: `Last backup ${fmtTime(s.state.last_success)}, ${formatBytes(s.state.last_size)}.${next}`,
  };
}
