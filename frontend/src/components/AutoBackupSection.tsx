import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CloudUpload } from "lucide-react";
import {
  api,
  AutoBackupError,
  type AutoBackupInput,
  type AutoBackupSettings,
  type AutoBackupView,
} from "../lib/api";
import { estimateUpload, formatBytes, isZeroTime, statusLine } from "../lib/autoBackup";
import { fmtDateTime } from "../lib/serverTime";
import { Button, Card, ErrorBanner, Field, Input, SectionHeader, Select, SegmentedControl, Toggle } from "./ui";
import { useToast } from "./Toast";
import { Note } from "../pages/settings/shared";

const MIN_PASSPHRASE = 8;

// Draft is the form state: numbers stay strings while typing, secrets are
// empty unless the admin is replacing them.
interface Draft {
  enabled: boolean;
  destination: "s3" | "ftp";
  interval: string;
  keep: string;
  limit: string;
  passphrase: string;
  passphraseConfirm: string;
  s3: { endpoint: string; region: string; bucket: string; prefix: string; access_key_id: string; secret: string; path_style: boolean };
  ftp: { host: string; port: string; tls: "explicit" | "none"; username: string; password: string; folder: string };
}

function toDraft(s: AutoBackupSettings): Draft {
  return {
    enabled: s.enabled,
    destination: s.destination,
    interval: String(s.interval_hours),
    keep: String(s.keep),
    limit: String(s.upload_limit_kbps),
    passphrase: "",
    passphraseConfirm: "",
    s3: { endpoint: s.s3.endpoint, region: s.s3.region, bucket: s.s3.bucket, prefix: s.s3.prefix, access_key_id: s.s3.access_key_id, secret: "", path_style: s.s3.path_style },
    ftp: { host: s.ftp.host, port: String(s.ftp.port || 21), tls: s.ftp.tls, username: s.ftp.username, password: "", folder: s.ftp.folder },
  };
}

function toInput(d: Draft): AutoBackupInput {
  const num = (v: string) => (v.trim() === "" ? NaN : Number(v));
  return {
    enabled: d.enabled,
    destination: d.destination,
    interval_hours: num(d.interval),
    keep: num(d.keep),
    upload_limit_kbps: d.limit.trim() === "" ? 0 : num(d.limit),
    passphrase: d.passphrase.trim(),
    s3: {
      endpoint: d.s3.endpoint.trim(),
      region: d.s3.region.trim(),
      bucket: d.s3.bucket.trim(),
      prefix: d.s3.prefix.trim(),
      access_key_id: d.s3.access_key_id.trim(),
      secret_access_key: d.s3.secret,
      path_style: d.s3.path_style,
    },
    ftp: {
      host: d.ftp.host.trim(),
      port: num(d.ftp.port),
      tls: d.ftp.tls,
      username: d.ftp.username,
      password: d.ftp.password,
      folder: d.ftp.folder.trim(),
    },
  };
}

// clientError catches what the server would reject before a round trip, keyed
// like the server's field names.
function clientError(d: Draft): { field: string; message: string } | null {
  const intOk = (v: string, lo: number, hi: number) => /^\d+$/.test(v.trim()) && Number(v) >= lo && Number(v) <= hi;
  if (!intOk(d.interval, 1, 720)) return { field: "interval_hours", message: "Enter a whole number of hours from 1 to 720." };
  if (!intOk(d.keep, 1, 365)) return { field: "keep", message: "Enter a whole number from 1 to 365." };
  if (d.limit.trim() !== "" && !intOk(d.limit, 0, 1_000_000)) return { field: "upload_limit_kbps", message: "Enter 0 for no limit, or up to 1,000,000 KB/s." };
  if (d.passphrase || d.passphraseConfirm) {
    if (d.passphrase.trim().length < MIN_PASSPHRASE) return { field: "passphrase", message: `Use at least ${MIN_PASSPHRASE} characters.` };
    if (d.passphrase !== d.passphraseConfirm) return { field: "passphrase_confirm", message: "The passphrases do not match." };
  }
  if (d.destination === "ftp" && !intOk(d.ftp.port, 1, 65535)) return { field: "ftp.port", message: "Enter a port from 1 to 65535." };
  return null;
}

const RESULT_LABEL: Record<string, string> = { success: "Uploaded", failed: "Failed", cancelled: "Cancelled" };

function fmtDuration(ms: number): string {
  if (ms < 1000) return "< 1 s";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  return m < 60 ? `${m} min ${s % 60} s` : `${Math.floor(m / 60)} h ${m % 60} min`;
}

/** A stored secret shows "Saved" with a Replace button; otherwise an input. */
function SecretField({
  label,
  saved,
  value,
  onChange,
  error,
  hint,
  autoComplete = "new-password",
}: {
  label: string;
  saved: boolean;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  hint?: ReactNode;
  autoComplete?: string;
}) {
  const [replacing, setReplacing] = useState(false);
  const labelId = useId();
  if (saved && !replacing) {
    return (
      <div className="space-y-1.5">
        <span id={labelId} className="text-[12.5px] font-medium text-fg">{label}</span>
        <div className="flex min-h-9 items-center justify-between gap-3 rounded-lg border border-line bg-subtle px-3">
          <span className="text-[13px] text-fg-muted">Saved</span>
          <Button type="button" variant="ghost" className="min-h-7 px-2 py-0.5" aria-describedby={labelId} onClick={() => setReplacing(true)}>
            Replace
          </Button>
        </div>
        {error && <p className="text-[12px] text-bad">{error}</p>}
      </div>
    );
  }
  return (
    <Field label={label} error={error} hint={hint}>
      <Input type="password" autoComplete={autoComplete} value={value} onChange={(e) => onChange(e.target.value)} />
    </Field>
  );
}

export function AutoBackupSection() {
  const toast = useToast();
  const qc = useQueryClient();
  const query = useQuery({
    queryKey: ["auto-backup"],
    queryFn: () => api.getAutoBackup(),
    refetchInterval: (q) => ((q.state.data as AutoBackupView | undefined)?.status.running ? 3_000 : 60_000),
  });

  const [draft, setDraft] = useState<Draft | null>(null);
  const [baseline, setBaseline] = useState<string>("");
  const [errors, setErrors] = useState<{ field: string; message: string } | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [pending, setPending] = useState<"save" | "test" | "run" | null>(null);
  const [testResult, setTestResult] = useState<string | null>(null);

  const settings = query.data?.settings;
  useEffect(() => {
    if (settings && draft === null) {
      const d = toDraft(settings);
      setDraft(d);
      setBaseline(JSON.stringify(d));
    }
  }, [settings, draft]);

  const dirty = draft !== null && JSON.stringify(draft) !== baseline;
  const status = query.data?.status;
  const line = useMemo(
    () => (status ? statusLine(status, (iso) => fmtDateTime(iso, { dateStyle: "medium", timeStyle: "short" })) : null),
    [status],
  );

  if (query.isPending) {
    return (
      <Card>
        <SectionHeader title="Scheduled offsite backup" icon={CloudUpload} iconTone="neutral" />
        <p role="status" className="border-t border-line px-5 py-4 text-[13px] text-fg-muted">Loading backup settings…</p>
      </Card>
    );
  }
  if (query.isError || !settings || !draft || !status) {
    return (
      <Card>
        <SectionHeader title="Scheduled offsite backup" icon={CloudUpload} iconTone="neutral" />
        <div className="border-t border-line px-5 py-4">
          <ErrorBanner
            message={`Could not load the backup settings: ${(query.error as Error | null)?.message ?? "unknown error"}.`}
            action={<Button variant="ghost" onClick={() => query.refetch()}>Try again</Button>}
          />
        </div>
      </Card>
    );
  }

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft({ ...draft, [k]: v });
  const setS3 = (k: keyof Draft["s3"], v: string | boolean) => setDraft({ ...draft, s3: { ...draft.s3, [k]: v } });
  const setFTP = (k: keyof Draft["ftp"], v: string) => setDraft({ ...draft, ftp: { ...draft.ftp, [k]: v } });
  const err = (field: string) => (errors?.field === field ? errors.message : undefined);

  const fail = (e: unknown) => {
    if (e instanceof AutoBackupError && e.field) {
      setErrors({ field: e.field, message: e.message.replace(/^[a-z0-9_.]+: /, "") });
      setActionError("Fix the highlighted field.");
    } else {
      setActionError((e as Error).message || "Request failed.");
    }
  };

  const begin = (kind: "save" | "test" | "run") => {
    setPending(kind);
    setErrors(null);
    setActionError(null);
    setTestResult(null);
  };

  const save = async () => {
    const ce = clientError(draft);
    if (ce) {
      setErrors(ce);
      setActionError("Fix the highlighted field.");
      return;
    }
    begin("save");
    try {
      const view = await api.saveAutoBackup(toInput(draft));
      qc.setQueryData(["auto-backup"], view);
      const d = toDraft(view.settings);
      setDraft(d);
      setBaseline(JSON.stringify(d));
      toast.success("Backup settings saved", view.settings.enabled ? "Backups now run on the schedule." : "The schedule is off.");
    } catch (e) {
      fail(e);
    } finally {
      setPending(null);
    }
  };

  const test = async () => {
    const ce = clientError(draft);
    if (ce) {
      setErrors(ce);
      setActionError("Fix the highlighted field.");
      return;
    }
    begin("test");
    try {
      await api.testAutoBackup(toInput(draft));
      setTestResult(`Connected. A test file was written to ${draft.destination === "s3" ? "the bucket" : "the folder"} and deleted.`);
    } catch (e) {
      fail(e);
    } finally {
      setPending(null);
    }
  };

  const run = async () => {
    begin("run");
    try {
      await api.runAutoBackup();
      await qc.invalidateQueries({ queryKey: ["auto-backup"] });
      toast.success("Backup started", "Progress shows at the top of this card.");
    } catch (e) {
      fail(e);
    } finally {
      setPending(null);
    }
  };

  const busy = pending !== null || status.running;
  const lastSize = status.state.last_size;
  const limitNum = Number(draft.limit || 0);
  const estimate = estimateUpload(lastSize, limitNum);
  const limitHint =
    limitNum > 0
      ? estimate
        ? `The last backup (${formatBytes(lastSize)}) would take ${estimate} at this limit.`
        : "KB per second. 0 means no limit."
      : "KB per second. 0 means no limit.";
  const s3 = settings.s3;
  const ftp = settings.ftp;

  return (
    <Card>
      <SectionHeader
        title="Scheduled offsite backup"
        description="Uploads the full router backup to S3 or FTP on a schedule and keeps the newest files there."
        icon={CloudUpload}
        iconTone="neutral"
      />

      {line && (
        <div className="border-t border-line px-4 py-3 sm:px-5">
          <p role="status" aria-live="polite" className={`text-[13px] leading-snug ${line.tone === "danger" ? "text-bad" : "text-fg"}`}>
            {line.text}
          </p>
          {!isZeroTime(status.state.last_success) && status.state.last_file && (
            <p className="mt-0.5 break-all text-[12px] text-fg-muted">{status.state.last_file}</p>
          )}
        </div>
      )}

      <form
        className="space-y-5 border-t border-line px-4 py-4 sm:px-5"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
        noValidate
      >
        <div className="flex items-center justify-between gap-4">
          <div>
            <span id="ab-enabled-label" className="text-[13px] font-medium text-fg">Run backups on a schedule</span>
            <p id="ab-enabled-hint" className="text-[12px] text-fg-muted">Needs a destination and a passphrase.</p>
          </div>
          <Toggle
            checked={draft.enabled}
            onChange={(v) => set("enabled", v)}
            aria-labelledby="ab-enabled-label"
            aria-describedby="ab-enabled-hint"
          />
        </div>

        <div className="space-y-1.5">
          <span id="ab-dest-label" className="text-[12.5px] font-medium text-fg">Destination</span>
          <div>
            <SegmentedControl
              aria-labelledby="ab-dest-label"
              value={draft.destination}
              onChange={(v) => set("destination", v)}
              options={[
                { value: "s3", label: "S3 or compatible" },
                { value: "ftp", label: "FTP" },
              ]}
            />
          </div>
        </div>

        {draft.destination === "s3" ? (
          <fieldset className="grid gap-4 sm:grid-cols-2">
            <legend className="sr-only">S3 bucket</legend>
            <Field label="Endpoint" optional hint="Blank means AWS. For R2, MinIO or Wasabi, paste the endpoint URL." error={err("s3.endpoint")} className="sm:col-span-2">
              <Input inputMode="url" placeholder="https://s3.example.com" value={draft.s3.endpoint} onChange={(e) => setS3("endpoint", e.target.value)} />
            </Field>
            <Field label="Region" error={err("s3.region")}>
              <Input placeholder="us-east-1" value={draft.s3.region} onChange={(e) => setS3("region", e.target.value)} />
            </Field>
            <Field label="Bucket" error={err("s3.bucket")}>
              <Input value={draft.s3.bucket} onChange={(e) => setS3("bucket", e.target.value)} />
            </Field>
            <Field label="Folder" optional hint="Files go under this prefix, for example keirouter/." className="sm:col-span-2">
              <Input value={draft.s3.prefix} onChange={(e) => setS3("prefix", e.target.value)} />
            </Field>
            <Field label="Access key ID" error={err("s3.access_key_id")}>
              <Input autoComplete="off" value={draft.s3.access_key_id} onChange={(e) => setS3("access_key_id", e.target.value)} />
            </Field>
            <SecretField label="Secret access key" saved={s3.secret_access_key.set} value={draft.s3.secret} onChange={(v) => setS3("secret", v)} error={err("s3.secret_access_key")} />
            <div className="flex items-center justify-between gap-4 sm:col-span-2">
              <div>
                <span id="ab-path-label" className="text-[13px] font-medium text-fg">Path-style URLs</span>
                <p id="ab-path-hint" className="text-[12px] text-fg-muted">Turn on for MinIO and most self-hosted S3.</p>
              </div>
              <Toggle checked={draft.s3.path_style} onChange={(v) => setS3("path_style", v)} aria-labelledby="ab-path-label" aria-describedby="ab-path-hint" />
            </div>
          </fieldset>
        ) : (
          <fieldset className="grid gap-4 sm:grid-cols-2">
            <legend className="sr-only">FTP server</legend>
            <Field label="Host" error={err("ftp.host")}>
              <Input placeholder="ftp.example.com" value={draft.ftp.host} onChange={(e) => setFTP("host", e.target.value)} />
            </Field>
            <Field label="Port" error={err("ftp.port")}>
              <Input inputMode="numeric" value={draft.ftp.port} onChange={(e) => setFTP("port", e.target.value)} />
            </Field>
            <Field label="Encryption" error={err("ftp.tls")} className="sm:col-span-2">
              <Select value={draft.ftp.tls} onChange={(e) => setFTP("tls", e.target.value)}>
                <option value="explicit">Explicit TLS (FTPS)</option>
                <option value="none">None (plain FTP)</option>
              </Select>
            </Field>
            {draft.ftp.tls === "none" && (
              <Note tone="warn" className="sm:col-span-2">
                Plain FTP sends the password and the backup file unencrypted. Use it only on a network you trust.
              </Note>
            )}
            <Field label="Username" error={err("ftp.username")}>
              <Input autoComplete="off" value={draft.ftp.username} onChange={(e) => setFTP("username", e.target.value)} />
            </Field>
            <SecretField label="Password" saved={ftp.password.set} value={draft.ftp.password} onChange={(v) => setFTP("password", v)} error={err("ftp.password")} />
            <Field label="Folder" optional hint="Created on Test connection if it does not exist." className="sm:col-span-2">
              <Input placeholder="backups/keirouter" value={draft.ftp.folder} onChange={(e) => setFTP("folder", e.target.value)} />
            </Field>
          </fieldset>
        )}

        <fieldset className="grid gap-4 sm:grid-cols-3">
          <legend className="sr-only">Schedule</legend>
          <Field label="Every (hours)" error={err("interval_hours")}>
            <Input inputMode="numeric" value={draft.interval} onChange={(e) => set("interval", e.target.value)} />
          </Field>
          <Field label="Keep (files)" error={err("keep")} hint="The oldest file is deleted after each upload.">
            <Input inputMode="numeric" value={draft.keep} onChange={(e) => set("keep", e.target.value)} />
          </Field>
          <Field label="Upload limit (KB/s)" error={err("upload_limit_kbps")} hint={limitHint}>
            <Input inputMode="numeric" value={draft.limit} onChange={(e) => set("limit", e.target.value)} />
          </Field>
        </fieldset>

        <fieldset className="grid gap-4 sm:grid-cols-2">
          <legend className="sr-only">Backup passphrase</legend>
          <SecretField
            label="Backup passphrase"
            saved={settings.passphrase.set}
            value={draft.passphrase}
            onChange={(v) => set("passphrase", v)}
            error={err("passphrase")}
            hint="Restoring a file needs it. Keep a copy somewhere other than this server."
          />
          {(draft.passphrase !== "" || !settings.passphrase.set) && (
            <Field label="Confirm passphrase" error={err("passphrase_confirm")}>
              <Input type="password" autoComplete="new-password" value={draft.passphraseConfirm} onChange={(e) => set("passphraseConfirm", e.target.value)} />
            </Field>
          )}
        </fieldset>

        {actionError && <ErrorBanner message={actionError} />}
        {testResult && (
          <p role="status" className="text-[13px] text-fg">{testResult}</p>
        )}

        <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
          <Button type="submit" disabled={busy || !dirty}>{pending === "save" ? "Saving…" : "Save"}</Button>
          <Button type="button" variant="ghost" disabled={busy} onClick={() => void test()}>
            {pending === "test" ? "Testing…" : "Test connection"}
          </Button>
          <Button type="button" variant="ghost" disabled={busy || dirty} aria-describedby={dirty ? "ab-run-hint" : undefined} onClick={() => void run()}>
            {pending === "run" ? "Starting…" : status.running ? "Backup running…" : "Back up now"}
          </Button>
          {dirty && (
            <span id="ab-run-hint" className="text-[12px] text-fg-muted">Save first: Back up now uses the saved settings.</span>
          )}
        </div>
      </form>

      <div className="border-t border-line px-4 py-4 sm:px-5">
        <h3 className="text-[13px] font-medium text-fg">Recent runs</h3>
        {status.history.length === 0 ? (
          <p className="mt-1 text-[13px] text-fg-muted">No runs yet. Press Back up now after saving to make the first one.</p>
        ) : (
          <div className="mt-2 overflow-x-auto">
            <table className="w-full min-w-[520px] text-left text-[12.5px]">
              <thead className="text-fg-muted">
                <tr>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Started</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Result</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Size</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Duration</th>
                  <th scope="col" className="py-1.5 font-medium">Note</th>
                </tr>
              </thead>
              <tbody>
                {status.history.map((r) => (
                  <tr key={r.started_at + r.result} className="border-t border-line align-top">
                    <td className="whitespace-nowrap py-1.5 pr-3">{fmtDateTime(r.started_at, { dateStyle: "medium", timeStyle: "short" })}</td>
                    <td className={`whitespace-nowrap py-1.5 pr-3 ${r.result === "success" ? "text-fg" : "text-bad"}`}>{RESULT_LABEL[r.result] ?? r.result}</td>
                    <td className="whitespace-nowrap py-1.5 pr-3">{r.size ? formatBytes(r.size) : ""}</td>
                    <td className="whitespace-nowrap py-1.5 pr-3">{fmtDuration(r.duration_ms)}</td>
                    <td className="break-words py-1.5 text-fg-muted">{r.note}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Card>
  );
}
