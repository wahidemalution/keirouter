import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Loader2, Package, Pencil, Plus, Power, Settings2, ShoppingCart, Trash2, Users } from "lucide-react";
import { cn } from "@/lib/utils";
import { api, type TokenPackage, type TokenPackageInput, type TokenPackageKind, type TokenSettings, type TokenSubscription } from "../lib/api";
import { PageHeader } from "../components/Layout";
import { useToast } from "../components/Toast";
import { useConfirm } from "../components/ui/confirm-dialog";
import {
  Badge,
  Button,
  EmptyState,
  ErrorBanner,
  Field,
  Input,
  LoadingRegion,
  Modal,
  ModalFooter,
  Select,
  Skeleton,
  TabBar,
  TabPanel,
  Textarea,
  Toggle,
} from "../components/ui";
import { PortalOrders } from "./payments/PortalOrders";
import { Panel, REFETCH_MS, TH, THEAD, dateID, errorText, idr } from "./payments/shared";

const TABS = ["paket", "pelanggan", "order"] as const;
type Tab = (typeof TABS)[number];
const TABS_ID = "token-package-tabs";

const QK_PACKAGES = ["token-packages"] as const;
const QK_SETTINGS = ["token-settings"] as const;
const QK_SUBSCRIPTIONS = ["token-subscriptions"] as const;

const DESCRIPTION: Record<Tab, string> = {
  paket: "Paket token GPT-6 yang dijual di portal: harga, jumlah token, masa aktif, dan batas pemakaian.",
  pelanggan: "Pemilik key paket token: sisa token, masa aktif, dan tombol untuk menonaktifkan key.",
  order: "Pembayaran paket token dari portal. Setujui manual jika gateway melewatkan pembayaran.",
};

const nf = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 1 });

/** 100000000 becomes "100 jt", 1000000000 becomes "1 M" (miliar). */
function tokenLabel(n: number): string {
  if (n >= 1e9) return `${nf.format(n / 1e9)} M`;
  if (n >= 1e6) return `${nf.format(n / 1e6)} jt`;
  if (n >= 1e3) return `${nf.format(n / 1e3)} rb`;
  return nf.format(n);
}

/** Price of one million tokens; days packages have no token count to divide by. */
function pricePerMillion(p: Pick<TokenPackage, "kind" | "tokens" | "price_idr">): string {
  if (p.kind !== "tokens" || p.tokens <= 0) return "-";
  return idr(Math.round(p.price_idr / (p.tokens / 1e6)));
}

function packageInput(p: TokenPackage, patch: Partial<TokenPackageInput> = {}): TokenPackageInput {
  return {
    name: p.name,
    kind: p.kind,
    tokens: p.tokens,
    days: p.days,
    price_idr: p.price_idr,
    enabled: p.enabled,
    featured: p.featured,
    sort: p.sort,
    ...patch,
  };
}

export function TokenPackagesPage() {
  const [tab, setTab] = useState<Tab>("paket");
  return (
    <div className="space-y-6">
      <PageHeader title="Paket Token" description={DESCRIPTION[tab]} />
      <TabBar
        idPrefix={TABS_ID}
        label="Bagian Paket Token"
        active={tab}
        onChange={setTab}
        tabs={[
          { value: "paket", label: "Paket", icon: Package },
          { value: "pelanggan", label: "Pelanggan", icon: Users },
          { value: "order", label: "Order", icon: ShoppingCart },
        ]}
      />
      <TabPanel idPrefix={TABS_ID} value={tab} className="space-y-6">
        {tab === "paket" ? (
          <>
            <SettingsPanel />
            <PackagesPanel />
          </>
        ) : tab === "pelanggan" ? (
          <SubscribersPanel />
        ) : (
          <PortalOrders product="token" />
        )}
      </TabPanel>
    </div>
  );
}

// ─── Pengaturan produk ──────────────────────────────────────────────────────

function SettingsPanel() {
  const settings = useQuery({ queryKey: QK_SETTINGS, queryFn: api.getTokenSettings });
  return (
    <Panel
      icon={Settings2}
      title="Pengaturan produk"
      subtitle="Saklar penjualan, model yang boleh dipakai key paket token, dan batas kecepatan."
    >
      {settings.isLoading ? (
        <LoadingRegion label="Memuat pengaturan paket token" className="space-y-3 p-4">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-20 w-full" />
        </LoadingRegion>
      ) : settings.isError || !settings.data ? (
        <div className="p-4">
          <ErrorBanner message={`Pengaturan tidak bisa dimuat. ${errorText(settings.error)}`} />
        </div>
      ) : (
        <SettingsForm initial={settings.data} />
      )}
    </Panel>
  );
}

function SettingsForm({ initial }: { initial: TokenSettings }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [enabled, setEnabled] = useState(initial.enabled);
  const [models, setModels] = useState(initial.allowed_models.join("\n"));
  const [rpm, setRpm] = useState(String(initial.rpm_limit));
  const [concurrency, setConcurrency] = useState(String(initial.concurrency_limit));
  const [touched, setTouched] = useState(false);

  const modelList = models
    .split(/[\n,]+/)
    .map((m) => m.trim())
    .filter(Boolean);
  const rpmNum = Number(rpm);
  const concNum = Number(concurrency);
  const modelsError = touched && modelList.length === 0 ? "Isi minimal satu model." : null;
  const rpmError = touched && (rpm.trim() === "" || !Number.isInteger(rpmNum) || rpmNum < 0) ? "Isi 0 (tanpa batas) atau lebih." : null;
  const concError =
    touched && (concurrency.trim() === "" || !Number.isInteger(concNum) || concNum < 0) ? "Isi 0 (tanpa batas) atau lebih." : null;

  const save = useMutation({
    mutationFn: () =>
      api.saveTokenSettings({ enabled, allowed_models: modelList, rpm_limit: rpmNum, concurrency_limit: concNum }),
    onSuccess: (saved) => {
      toast.success("Pengaturan disimpan");
      qc.setQueryData(QK_SETTINGS, saved);
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    const ok =
      modelList.length > 0 &&
      rpm.trim() !== "" &&
      Number.isInteger(rpmNum) &&
      rpmNum >= 0 &&
      concurrency.trim() !== "" &&
      Number.isInteger(concNum) &&
      concNum >= 0;
    if (!ok || save.isPending) return;
    save.mutate();
  };

  return (
    <form onSubmit={onSubmit} noValidate className="space-y-4 p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <p id="token-sale-switch" className="text-[13px] font-medium text-fg">
            Penjualan dibuka
          </p>
          <p className="text-[12px] text-fg-muted">
            Saat mati, portal tidak menerima order paket token baru. Key yang sudah terbit tetap jalan sampai habis atau kedaluwarsa.
          </p>
        </div>
        <Toggle checked={enabled} onChange={setEnabled} aria-labelledby="token-sale-switch" />
      </div>
      <Field
        label="Model yang diizinkan"
        error={modelsError}
        hint={modelsError ? undefined : "Satu model per baris. Key paket token hanya bisa memanggil model di daftar ini."}
        required
      >
        <Textarea value={models} onChange={(e) => setModels(e.target.value)} rows={3} spellCheck={false} className="font-mono" />
      </Field>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field label="Batas permintaan per menit" error={rpmError} hint={rpmError ? undefined : "0 = tanpa batas. Berlaku per key."} required>
          <Input type="number" inputMode="numeric" min="0" step="1" value={rpm} onChange={(e) => setRpm(e.target.value)} className="tabular-nums" />
        </Field>
        <Field
          label="Batas permintaan bersamaan"
          error={concError}
          hint={concError ? undefined : "0 = tanpa batas. Jumlah permintaan yang berjalan sekaligus per key."}
          required
        >
          <Input
            type="number"
            inputMode="numeric"
            min="0"
            step="1"
            value={concurrency}
            onChange={(e) => setConcurrency(e.target.value)}
            className="tabular-nums"
          />
        </Field>
      </div>
      {save.isError && <ErrorBanner message={`Pengaturan gagal disimpan. ${errorText(save.error)}`} />}
      <div className="flex justify-end">
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Check aria-hidden="true" />}
          Simpan pengaturan
        </Button>
      </div>
    </form>
  );
}

// ─── Daftar paket ───────────────────────────────────────────────────────────

function PackagesPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [editing, setEditing] = useState<TokenPackage | "new" | null>(null);
  const packages = useQuery({ queryKey: QK_PACKAGES, queryFn: api.listTokenPackages });

  const setEnabled = useMutation({
    mutationFn: ({ pkg, enabled }: { pkg: TokenPackage; enabled: boolean }) =>
      api.updateTokenPackage(pkg.id, packageInput(pkg, { enabled })),
    onSuccess: (saved) => {
      toast.success(saved.enabled ? "Paket dijual" : "Paket disembunyikan", saved.name);
      qc.invalidateQueries({ queryKey: QK_PACKAGES });
    },
    onError: (e) => toast.error("Status paket gagal diubah", errorText(e)),
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteTokenPackage(id),
    onSuccess: () => {
      toast.success("Paket dihapus");
      qc.invalidateQueries({ queryKey: QK_PACKAGES });
    },
    onError: (e) => toast.error("Paket gagal dihapus", errorText(e)),
  });

  const onDelete = async (p: TokenPackage) => {
    const ok = await confirm({
      title: `Hapus paket ${p.name}?`,
      description: "Paket hilang dari portal. Key yang sudah dibeli dan riwayat order tidak berubah.",
      confirmLabel: "Hapus paket",
      cancelLabel: "Batal",
      tone: "danger",
    });
    if (ok) remove.mutate(p.id);
  };

  const list = packages.data ?? [];

  return (
    <Panel
      icon={Package}
      title="Daftar paket"
      subtitle="Urutan di portal mengikuti kolom Urutan, angka kecil tampil lebih dulu."
      action={
        <Button onClick={() => setEditing("new")}>
          <Plus aria-hidden="true" />
          Tambah paket
        </Button>
      }
    >
      {packages.isLoading ? (
        <LoadingRegion label="Memuat daftar paket" className="space-y-2 p-4">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </LoadingRegion>
      ) : packages.isError ? (
        <div className="p-4">
          <ErrorBanner message={`Daftar paket tidak bisa dimuat. ${errorText(packages.error)}`} />
        </div>
      ) : list.length === 0 ? (
        <EmptyState icon={Package} size="sm" title="Belum ada paket" hint="Tambah paket pertama agar pelanggan bisa membeli token di portal." />
      ) : (
        <div className="overflow-x-auto" role="region" aria-label="Tabel paket token" tabIndex={0}>
          <table className="w-full min-w-[880px] text-[13px]">
            <thead>
              <tr className={THEAD}>
                <th scope="col" className={TH}>Nama</th>
                <th scope="col" className={TH}>Jenis</th>
                <th scope="col" className={cn(TH, "text-right")}>Token</th>
                <th scope="col" className={cn(TH, "text-right")}>Masa aktif</th>
                <th scope="col" className={cn(TH, "text-right")}>Harga</th>
                <th scope="col" className={cn(TH, "text-right")}>Per 1 jt token</th>
                <th scope="col" className={cn(TH, "text-right")}>Urutan</th>
                <th scope="col" className={TH}>Dijual</th>
                <th scope="col" className={cn(TH, "text-right")}>
                  <span className="sr-only">Aksi</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {list.map((p) => {
                const toggling = setEnabled.isPending && setEnabled.variables?.pkg.id === p.id;
                const deleting = remove.isPending && remove.variables === p.id;
                return (
                  <tr key={p.id} className="transition-colors hover:bg-hover">
                    <td className="px-4 py-2.5">
                      <span className="flex flex-wrap items-center gap-1.5 font-medium text-fg">
                        {p.name}
                        {p.featured && <Badge tone="accent">Unggulan</Badge>}
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-fg-muted">{p.kind === "tokens" ? "Kuota token" : "Harian"}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">{p.kind === "tokens" ? tokenLabel(p.tokens) : "-"}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">{p.days} hari</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right font-semibold tabular-nums">{idr(p.price_idr)}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums text-fg-muted">{pricePerMillion(p)}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums text-fg-muted">{p.sort}</td>
                    <td className="px-4 py-2.5">
                      <Toggle
                        checked={p.enabled}
                        disabled={toggling}
                        onChange={(enabled) => setEnabled.mutate({ pkg: p, enabled })}
                        aria-label={`${p.enabled ? "Sembunyikan" : "Jual"} paket ${p.name}`}
                      />
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" onClick={() => setEditing(p)} title="Ubah paket" aria-label={`Ubah ${p.name}`}>
                          <Pencil aria-hidden="true" />
                        </Button>
                        <Button variant="ghost" onClick={() => onDelete(p)} disabled={deleting} title="Hapus paket" aria-label={`Hapus ${p.name}`}>
                          {deleting ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Trash2 aria-hidden="true" />}
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {editing && <PackageModal pkg={editing === "new" ? null : editing} onClose={() => setEditing(null)} />}
    </Panel>
  );
}

function PackageModal({ pkg, onClose }: { pkg: TokenPackage | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState(pkg?.name ?? "");
  const [kind, setKind] = useState<TokenPackageKind>(pkg?.kind ?? "tokens");
  const [millions, setMillions] = useState(pkg && pkg.kind === "tokens" ? String(pkg.tokens / 1e6) : "");
  const [days, setDays] = useState(pkg ? String(pkg.days) : "30");
  const [price, setPrice] = useState(pkg ? String(pkg.price_idr) : "");
  const [sort, setSort] = useState(pkg ? String(pkg.sort) : "0");
  const [enabled, setEnabled] = useState(pkg?.enabled ?? true);
  const [featured, setFeatured] = useState(pkg?.featured ?? false);
  const [touched, setTouched] = useState(false);

  const millionsNum = Number(millions);
  const daysNum = Number(days);
  const priceNum = Number(price);
  const sortNum = Number(sort);
  const nameError = touched && !name.trim() ? "Nama paket wajib diisi." : null;
  const tokensError = touched && kind === "tokens" && !(millionsNum > 0) ? "Isi jumlah token lebih dari 0." : null;
  const daysError = touched && (!Number.isInteger(daysNum) || daysNum < 1) ? "Isi bilangan bulat minimal 1." : null;
  const priceError = touched && (!Number.isInteger(priceNum) || priceNum <= 0) ? "Isi harga dalam Rupiah, lebih dari 0." : null;
  const sortError = touched && (sort.trim() === "" || !Number.isInteger(sortNum)) ? "Isi bilangan bulat." : null;

  const save = useMutation({
    mutationFn: (body: TokenPackageInput) => (pkg ? api.updateTokenPackage(pkg.id, body) : api.createTokenPackage(body)),
    onSuccess: (saved) => {
      toast.success(pkg ? "Paket diperbarui" : "Paket ditambahkan", saved.name);
      qc.invalidateQueries({ queryKey: QK_PACKAGES });
      onClose();
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    const ok =
      name.trim() !== "" &&
      (kind === "days" || millionsNum > 0) &&
      Number.isInteger(daysNum) &&
      daysNum >= 1 &&
      Number.isInteger(priceNum) &&
      priceNum > 0 &&
      sort.trim() !== "" &&
      Number.isInteger(sortNum);
    if (!ok || save.isPending) return;
    save.mutate({
      name: name.trim(),
      kind,
      tokens: kind === "tokens" ? Math.round(millionsNum * 1e6) : 0,
      days: daysNum,
      price_idr: priceNum,
      enabled,
      featured,
      sort: sortNum,
    });
  };

  const perMillion =
    kind === "tokens" && millionsNum > 0 && priceNum > 0 ? pricePerMillion({ kind, tokens: millionsNum * 1e6, price_idr: priceNum }) : null;

  return (
    <Modal open onClose={save.isPending ? () => {} : onClose} title={pkg ? `Ubah paket ${pkg.name}` : "Tambah paket"} subtitle="Paket token GPT-6">
      <form onSubmit={onSubmit} noValidate>
        <div className="space-y-4 px-5 py-4">
          <Field label="Nama paket" error={nameError} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={80} autoComplete="off" autoFocus />
          </Field>
          <Field
            label="Jenis"
            hint={kind === "tokens" ? "Pelanggan membeli sejumlah token yang berlaku selama masa aktif." : "Pelanggan membeli akses selama masa aktif, tanpa kuota token."}
          >
            <Select value={kind} onChange={(e) => setKind(e.target.value as TokenPackageKind)}>
              <option value="tokens">Kuota token</option>
              <option value="days">Harian</option>
            </Select>
          </Field>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {kind === "tokens" && (
              <Field label="Jumlah token (juta)" error={tokensError} hint={tokensError ? undefined : "100 berarti 100 jt token."} required>
                <Input
                  type="number"
                  inputMode="decimal"
                  min="0"
                  step="any"
                  value={millions}
                  onChange={(e) => setMillions(e.target.value)}
                  className="tabular-nums"
                />
              </Field>
            )}
            <Field label="Masa aktif (hari)" error={daysError} required>
              <Input type="number" inputMode="numeric" min="1" step="1" value={days} onChange={(e) => setDays(e.target.value)} className="tabular-nums" />
            </Field>
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field
              label="Harga (Rp)"
              error={priceError}
              hint={priceError ? undefined : perMillion ? `${perMillion} per 1 jt token` : priceNum > 0 ? idr(priceNum) : undefined}
              required
            >
              <Input type="number" inputMode="numeric" min="1" step="1" value={price} onChange={(e) => setPrice(e.target.value)} className="tabular-nums" />
            </Field>
            <Field label="Urutan" error={sortError} hint={sortError ? undefined : "Angka kecil tampil lebih dulu."}>
              <Input type="number" inputMode="numeric" step="1" value={sort} onChange={(e) => setSort(e.target.value)} className="tabular-nums" />
            </Field>
          </div>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p id="pkg-enabled" className="text-[13px] font-medium text-fg">
                Dijual
              </p>
              <p className="text-[12px] text-fg-muted">Jika mati, paket tidak tampil di portal.</p>
            </div>
            <Toggle checked={enabled} onChange={setEnabled} aria-labelledby="pkg-enabled" />
          </div>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p id="pkg-featured" className="text-[13px] font-medium text-fg">
                Tandai unggulan
              </p>
              <p className="text-[12px] text-fg-muted">Paket diberi label Unggulan di portal.</p>
            </div>
            <Toggle checked={featured} onChange={setFeatured} aria-labelledby="pkg-featured" />
          </div>
          {save.isError && <ErrorBanner message={`Paket gagal disimpan. ${errorText(save.error)}`} />}
        </div>
        <ModalFooter>
          <Button type="button" variant="ghost" onClick={onClose} disabled={save.isPending}>
            Batal
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Check aria-hidden="true" />}
            {pkg ? "Simpan" : "Tambah"}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  );
}

// ─── Pelanggan ──────────────────────────────────────────────────────────────

function subscriptionStatus(s: TokenSubscription): { label: string; tone: "success" | "danger" | "neutral" } {
  if (s.disabled) return { label: "Key dinonaktifkan", tone: "danger" };
  if (!s.active) return { label: "Tidak aktif", tone: "neutral" };
  return { label: "Aktif", tone: "success" };
}

function SubscribersPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const subs = useQuery({ queryKey: QK_SUBSCRIPTIONS, queryFn: api.listTokenSubscriptions, refetchInterval: REFETCH_MS });

  // The endpoint flips the current state, so a second request in flight could
  // undo the first. One toggle at a time keeps the result predictable.
  const toggle = useMutation({
    mutationFn: (sub: string) => api.toggleTokenKey(sub),
    onSuccess: (res) => {
      toast.success(res.disabled ? "Key dinonaktifkan" : "Key diaktifkan lagi");
      qc.invalidateQueries({ queryKey: QK_SUBSCRIPTIONS });
    },
    onError: (e) => toast.error("Status key gagal diubah", errorText(e)),
  });

  const list = subs.data ?? [];

  return (
    <Panel icon={Users} title="Pelanggan paket token" subtitle="Sisa token dihitung dari batas paket dikurangi pemakaian sejak key terbit.">
      {subs.isLoading ? (
        <LoadingRegion label="Memuat pelanggan" className="space-y-2 p-4">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </LoadingRegion>
      ) : subs.isError && !subs.data ? (
        <div className="p-4">
          <ErrorBanner message={`Daftar pelanggan tidak bisa dimuat. ${errorText(subs.error)}`} />
        </div>
      ) : list.length === 0 ? (
        <EmptyState icon={Users} size="sm" title="Belum ada pelanggan" hint="Pemilik key paket token muncul di sini setelah pembayaran pertama selesai." />
      ) : (
        <div className="overflow-x-auto" role="region" aria-label="Tabel pelanggan paket token" tabIndex={0}>
          <table className="w-full min-w-[800px] text-[13px]">
            <thead>
              <tr className={THEAD}>
                <th scope="col" className={TH}>Pelanggan</th>
                <th scope="col" className={TH}>Key</th>
                <th scope="col" className={cn(TH, "text-right")}>Sisa token</th>
                <th scope="col" className={TH}>Berlaku sampai</th>
                <th scope="col" className={TH}>Status</th>
                <th scope="col" className={cn(TH, "text-right")}>
                  <span className="sr-only">Aksi</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {list.map((s) => {
                const status = subscriptionStatus(s);
                const remaining = Math.max(0, s.limit_tokens - s.used_tokens);
                const busy = toggle.isPending && toggle.variables === s.google_sub;
                return (
                  <tr key={s.google_sub} className="transition-colors hover:bg-hover">
                    <td className="px-4 py-2.5 text-fg">{s.email || s.google_sub}</td>
                    <td className="px-4 py-2.5 font-mono text-[12px] text-fg-muted">{s.key_display || s.key_id}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">
                      <span className="font-semibold">{tokenLabel(remaining)}</span>
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 tabular-nums text-fg-muted">{dateID(s.expires_at, true)}</td>
                    <td className="px-4 py-2.5">
                      <Badge tone={status.tone}>{status.label}</Badge>
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex justify-end">
                        <Button
                          variant={s.disabled ? "ghost" : "danger"}
                          onClick={() => toggle.mutate(s.google_sub)}
                          disabled={toggle.isPending}
                          title={s.disabled ? "Aktifkan key lagi" : "Nonaktifkan key"}
                        >
                          {busy ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Power aria-hidden="true" />}
                          {s.disabled ? "Aktifkan" : "Nonaktifkan"}
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}
