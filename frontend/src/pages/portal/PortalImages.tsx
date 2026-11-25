import { useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { AlertCircle, Clock, Download, ImagePlus, RotateCcw, Tags, Trash2 } from "lucide-react";
import {
  createPortalImage, deletePortalImage, fetchPortalImageJobs, fetchPortalImageModels, fetchPortalStatus,
  PortalAPIError, type PortalImageJob, type PortalImageModel,
} from "../../lib/api";
import { Button, EmptyState, ErrorBanner, ErrorCard, Field, Select, Spinner, Textarea } from "../../components/ui";
import { portal } from "../../lib/portalRoutes";
import {
  FAMILY_LABELS, deletesIn, formatImagePriceMicro, imageAlt, imageErrorText, optionsForFamily, totalPriceMicro,
} from "../../lib/imageOptions";
import { PageHeader, PanelCard, TD, TH, THEAD, formatDateTime } from "./components";

const MODELS_QK = ["portal-image-models"];
const JOBS_QK = ["portal-image-jobs"];
const POLL_MS = 3000;
const HISTORY_HEADING_ID = "riwayat-gambar";
const MAX_PROMPT = 4000;
const COUNTS = [1, 2, 3, 4];
// Coarse pointers get 44px controls; the shared controls are 36px.
const TAP = "[@media(pointer:coarse)]:min-h-11";
const isActive = (j: PortalImageJob) => j.status === "queued" || j.status === "running";

interface Settings {
  chainId: string;
  prompt: string;
  n: number;
  size: string;
  aspect: string;
  resolution: string;
}

interface SubmitError {
  status: number;
  text: string;
}

function toSubmitError(e: unknown): SubmitError {
  if (e instanceof PortalAPIError) return { status: e.status, text: imageErrorText(e.code, e.message) };
  return { status: 0, text: e instanceof Error ? e.message : "Gagal membuat gambar." };
}

// pick keeps a value only when the family offers it, else the family default.
function pick(list: string[] | undefined, value: string): string {
  if (!list || list.length === 0) return "";
  return list.includes(value) ? value : list[0];
}

function optionSummary(job: PortalImageJob): string {
  const p = job.params;
  return [p.size, p.aspect_ratio, p.resolution.toUpperCase()].filter(Boolean).join(" · ");
}

// useMinuteClock re-renders once a minute so "dihapus dalam N jam" stays true.
function useMinuteClock(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(t);
  }, []);
  return now;
}

export function PortalImagesPage() {
  const status = useQuery({ queryKey: ["portal-status"], queryFn: fetchPortalStatus, retry: false });

  if (status.isLoading) return <Spinner label="Memuat akun" />;
  if (!status.data) return <ErrorCard message="Gagal memuat status portal." />;

  if (status.data.has_key === false) {
    return (
      <div className="space-y-6">
        <PageHeader icon={ImagePlus} title="Buat Gambar" />
        <EmptyState
          variant="card"
          icon={ImagePlus}
          title="Belum punya API key"
          hint="Gambar dibayar dari saldo PAYG API key Anda. Buat API key terlebih dahulu."
          action={<Link to={portal("/key")} className="text-[13px] font-medium text-link hover:underline">Buat API key</Link>}
        />
      </div>
    );
  }

  return <ImageStudio />;
}

function ImageStudio() {
  const qc = useQueryClient();
  const now = useMinuteClock();
  const promptId = useId();
  const formRef = useRef<HTMLFormElement>(null);

  const models = useQuery({ queryKey: MODELS_QK, queryFn: fetchPortalImageModels, retry: false });
  const jobs = useQuery({
    queryKey: JOBS_QK,
    queryFn: () => fetchPortalImageJobs(50),
    retry: false,
    // Poll only while a job is queued or running. react-query pauses the
    // interval in a hidden tab (refetchIntervalInBackground is false) and
    // resumes it when the tab is visible again.
    refetchInterval: (query) => (query.state.data?.some(isActive) ? POLL_MS : false),
  });

  const [form, setForm] = useState<Settings>({ chainId: "", prompt: "", n: 1, size: "", aspect: "", resolution: "" });
  const [submitError, setSubmitError] = useState<SubmitError | null>(null);
  const [notice, setNotice] = useState("");

  const list = models.data ?? [];
  const model: PortalImageModel | undefined = list.find((m) => m.chain_id === form.chainId) ?? list[0];
  const opts = optionsForFamily(model?.family ?? "");
  const size = pick(opts.sizes, form.size);
  const aspect = pick(opts.aspectRatios, form.aspect);
  const resolution = pick(opts.resolutions, form.resolution);
  const total = model ? totalPriceMicro(model.price_micro, model.price_2k_micro ?? 0, resolution, form.n) : 0;

  const create = useMutation({
    mutationFn: () =>
      createPortalImage({
        chain_id: model!.chain_id,
        prompt: form.prompt.trim(),
        n: form.n,
        size,
        aspect_ratio: aspect,
        resolution,
      }),
    onSuccess: (job) => {
      setSubmitError(null);
      qc.setQueryData<PortalImageJob[]>(JOBS_QK, (old) => [job, ...(old ?? []).filter((j) => j.id !== job.id)]);
      void qc.invalidateQueries({ queryKey: JOBS_QK });
    },
    onError: (e) => setSubmitError(toSubmitError(e)),
  });

  const promptEmpty = form.prompt.trim() === "";
  const canSubmit = !!model && !promptEmpty && !create.isPending;

  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (!canSubmit) {
      if (promptEmpty) setSubmitError({ status: 400, text: imageErrorText("empty_prompt", "") });
      return;
    }
    setNotice("");
    create.mutate();
  };

  const onPromptKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      submit();
    }
  };

  const reuse = (job: PortalImageJob) => {
    const p = job.params;
    const available = list.some((m) => m.chain_id === p.chain_id);
    setForm({
      chainId: available ? p.chain_id : form.chainId,
      prompt: job.prompt,
      n: Math.min(4, Math.max(1, p.n || 1)),
      size: p.size,
      aspect: p.aspect_ratio,
      resolution: p.resolution,
    });
    setSubmitError(null);
    setNotice(
      available
        ? "Setelan dimuat ke formulir."
        : "Prompt dimuat ke formulir. Model job ini tidak lagi tersedia, pilih model lain.",
    );
    formRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
    document.getElementById(promptId)?.focus({ preventScroll: true });
  };

  const modelName = (chainId: string) => list.find((m) => m.chain_id === chainId)?.name;

  return (
    <div className="space-y-6">
      <PageHeader
        icon={ImagePlus}
        title="Buat Gambar"
        subtitle="Tulis prompt, pilih model, lalu bayar per gambar dari saldo PAYG."
      />

      <PanelCard icon={ImagePlus} title="Prompt dan setelan">
        {models.isLoading ? (
          <Spinner label="Memuat model gambar" />
        ) : models.isError ? (
          <div className="p-4 sm:p-5">
            <ErrorBanner
              message={models.error instanceof Error ? models.error.message : "Gagal memuat model gambar."}
              action={<Button variant="ghost" onClick={() => void models.refetch()}>Coba lagi</Button>}
            />
          </div>
        ) : list.length === 0 ? (
          <EmptyState
            icon={ImagePlus}
            title="Belum ada model gambar yang tersedia"
            hint="Model gambar muncul di sini setelah admin mengaktifkannya untuk akun Anda."
          />
        ) : (
          <form ref={formRef} onSubmit={submit} className="grid grid-cols-1 lg:grid-cols-[minmax(0,1fr)_320px]" noValidate>
            <div className="min-w-0 space-y-4 p-4 sm:p-5">
              <Field
                label="Prompt"
                hint={
                  <span className="flex flex-wrap justify-between gap-x-3">
                    <span>Tekan Ctrl+Enter (⌘+Enter di Mac) untuk membuat.</span>
                    <span className="tabular-nums">{form.prompt.length}/{MAX_PROMPT}</span>
                  </span>
                }
              >
                <Textarea
                  id={promptId}
                  value={form.prompt}
                  onChange={(e) => {
                    setForm((f) => ({ ...f, prompt: e.target.value }));
                    setSubmitError(null);
                  }}
                  onKeyDown={onPromptKey}
                  maxLength={MAX_PROMPT}
                  rows={5}
                  placeholder="Jelaskan gambar yang Anda inginkan"
                  className="min-h-32 text-[14px]"
                />
              </Field>

              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <Field label="Model">
                  <Select
                    value={model?.chain_id ?? ""}
                    onChange={(e) => setForm((f) => ({ ...f, chainId: e.target.value }))}
                    className={TAP}
                  >
                    {list.map((m) => (
                      <option key={m.chain_id} value={m.chain_id}>
                        {m.name} · {formatImagePriceMicro(m.price_micro)}/gambar
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label="Jumlah gambar">
                  <Select
                    value={String(form.n)}
                    onChange={(e) => setForm((f) => ({ ...f, n: Number(e.target.value) }))}
                    className={`tabular-nums ${TAP}`}
                  >
                    {COUNTS.map((c) => (
                      <option key={c} value={c}>{c}</option>
                    ))}
                  </Select>
                </Field>
                {opts.sizes && (
                  <Field label="Ukuran">
                    <Select value={size} onChange={(e) => setForm((f) => ({ ...f, size: e.target.value }))} className={`tabular-nums ${TAP}`}>
                      {opts.sizes.map((s) => (
                        <option key={s} value={s}>{s === "auto" ? "Otomatis" : s.replace("x", " × ")}</option>
                      ))}
                    </Select>
                  </Field>
                )}
                {opts.aspectRatios && (
                  <Field label="Rasio aspek">
                    <Select value={aspect} onChange={(e) => setForm((f) => ({ ...f, aspect: e.target.value }))} className={`tabular-nums ${TAP}`}>
                      {opts.aspectRatios.map((a) => (
                        <option key={a} value={a}>{a}</option>
                      ))}
                    </Select>
                  </Field>
                )}
                {opts.resolutions && (
                  <Field label="Resolusi">
                    <Select value={resolution} onChange={(e) => setForm((f) => ({ ...f, resolution: e.target.value }))} className={TAP}>
                      {opts.resolutions.map((r) => (
                        <option key={r} value={r}>
                          {r.toUpperCase()}
                          {r === "2k" && model?.price_2k_micro ? ` · ${formatImagePriceMicro(model.price_2k_micro)}/gambar` : ""}
                        </option>
                      ))}
                    </Select>
                  </Field>
                )}
              </div>
            </div>

            <div className="flex min-w-0 flex-col gap-4 border-t border-line bg-subtle p-4 sm:p-5 lg:border-l lg:border-t-0">
              <dl className="space-y-2.5 text-[13px]">
                <div className="flex items-baseline justify-between gap-3">
                  <dt className="text-fg-muted">Model</dt>
                  <dd className="min-w-0 truncate text-right font-medium text-fg" title={model?.name}>{model?.name}</dd>
                </div>
                <div className="flex items-baseline justify-between gap-3">
                  <dt className="text-fg-muted">Harga per gambar</dt>
                  <dd className="tabular-nums text-fg">
                    {model ? formatImagePriceMicro(totalPriceMicro(model.price_micro, model.price_2k_micro ?? 0, resolution, 1)) : ""}
                  </dd>
                </div>
                <div className="flex items-baseline justify-between gap-3 border-t border-line pt-2.5">
                  <dt className="text-fg-muted">Total</dt>
                  <dd className="text-[18px] font-semibold tabular-nums text-fg">{formatImagePriceMicro(total)}</dd>
                </div>
              </dl>
              <Button type="submit" disabled={create.isPending} className={`w-full ${TAP}`}>
                <ImagePlus aria-hidden="true" />
                {create.isPending ? "Mengirim…" : `Buat ${form.n} gambar · ${formatImagePriceMicro(total)}`}
              </Button>
              <p className="text-[12px] leading-5 text-fg-muted">
                Saldo dipotong hanya untuk gambar yang berhasil dibuat.
              </p>
              {submitError && (
                <ErrorBanner
                  message={submitError.text}
                  action={
                    submitError.status === 402 ? (
                      <Link
                        to={portal("/topup")}
                        className="inline-flex min-h-9 items-center whitespace-nowrap rounded-md px-1 text-[13px] font-medium text-link underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500"
                      >
                        Isi saldo
                      </Link>
                    ) : undefined
                  }
                />
              )}
              <p role="status" aria-live="polite" className="text-[12.5px] text-fg-muted empty:hidden">{notice}</p>
            </div>
          </form>
        )}
      </PanelCard>

      <section aria-labelledby={HISTORY_HEADING_ID} className="space-y-3">
        <div className="flex items-start gap-3 rounded-2xl border border-warn/30 bg-warn/10 px-4 py-3">
          <Clock className="mt-0.5 h-4 w-4 shrink-0 text-warn" strokeWidth={1.75} aria-hidden="true" />
          <p className="text-[13px] text-fg">
            Gambar hanya disimpan 24 jam, lalu dihapus otomatis. Unduh gambar yang ingin Anda simpan segera.
          </p>
        </div>

        <div className="flex items-baseline justify-between gap-3">
          <h2 id={HISTORY_HEADING_ID} tabIndex={-1} className="rounded-md text-[15px] font-semibold text-fg focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500">Riwayat</h2>
          {jobs.data?.some(isActive) && (
            <p className="text-[12.5px] text-fg-muted">Diperbarui otomatis tiap 3 detik.</p>
          )}
        </div>

        {jobs.isLoading ? (
          <Spinner label="Memuat riwayat gambar" />
        ) : jobs.isError ? (
          <ErrorBanner
            message={jobs.error instanceof Error ? jobs.error.message : "Gagal memuat riwayat gambar."}
            action={<Button variant="ghost" onClick={() => void jobs.refetch()}>Coba lagi</Button>}
          />
        ) : (jobs.data ?? []).length === 0 ? (
          <EmptyState variant="card" icon={ImagePlus} title="Belum ada gambar. Gambar disimpan 24 jam." />
        ) : (
          <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {jobs.data!.map((job) => (
              <li key={job.id} className="min-w-0">
                <JobCard job={job} now={now} modelName={modelName(job.params.chain_id)} onReuse={reuse} />
              </li>
            ))}
          </ul>
        )}
      </section>

      <PriceTable models={models} />
    </div>
  );
}

function JobCard({ job, now, modelName, onReuse }: {
  job: PortalImageJob;
  now: number;
  modelName?: string;
  onReuse: (job: PortalImageJob) => void;
}) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const titleId = useId();
  const cancelId = `${titleId}-cancel`;
  const deleteId = `${titleId}-delete`;
  // Focus follows the confirm step: onto Batal when it opens, back onto Hapus
  // when it is cancelled.
  const wasConfirming = useRef(false);

  const remove = useMutation({
    mutationFn: () => deletePortalImage(job.id),
    onSuccess: () => {
      qc.setQueryData<PortalImageJob[]>(JOBS_QK, (old) => (old ?? []).filter((j) => j.id !== job.id));
      // The card and its focused button are gone; land on the history heading.
      document.getElementById(HISTORY_HEADING_ID)?.focus();
      void qc.invalidateQueries({ queryKey: JOBS_QK });
    },
    onError: (e) => {
      setConfirming(false);
      setDeleteError(e instanceof PortalAPIError ? imageErrorText(e.code, e.message) : "Gagal menghapus gambar.");
    },
  });

  useEffect(() => {
    if (confirming) document.getElementById(cancelId)?.focus();
    else if (wasConfirming.current) document.getElementById(deleteId)?.focus();
    wasConfirming.current = confirming;
  }, [confirming, cancelId, deleteId]);

  const cancelConfirm = () => setConfirming(false);

  const active = isActive(job);
  const files = job.files ?? [];
  const alt = imageAlt(job.prompt);
  const meta = [modelName, optionSummary(job), `${job.params.n} gambar`].filter(Boolean).join(" · ");

  return (
    <article aria-labelledby={titleId} className="flex h-full flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-[var(--shadow-card)]">
      {job.status === "succeeded" && files.length > 0 ? (
        <div className={`grid gap-px bg-line ${files.length > 1 ? "grid-cols-2" : "grid-cols-1"}`}>
          {files.map((f, i) => (
            <div key={f.id} className="flex min-w-0 flex-col bg-subtle">
              <a
                href={f.url}
                target="_blank"
                rel="noopener"
                className="block aspect-square focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-500"
              >
                <img
                  src={f.url}
                  alt={files.length > 1 ? `${alt} (gambar ${i + 1} dari ${files.length})` : alt}
                  loading="lazy"
                  className="h-full w-full object-contain"
                />
              </a>
              <a
                href={`${f.url}?download=1`}
                download
                aria-label={files.length > 1 ? `Unduh gambar ${i + 1} dari ${files.length}` : "Unduh gambar"}
                className="flex min-h-9 items-center justify-center gap-1.5 border-t border-line bg-surface text-[12.5px] font-medium text-fg transition-colors hover:bg-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-500 [@media(pointer:coarse)]:min-h-11"
              >
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
                Unduh{files.length > 1 ? ` ${i + 1}` : ""}
              </a>
            </div>
          ))}
        </div>
      ) : active ? (
        <div className="flex aspect-[4/3] flex-col items-center justify-center gap-3 bg-subtle px-4 text-center" role="status">
          <div className="h-5 w-5 animate-spin rounded-full border-2 border-line-strong border-t-fg-muted motion-reduce:animate-none" aria-hidden="true" />
          <p className="text-[13px] font-medium text-fg">{job.status === "queued" ? "Dalam antrean" : "Sedang dibuat"}</p>
          <p className="text-[12px] text-fg-muted">Status diperbarui otomatis.</p>
        </div>
      ) : job.status === "failed" ? (
        <div className="flex aspect-[4/3] flex-col items-center justify-center gap-2 bg-subtle px-4 text-center">
          <AlertCircle className="h-5 w-5 text-bad" strokeWidth={1.75} aria-hidden="true" />
          <p className="text-[13px] font-medium text-fg">Gagal</p>
          <p className="text-[12.5px] leading-5 text-fg-muted">{job.error || "Gambar tidak dapat dibuat."}</p>
        </div>
      ) : (
        <div className="flex aspect-[4/3] flex-col items-center justify-center bg-subtle px-4 text-center">
          <p className="text-[12.5px] text-fg-muted">Gambar sudah tidak tersedia.</p>
        </div>
      )}

      <div className="flex flex-1 flex-col gap-2 p-4">
        <p id={titleId} className="line-clamp-3 break-words text-[13px] leading-5 text-fg" title={job.prompt}>
          {job.prompt}
        </p>
        <p className="break-words text-[12px] text-fg-muted">{meta}</p>
        <p className="flex flex-wrap gap-x-2 text-[12px] tabular-nums text-fg-muted">
          <span>{formatDateTime(job.created_at)}</span>
          {job.status === "succeeded" && job.cost_micro > 0 && <span>Biaya {formatImagePriceMicro(job.cost_micro)}</span>}
        </p>
        {job.status === "succeeded" && job.expires_at && (
          <p className="flex items-center gap-1.5 text-[12px] font-medium text-warn">
            <Clock className="h-3.5 w-3.5 shrink-0" strokeWidth={1.75} aria-hidden="true" />
            {deletesIn(job.expires_at, now)}
          </p>
        )}
        {deleteError && <p role="alert" className="text-[12.5px] text-bad">{deleteError}</p>}

        <div className="mt-auto flex flex-wrap gap-2 pt-2">
          {confirming ? (
            <div
              className="flex w-full flex-wrap items-center gap-2"
              onKeyDown={(e) => {
                if (e.key === "Escape") cancelConfirm();
              }}
            >
              <p className="w-full text-[12.5px] text-fg">Hapus job ini beserta gambarnya?</p>
              <Button variant="danger" onClick={() => remove.mutate()} disabled={remove.isPending} className={TAP}>
                <Trash2 aria-hidden="true" />
                {remove.isPending ? "Menghapus…" : "Ya, hapus"}
              </Button>
              <Button id={cancelId} variant="ghost" onClick={cancelConfirm} className={TAP}>Batal</Button>
            </div>
          ) : (
            <>
              <Button variant="ghost" onClick={() => onReuse(job)} className={TAP}>
                <RotateCcw aria-hidden="true" />
                Pakai setelan ini
              </Button>
              {!active && (
                <Button
                  id={deleteId}
                  variant="ghost"
                  onClick={() => {
                    setDeleteError("");
                    setConfirming(true);
                  }}
                  className={TAP}
                >
                  <Trash2 aria-hidden="true" />
                  Hapus
                </Button>
              )}
            </>
          )}
        </div>
      </div>
    </article>
  );
}

function PriceTable({ models }: { models: UseQueryResult<PortalImageModel[]> }) {
  const list = models.data ?? [];
  return (
    <PanelCard icon={Tags} title="Harga per gambar" count={models.isSuccess ? list.length : undefined}>
      {models.isLoading ? (
        <Spinner label="Memuat harga" />
      ) : models.isError ? (
        <p className="px-4 py-3 text-[13px] text-bad sm:px-5">Harga tidak dapat dimuat.</p>
      ) : list.length === 0 ? (
        <EmptyState size="sm" icon={Tags} title="Belum ada model gambar yang tersedia" />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[480px] text-[13px]">
            <thead className={THEAD}>
              <tr>
                <th scope="col" className={`${TH} text-left`}>Model</th>
                <th scope="col" className={`${TH} text-left`}>Keluarga</th>
                <th scope="col" className={`${TH} text-right`}>Per gambar</th>
                <th scope="col" className={`${TH} text-right`}>2K</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {list.map((m) => {
                // Only families with a resolution choice have a 2K price.
                const has2K = !!optionsForFamily(m.family).resolutions;
                const price2K = has2K && m.price_2k_micro ? m.price_2k_micro : 0;
                return (
                  <tr key={m.chain_id}>
                    <td className={`${TD} font-mono text-[12.5px] text-fg`}>{m.name}</td>
                    <td className={`${TD} text-fg-muted`}>{FAMILY_LABELS[m.family] ?? m.family}</td>
                    <td className={`${TD} text-right tabular-nums text-fg`}>{formatImagePriceMicro(m.price_micro)}</td>
                    <td className={`${TD} text-right tabular-nums ${price2K ? "text-fg" : "text-fg-muted"}`}>
                      {price2K ? formatImagePriceMicro(price2K) : has2K ? "Sama" : "Tidak ada"}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </PanelCard>
  );
}
