import { test } from "node:test";
import assert from "node:assert/strict";
import { estimateUpload, formatBytes, statusLine, type AutoBackupStatusLike } from "./autoBackup.ts";

const MB = 1024 * 1024;
const fmt = (iso: string) => `T(${iso})`;

function status(over: Partial<AutoBackupStatusLike> = {}): AutoBackupStatusLike {
  return {
    state: { last_success: "0001-01-01T00:00:00Z", last_attempt: "0001-01-01T00:00:00Z", last_failed: false, consecutive_failures: 0, last_size: 0, last_file: "" },
    next_due: "0001-01-01T00:00:00Z",
    running: false,
    bytes_sent: 0,
    bytes_total: 0,
    failing: false,
    history: [],
    ...over,
  };
}

test("estimateUpload rounds to minutes and hides unknowns", () => {
  assert.equal(estimateUpload(500 * MB, 1024), "≈ 8 min");
  assert.equal(estimateUpload(10 * 1024, 1024), "< 1 min");
  assert.equal(estimateUpload(8 * 1024 * MB, 1024), "≈ 2 h 17 min");
  assert.equal(estimateUpload(0, 1024), null);
  assert.equal(estimateUpload(500 * MB, 0), null);
});

test("formatBytes uses binary units", () => {
  assert.equal(formatBytes(182 * MB), "182 MB");
  assert.equal(formatBytes(1536 * MB), "1.5 GB");
  assert.equal(formatBytes(2048), "2 KB");
  assert.equal(formatBytes(0), "0 B");
});

test("statusLine: never run", () => {
  assert.deepEqual(statusLine(status(), fmt), {
    tone: "neutral",
    text: "No scheduled backup has run yet. Add a destination, then press Test connection.",
  });
});

test("statusLine: running shows upload progress", () => {
  assert.deepEqual(statusLine(status({ running: true, bytes_sent: 64 * MB, bytes_total: 182 * MB }), fmt), {
    tone: "neutral",
    text: "Uploading 64 MB of 182 MB.",
  });
  assert.equal(statusLine(status({ running: true }), fmt).text, "Building the backup file.");
});

test("statusLine: failing streak names the error", () => {
  const s = status({
    failing: true,
    state: { ...status().state, consecutive_failures: 3, last_failed: true },
    history: [{ started_at: "2026-11-01T14:00:00Z", result: "failed", size: 0, duration_ms: 10, note: "s3: PUT: 403 AccessDenied: Access Denied", file: "" }],
  });
  assert.deepEqual(statusLine(s, fmt), {
    tone: "danger",
    text: "Last 3 runs failed: s3: PUT: 403 AccessDenied: Access Denied. Older backups are still at the destination.",
  });
});

test("statusLine: one failure says when it retries", () => {
  const s = status({
    next_due: "2026-11-01T14:30:00Z",
    state: { ...status().state, consecutive_failures: 1, last_failed: true },
    history: [{ started_at: "2026-11-01T14:00:00Z", result: "failed", size: 0, duration_ms: 10, note: "timeout", file: "" }],
  });
  assert.deepEqual(statusLine(s, fmt), { tone: "danger", text: "Last run failed: timeout. Next try T(2026-11-01T14:30:00Z)." });
});

test("statusLine: healthy", () => {
  const s = status({
    next_due: "2026-11-02T14:00:00Z",
    state: { ...status().state, last_success: "2026-11-01T14:00:00Z", last_size: 182 * MB, last_file: "keirouter-backup-20261101-140000.json.gz" },
    history: [{ started_at: "2026-11-01T14:00:00Z", result: "success", size: 182 * MB, duration_ms: 1000, note: "", file: "keirouter-backup-20261101-140000.json.gz" }],
  });
  assert.deepEqual(statusLine(s, fmt), {
    tone: "neutral",
    text: "Last backup T(2026-11-01T14:00:00Z), 182 MB. Next run T(2026-11-02T14:00:00Z).",
  });
});
