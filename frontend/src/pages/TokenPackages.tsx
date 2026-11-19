import { useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BarChart3, Check, Coins, History, LayoutDashboard, Loader2, Package, Pencil, Plus, Power, Receipt, Search, Settings2, ShoppingCart, SlidersHorizontal, Trash2, Users, Wallet } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  api,
  type TokenOverview,
  type TokenPackage,
  type TokenPackageInput,
  type TokenPackageKind,
  type TokenSettings,
  type TokenSubscriberOrder,
  type TokenSubscription,
  type TokenUpstreamCost,
} from "../lib/api";
import { formatDateTime } from "../lib/format";
import { formatTokens } from "../lib/format";
import { PageHeader } from "../components/Layout";
import { ChartCard, TimeBars, dayLabel } from "../components/charts/TimeSeries";
import { useToast } from "../components/Toast";
import { useConfirm } from "../components/ui/confirm-dialog";
import {
  Badge,
  Button,
  EmptyState,
  ErrorBanner,
  Field,
  Input,
  Kpi,
  KpiGrid,
  LoadingRegion,
  Modal,
  ModalFooter,
  Select,
  SegmentedControl,
  Skeleton,
  TabBar,
  TablePagination,
  TabPanel,
  Toggle,
  useClientPagination,
} from "../components/ui";
import { ORDER_STATUS_LABEL, ORDER_STATUS_TONE, PortalOrders } from "./payments/PortalOrders";
import { Panel, REFETCH_MS, TH, THEAD, count, dateID, errorText, idr } from "./payments/shared";

const TABS = ["overview", "packages", "subscribers", "orders"] as const;
type Tab = (typeof TABS)[number];
const TABS_ID = "token-package-tabs";

const QK_PACKAGES = ["token-packages"] as const;
const QK_SETTINGS = ["token-settings"] as const;
const QK_SUBSCRIPTIONS = ["token-subscriptions"] as const;
const QK_OVERVIEW = ["token-overview"] as const;

/** Every digit, Indonesian grouping: 50.000.000, so an operator sees the exact balance. */
function exactTokens(n: number): string {
  return n.toLocaleString("id-ID");
}

const DESCRIPTION: Record<Tab, string> = {
  overview: "Unspent tokens, subscriber count and daily token usage across all token package keys.",
  packages: "GPT-6 token packages sold in the portal: price, token amount, validity and rate limits.",
  subscribers: "Owners of token package keys: remaining tokens, expiry, a balance correction, and a switch to disable a key.",
  orders: "Token package payments from the portal. Approve one manually if the gateway missed it.",
};

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
  const [tab, setTab] = useState<Tab>("overview");
  return (
    <div className="space-y-6">
      <PageHeader title="Token Packages" description={DESCRIPTION[tab]} />
      <TabBar
        idPrefix={TABS_ID}
        label="Token package sections"
        active={tab}
        onChange={setTab}
        tabs={[
          { value: "overview", label: "Overview", icon: LayoutDashboard },
          { value: "packages", label: "Packages", icon: Package },
          { value: "subscribers", label: "Subscribers", icon: Users },
          { value: "orders", label: "Orders", icon: ShoppingCart },
        ]}
      />
      <TabPanel idPrefix={TABS_ID} value={tab} className="space-y-6">
        {tab === "overview" ? (
          <OverviewPanel />
        ) : tab === "packages" ? (
          <>
            <SettingsPanel />
            <PackagesPanel />
          </>
        ) : tab === "subscribers" ? (
          <SubscribersPanel />
        ) : (
          <PortalOrders product="token" searchable />
        )}
      </TabPanel>
    </div>
  );
}

// ─── Product settings ───────────────────────────────────────────────────────

function SettingsPanel() {
  const settings = useQuery({ queryKey: QK_SETTINGS, queryFn: api.getTokenSettings });
  return (
    <Panel
      icon={Settings2}
      title="Product settings"
      subtitle="Sale switch, the models token package keys may call, and rate limits."
    >
      {settings.isLoading ? (
        <LoadingRegion label="Loading token package settings" className="space-y-3 p-4">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-20 w-full" />
        </LoadingRegion>
      ) : settings.isError || !settings.data ? (
        <div className="p-4">
          <ErrorBanner message={`Couldn't load settings. ${errorText(settings.error)}`} />
        </div>
      ) : (
        <SettingsForm initial={settings.data} />
      )}
    </Panel>
  );
}

/** Chain whitelist picker. Values are chain names, which is what token keys
 *  are matched against (case-insensitive). Saved names with no matching chain
 *  stay listed so the admin can see and remove them. */
function ChainPicker({ value, onChange }: { value: string[]; onChange: (next: string[]) => void }) {
  const [query, setQuery] = useState("");
  const chains = useQuery({ queryKey: ["chains"], queryFn: api.listChains });
  const selected = new Set(value.map((v) => v.toLowerCase()));
  const all = (chains.data?.chains ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));
  const known = new Set(all.map((c) => c.name.toLowerCase()));
  const missing = value.filter((v) => !known.has(v.toLowerCase()));
  const q = query.trim().toLowerCase();
  const shown = q ? all.filter((c) => c.name.toLowerCase().includes(q)) : all;

  const toggle = (name: string) => {
    const key = name.toLowerCase();
    onChange(selected.has(key) ? value.filter((v) => v.toLowerCase() !== key) : [...value, name]);
  };

  if (chains.isLoading) return <Skeleton className="h-32 w-full" />;
  if (chains.isError) return <ErrorBanner message={`Couldn't load chains. ${errorText(chains.error)}`} />;

  return (
    <div className="rounded-md border border-line">
      <div className="flex items-center gap-2 border-b border-line p-2">
        <Input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search chains"
          aria-label="Search chains"
          className="h-8"
        />
        <span className="shrink-0 text-[12px] tabular-nums text-fg-muted">{value.length} selected</span>
      </div>
      {all.length === 0 ? (
        <p className="p-3 text-[12.5px] text-fg-muted">No chains yet. Create a chain first, then pick it here.</p>
      ) : (
        <ul className="max-h-60 divide-y divide-line overflow-y-auto" aria-label="Chains">
          {shown.length === 0 && <li className="p-3 text-[12.5px] text-fg-muted">No chain matches “{query}”.</li>}
          {shown.map((c) => (
            <li key={c.id}>
              <label className="flex min-h-10 cursor-pointer items-center gap-2.5 px-3 py-1.5 text-[13px] hover:bg-subtle">
                <input
                  type="checkbox"
                  checked={selected.has(c.name.toLowerCase())}
                  onChange={() => toggle(c.name)}
                  className="h-4 w-4 shrink-0 rounded border-input accent-accent-500"
                />
                <span className="min-w-0 flex-1 truncate font-mono">{c.name}</span>
                {c.disabled ? <Badge tone="danger">Off sale</Badge> : c.sold_out ? <Badge tone="warning">Sold out</Badge> : null}
                <span className="shrink-0 text-[11.5px] text-fg-faint">
                  {c.steps.length} {c.steps.length === 1 ? "step" : "steps"}
                </span>
              </label>
            </li>
          ))}
        </ul>
      )}
      {missing.length > 0 && (
        <div className="border-t border-line p-3">
          <p className="mb-1.5 text-[12px] text-fg-muted">Saved but no chain has this name (keys can't use it):</p>
          <div className="flex flex-wrap gap-1.5">
            {missing.map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => onChange(value.filter((v) => v !== m))}
                className="inline-flex min-h-8 items-center gap-1 rounded-md border border-line px-2 font-mono text-[12px] text-fg hover:bg-subtle"
                aria-label={`Remove ${m}`}
              >
                {m}
                <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function SettingsForm({ initial }: { initial: TokenSettings }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [enabled, setEnabled] = useState(initial.enabled);
  const [modelList, setModelList] = useState<string[]>(initial.allowed_models);
  const [rpm, setRpm] = useState(String(initial.rpm_limit));
  const [concurrency, setConcurrency] = useState(String(initial.concurrency_limit));
  const [touched, setTouched] = useState(false);

  const rpmNum = Number(rpm);
  const concNum = Number(concurrency);
  const modelsError = touched && modelList.length === 0 ? "Pick at least one chain." : null;
  const rpmError = touched && (rpm.trim() === "" || !Number.isInteger(rpmNum) || rpmNum < 1) ? "Enter 1 or more." : null;
  const concError =
    touched && (concurrency.trim() === "" || !Number.isInteger(concNum) || concNum < 1) ? "Enter 1 or more." : null;

  const save = useMutation({
    mutationFn: () =>
      api.saveTokenSettings({ enabled, allowed_models: modelList, rpm_limit: rpmNum, concurrency_limit: concNum }),
    onSuccess: (saved) => {
      toast.success("Settings saved");
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
      rpmNum >= 1 &&
      concurrency.trim() !== "" &&
      Number.isInteger(concNum) &&
      concNum >= 1;
    if (!ok || save.isPending) return;
    save.mutate();
  };

  return (
    <form onSubmit={onSubmit} noValidate className="space-y-4 p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <p id="token-sale-switch" className="text-[13px] font-medium text-fg">
            Sales open
          </p>
          <p className="text-[12px] text-fg-muted">
            When off, the portal stops taking new token package orders. Keys already issued keep working until they run out or expire.
          </p>
        </div>
        <Toggle checked={enabled} onChange={setEnabled} aria-labelledby="token-sale-switch" />
      </div>
      <Field
        label="Allowed models"
        error={modelsError}
        hint={modelsError ? undefined : "Token package keys can only call the chains ticked here."}
        required
      >
        <ChainPicker value={modelList} onChange={setModelList} />
      </Field>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field label="Requests per minute" error={rpmError} hint={rpmError ? undefined : "Applies per key."} required>
          <Input type="number" inputMode="numeric" min="1" step="1" value={rpm} onChange={(e) => setRpm(e.target.value)} className="tabular-nums" />
        </Field>
        <Field
          label="Concurrent requests"
          error={concError}
          hint={concError ? undefined : "Requests running at the same time per key."}
          required
        >
          <Input
            type="number"
            inputMode="numeric"
            min="1"
            step="1"
            value={concurrency}
            onChange={(e) => setConcurrency(e.target.value)}
            className="tabular-nums"
          />
        </Field>
      </div>
      {save.isError && <ErrorBanner message={`Couldn't save settings. ${errorText(save.error)}`} />}
      <div className="flex justify-end">
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Check aria-hidden="true" />}
          Save settings
        </Button>
      </div>
    </form>
  );
}

// ─── Package list ───────────────────────────────────────────────────────────

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
      toast.success(saved.enabled ? "Package on sale" : "Package hidden", saved.name);
      qc.invalidateQueries({ queryKey: QK_PACKAGES });
    },
    onError: (e) => toast.error("Couldn't change package status", errorText(e)),
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteTokenPackage(id),
    onSuccess: () => {
      toast.success("Package deleted");
      qc.invalidateQueries({ queryKey: QK_PACKAGES });
    },
    onError: (e) => toast.error("Couldn't delete package", errorText(e)),
  });

  const onDelete = async (p: TokenPackage) => {
    const ok = await confirm({
      title: `Delete package ${p.name}?`,
      description: "The package disappears from the portal. Keys already bought and order history are unchanged.",
      confirmLabel: "Delete package",
      cancelLabel: "Cancel",
      tone: "danger",
    });
    if (ok) remove.mutate(p.id);
  };

  const list = packages.data ?? [];

  return (
    <Panel
      icon={Package}
      title="Package list"
      subtitle="The portal orders packages by the Sort column, lowest first."
      action={
        <Button onClick={() => setEditing("new")}>
          <Plus aria-hidden="true" />
          Add package
        </Button>
      }
    >
      {packages.isLoading ? (
        <LoadingRegion label="Loading packages" className="space-y-2 p-4">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </LoadingRegion>
      ) : packages.isError ? (
        <div className="p-4">
          <ErrorBanner message={`Couldn't load packages. ${errorText(packages.error)}`} />
        </div>
      ) : list.length === 0 ? (
        <EmptyState icon={Package} size="sm" title="No packages yet" hint="Add the first package so customers can buy tokens in the portal." />
      ) : (
        <div className="overflow-x-auto" role="region" aria-label="Token packages table" tabIndex={0}>
          <table className="w-full min-w-[880px] text-[13px]">
            <thead>
              <tr className={THEAD}>
                <th scope="col" className={TH}>Name</th>
                <th scope="col" className={TH}>Type</th>
                <th scope="col" className={cn(TH, "text-right")}>Tokens</th>
                <th scope="col" className={cn(TH, "text-right")}>Validity</th>
                <th scope="col" className={cn(TH, "text-right")}>Price</th>
                <th scope="col" className={cn(TH, "text-right")}>Per 1M tokens</th>
                <th scope="col" className={cn(TH, "text-right")}>Sort</th>
                <th scope="col" className={TH}>On sale</th>
                <th scope="col" className={cn(TH, "text-right")}>
                  <span className="sr-only">Actions</span>
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
                        {p.featured && <Badge tone="accent">Recommended</Badge>}
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-fg-muted">{p.kind === "tokens" ? "Token quota" : "Daily"}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">{p.kind === "tokens" ? formatTokens(p.tokens) : "-"}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">{p.days} days</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right font-semibold tabular-nums">{idr(p.price_idr)}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums text-fg-muted">{pricePerMillion(p)}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums text-fg-muted">{p.sort}</td>
                    <td className="px-4 py-2.5">
                      <Toggle
                        checked={p.enabled}
                        disabled={toggling}
                        onChange={(enabled) => setEnabled.mutate({ pkg: p, enabled })}
                        aria-label={`${p.enabled ? "Hide" : "Sell"} package ${p.name}`}
                      />
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" onClick={() => setEditing(p)} title="Edit package" aria-label={`Edit ${p.name}`}>
                          <Pencil aria-hidden="true" />
                        </Button>
                        <Button variant="ghost" onClick={() => onDelete(p)} disabled={deleting} title="Delete package" aria-label={`Delete ${p.name}`}>
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
  const nameError = touched && !name.trim() ? "Package name is required." : null;
  const tokensError = touched && kind === "tokens" && !(millionsNum > 0) ? "Enter a token amount above 0." : null;
  const daysError = touched && (!Number.isInteger(daysNum) || daysNum < 1) ? "Enter a whole number, 1 or more." : null;
  const priceError = touched && (!Number.isInteger(priceNum) || priceNum <= 0) ? "Enter a price in Rupiah above 0." : null;
  const sortError = touched && (sort.trim() === "" || !Number.isInteger(sortNum)) ? "Enter a whole number." : null;

  const save = useMutation({
    mutationFn: (body: TokenPackageInput) => (pkg ? api.updateTokenPackage(pkg.id, body) : api.createTokenPackage(body)),
    onSuccess: (saved) => {
      toast.success(pkg ? "Package updated" : "Package added", saved.name);
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
    <Modal open onClose={save.isPending ? () => {} : onClose} title={pkg ? `Edit package ${pkg.name}` : "Add package"} subtitle="GPT-6 token package">
      <form onSubmit={onSubmit} noValidate>
        <div className="space-y-4 px-5 py-4">
          <Field label="Package name" error={nameError} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={80} autoComplete="off" autoFocus />
          </Field>
          <Field
            label="Type"
            hint={kind === "tokens" ? "Customers buy a token amount that is valid for the validity period." : "Customers buy access for the validity period, with no token quota."}
          >
            <Select value={kind} onChange={(e) => setKind(e.target.value as TokenPackageKind)}>
              <option value="tokens">Token quota</option>
              <option value="days">Daily</option>
            </Select>
          </Field>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {kind === "tokens" && (
              <Field label="Tokens (millions)" error={tokensError} hint={tokensError ? undefined : "100 means 100M tokens."} required>
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
            <Field label="Validity (days)" error={daysError} required>
              <Input type="number" inputMode="numeric" min="1" step="1" value={days} onChange={(e) => setDays(e.target.value)} className="tabular-nums" />
            </Field>
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field
              label="Price (IDR)"
              error={priceError}
              hint={priceError ? undefined : perMillion ? `${perMillion} per 1M tokens` : priceNum > 0 ? idr(priceNum) : undefined}
              required
            >
              <Input type="number" inputMode="numeric" min="1" step="1" value={price} onChange={(e) => setPrice(e.target.value)} className="tabular-nums" />
            </Field>
            <Field label="Sort" error={sortError} hint={sortError ? undefined : "Lowest shows first."}>
              <Input type="number" inputMode="numeric" step="1" value={sort} onChange={(e) => setSort(e.target.value)} className="tabular-nums" />
            </Field>
          </div>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p id="pkg-enabled" className="text-[13px] font-medium text-fg">
                On sale
              </p>
              <p className="text-[12px] text-fg-muted">When off, the package is hidden from the portal.</p>
            </div>
            <Toggle checked={enabled} onChange={setEnabled} aria-labelledby="pkg-enabled" />
          </div>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p id="pkg-featured" className="text-[13px] font-medium text-fg">
                Mark as recommended
              </p>
              <p className="text-[12px] text-fg-muted">The portal and the public page show a Recommended label on this package.</p>
            </div>
            <Toggle checked={featured} onChange={setFeatured} aria-labelledby="pkg-featured" />
          </div>
          {save.isError && <ErrorBanner message={`Couldn't save package. ${errorText(save.error)}`} />}
        </div>
        <ModalFooter>
          <Button type="button" variant="ghost" onClick={onClose} disabled={save.isPending}>
            Cancel
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Check aria-hidden="true" />}
            {pkg ? "Save" : "Add"}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  );
}

// ─── Overview ───────────────────────────────────────────────────────────────

// Days arrive as UTC dates; midnight UTC falls on the same date in the
// server zone the chart labels use.
const dayMs = (d: { day: string }) => Date.parse(`${d.day}T00:00:00Z`);

function OverviewPanel() {
  const overview = useQuery({ queryKey: QK_OVERVIEW, queryFn: api.getTokenOverview, refetchInterval: REFETCH_MS });

  if (overview.isLoading) {
    return (
      <LoadingRegion label="Loading token package overview" className="space-y-4">
        <Skeleton className="h-[104px] w-full rounded-2xl" />
        <Skeleton className="h-[260px] w-full rounded-2xl" />
      </LoadingRegion>
    );
  }
  if (!overview.data) {
    return <ErrorBanner message={`Couldn't load the overview. ${errorText(overview.error)}`} />;
  }
  return <OverviewBody o={overview.data} stale={overview.isError ? errorText(overview.error) : null} />;
}

function OverviewBody({ o, stale }: { o: TokenOverview; stale: string | null }) {
  return (
    <div className="space-y-4">
      {stale && <ErrorBanner message={`The last refresh failed, so these numbers may be out of date. ${stale}`} />}
      <KpiGrid cols={4} label="Token package totals">
        <Kpi
          icon={Coins}
          label="Unused tokens"
          value={exactTokens(o.unused_tokens)}
          hint={`Still spendable on ${count(o.subscribers_active)} active ${o.subscribers_active === 1 ? "subscription" : "subscriptions"}`}
        />
        <Kpi
          icon={Users}
          label="Subscribers"
          value={count(o.subscribers_total)}
          hint={`${count(o.subscribers_active)} active · ${count(o.subscribers_total - o.subscribers_active)} expired${
            o.subscribers_disabled > 0 ? ` · ${count(o.subscribers_disabled)} key disabled` : ""
          }`}
        />
        <Kpi
          icon={BarChart3}
          label={`Tokens used, last ${o.range_days} days`}
          value={exactTokens(o.used_tokens_range)}
          hint={`${exactTokens(o.used_tokens_total)} since the first package`}
        />
        <UpstreamCostKpi cost={o.upstream_cost} rangeDays={o.range_days} />
      </KpiGrid>
      <UpstreamCostNote cost={o.upstream_cost} />
      <ChartCard
        title="Token usage per day"
        icon={BarChart3}
        subtitle={`Prompt plus completion tokens of every token package key, last ${o.range_days} days (UTC days).`}
      >
        <TimeBars
          data={o.daily}
          x={dayMs}
          xFormat={dayLabel}
          format={exactTokens}
          label={`Token package usage per day, last ${o.range_days} days`}
          height={220}
          series={[{ key: "tokens", label: "Tokens", color: "var(--color-accent-500)", value: (d) => d.tokens }]}
        />
      </ChartCard>
    </div>
  );
}

/** Dollars with enough decimals that a few cents of upstream cost still show. */
function costUSD(n: number): string {
  const digits = n !== 0 && Math.abs(n) < 10 ? 4 : 2;
  return `$${n.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: digits })}`;
}

/**
 * What the operator paid upstream for token package usage, before markup.
 * Provider names come from the usage rows, so spend that fell back to another
 * provider is named instead of being counted as a6api.
 */
function UpstreamCostKpi({ cost, rangeDays }: { cost: TokenUpstreamCost | undefined; rangeDays: number }) {
  if (!cost) return null;
  const names = cost.providers.map((p) => p.provider).join(", ");
  return (
    <Kpi
      icon={Wallet}
      label={names ? `Paid to ${names}` : "Paid upstream"}
      value={costUSD(cost.total_usd)}
      hint={`Before markup · ${costUSD(cost.range_usd)} last ${rangeDays} days`}
    />
  );
}

/** Says which requests the upstream total leaves out or still estimates, when there are any. */
function UpstreamCostNote({ cost }: { cost: TokenUpstreamCost | undefined }) {
  if (!cost || (cost.pending_requests === 0 && cost.unpriced_requests === 0)) return null;
  return (
    <p className="text-[12.5px] text-fg-muted">
      {cost.unpriced_requests > 0 &&
        `${count(cost.unpriced_requests)} ${cost.unpriced_requests === 1 ? "request has" : "requests have"} no upstream cost recorded, so the amount paid leaves them out. `}
      {cost.pending_requests > 0 &&
        `${count(cost.pending_requests)} ${cost.pending_requests === 1 ? "request still uses" : "requests still use"} the provisional ceiling price until the provider's usage log settles ${cost.pending_requests === 1 ? "it" : "them"}.`}
    </p>
  );
}

// ─── Subscribers ────────────────────────────────────────────────────────────

const SUBSCRIBERS_PER_PAGE = 20;

function subscriptionStatus(s: TokenSubscription): { label: string; tone: "success" | "danger" | "neutral" } {
  if (s.disabled) return { label: "Key disabled", tone: "danger" };
  if (!s.active) return { label: "Inactive", tone: "neutral" };
  return { label: "Active", tone: "success" };
}

function subscriberMatches(s: TokenSubscription, q: string): boolean {
  return [s.email, s.google_sub, s.key_display, s.key_id].some((v) => v?.toLowerCase().includes(q));
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
      toast.success(res.disabled ? "Key disabled" : "Key enabled again");
      qc.invalidateQueries({ queryKey: QK_SUBSCRIPTIONS });
    },
    onError: (e) => toast.error("Couldn't change key status", errorText(e)),
  });

  const [adjusting, setAdjusting] = useState<TokenSubscription | null>(null);
  const [viewing, setViewing] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const all = subs.data;
  const list = useMemo(() => {
    const q = search.trim().toLowerCase();
    return q ? (all ?? []).filter((s) => subscriberMatches(s, q)) : (all ?? []);
  }, [all, search]);
  const { paged, page, pages, setPage, total } = useClientPagination(list, SUBSCRIBERS_PER_PAGE);
  // Read from the live list so the detail view shows the balance after an adjustment.
  const viewed = viewing ? (all ?? []).find((s) => s.google_sub === viewing) ?? null : null;

  return (
    <Panel icon={Users} title="Token package subscribers" subtitle="Remaining tokens are the package limit minus usage since the key was issued. Select a subscriber to see their purchases and balance changes.">
      {subs.isLoading ? (
        <LoadingRegion label="Loading subscribers" className="space-y-2 p-4">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </LoadingRegion>
      ) : subs.isError && !subs.data ? (
        <div className="p-4">
          <ErrorBanner message={`Couldn't load subscribers. ${errorText(subs.error)}`} />
        </div>
      ) : (all ?? []).length === 0 ? (
        <EmptyState icon={Users} size="sm" title="No subscribers yet" hint="Token package key owners show up here after their first payment completes." />
      ) : (
        <>
          <div className="border-b border-line px-4 py-3" role="search" aria-label="Filter subscribers">
            <label className="relative block min-w-0 sm:w-80">
              <span className="sr-only">Search subscribers</span>
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-fg-faint" strokeWidth={1.75} aria-hidden="true" />
              <Input
                type="search"
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setPage(1);
                }}
                placeholder="Search email or key"
                className="pl-8"
              />
            </label>
          </div>
          {list.length === 0 ? (
            <EmptyState icon={Search} size="sm" title="No subscriber matches" hint={`Nothing matches "${search.trim()}". Search by email or key, or clear the search.`} />
          ) : (
            <>
              <div className="overflow-x-auto" role="region" aria-label="Token package subscribers table" tabIndex={0}>
                <table className="w-full min-w-[940px] text-[13px]">
                  <thead>
                    <tr className={THEAD}>
                      <th scope="col" className={TH}>Subscriber</th>
                      <th scope="col" className={TH}>Key</th>
                      <th scope="col" className={cn(TH, "text-right")}>Tokens left</th>
                      <th scope="col" className={TH}>Valid until</th>
                      <th scope="col" className={TH}>Status</th>
                      <th scope="col" className={cn(TH, "text-right")}>
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {paged.map((s) => {
                      const status = subscriptionStatus(s);
                      const remaining = Math.max(0, s.limit_tokens - s.used_tokens);
                      const busy = toggle.isPending && toggle.variables === s.google_sub;
                      const owner = s.email || s.google_sub;
                      return (
                        <tr key={s.google_sub} className="transition-colors hover:bg-hover">
                          <td className="px-4 py-2.5">
                            <button
                              type="button"
                              onClick={() => setViewing(s.google_sub)}
                              className="rounded text-left text-fg underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500"
                              aria-label={`Show purchase and balance history of ${owner}`}
                            >
                              {owner}
                            </button>
                          </td>
                          <td className="px-4 py-2.5 font-mono text-[12px] text-fg-muted">{s.key_display || s.key_id}</td>
                          <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">
                            <span className="font-semibold">{exactTokens(remaining)}</span>
                          </td>
                          <td className="whitespace-nowrap px-4 py-2.5 tabular-nums text-fg-muted">{dateID(s.expires_at, true)}</td>
                          <td className="px-4 py-2.5">
                            <Badge tone={status.tone}>{status.label}</Badge>
                          </td>
                          <td className="px-4 py-2.5">
                            <div className="flex justify-end gap-2">
                              <Button variant="secondary" onClick={() => setAdjusting(s)} aria-label={`Adjust tokens for ${owner}`}>
                                <SlidersHorizontal aria-hidden="true" />
                                Adjust tokens
                              </Button>
                              <Button
                                variant={s.disabled ? "ghost" : "danger"}
                                onClick={() => toggle.mutate(s.google_sub)}
                                disabled={toggle.isPending}
                                title={s.disabled ? "Enable key again" : "Disable key"}
                                aria-label={`${s.disabled ? "Enable" : "Disable"} key for ${owner}`}
                              >
                                {busy ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Power aria-hidden="true" />}
                                {s.disabled ? "Enable" : "Disable"}
                              </Button>
                            </div>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              <TablePagination page={page} pages={pages} total={total} onPage={setPage} label="Subscriber pages" />
            </>
          )}
        </>
      )}
      {viewed && (
        <SubscriberHistoryModal
          sub={viewed}
          onClose={() => setViewing(null)}
          onAdjust={() => {
            setViewing(null);
            setAdjusting(viewed);
          }}
        />
      )}
      {adjusting && <AdjustTokensModal sub={adjusting} onClose={() => setAdjusting(null)} />}
    </Panel>
  );
}

const tokenAdjustmentsKey = (sub: string) => ["token-adjustments", sub] as const;
const tokenSubscriberOrdersKey = (sub: string) => ["token-subscriber-orders", sub] as const;

/** What one order bought, in words: "100.000.000 tokens, 15 days" or "+30 days". */
function orderContents(o: TokenSubscriberOrder): string {
  if (o.token_kind === "tokens") return `${exactTokens(o.tokens)} tokens, ${o.days} days`;
  if (o.token_kind === "days") return `+${o.days} days, no tokens`;
  return "Package details unknown";
}

/**
 * A subscriber's audit trail: every token package order with what it bought,
 * and every manual balance change with its reason.
 */
function SubscriberHistoryModal({ sub, onClose, onAdjust }: { sub: TokenSubscription; onClose: () => void; onAdjust: () => void }) {
  const orders = useQuery({ queryKey: tokenSubscriberOrdersKey(sub.google_sub), queryFn: () => api.listTokenSubscriberOrders(sub.google_sub) });
  const adjustments = useQuery({ queryKey: tokenAdjustmentsKey(sub.google_sub), queryFn: () => api.listTokenAdjustments(sub.google_sub) });
  const owner = sub.email || sub.google_sub;
  const remaining = Math.max(0, sub.limit_tokens - sub.used_tokens);
  const paid = (orders.data?.orders ?? []).filter((o) => o.status === "completed" || o.status === "manual");
  const boughtTokens = paid.reduce((n, o) => n + (o.token_kind === "tokens" ? o.tokens : 0), 0);

  return (
    <Modal open onClose={onClose} title={owner} subtitle="Token package purchases and manual balance changes, newest first." maxWidth="max-w-2xl">
      <div className="space-y-5 px-5 py-4">
        <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border border-line bg-line text-[13px] sm:grid-cols-4">
          {[
            ["Tokens left", exactTokens(remaining)],
            ["Used", exactTokens(sub.used_tokens)],
            ["Limit", exactTokens(sub.limit_tokens)],
            ["Valid until", dateID(sub.expires_at, true)],
          ].map(([label, value]) => (
            <div key={label} className="bg-surface px-3 py-2.5">
              <dt className="text-[12px] text-fg-muted">{label}</dt>
              <dd className="mt-0.5 font-semibold tabular-nums text-fg">{value}</dd>
            </div>
          ))}
        </dl>

        <section aria-labelledby="sub-history-orders">
          <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
            <h3 id="sub-history-orders" className="flex items-center gap-1.5 text-[13px] font-medium text-fg">
              <Receipt className="h-3.5 w-3.5 text-fg-muted" aria-hidden="true" />
              Purchases
            </h3>
            {paid.length > 0 && (
              <p className="text-[12px] text-fg-muted">
                {count(paid.length)} paid · {exactTokens(boughtTokens)} tokens bought
              </p>
            )}
          </div>
          {orders.isLoading ? (
            <LoadingRegion label="Loading purchases">
              <Skeleton className="h-10 w-full" />
            </LoadingRegion>
          ) : orders.isError ? (
            <ErrorBanner message={`Couldn't load purchases. ${errorText(orders.error)}`} />
          ) : !orders.data?.orders.length ? (
            <p className="text-[12.5px] text-fg-muted">No token package orders from this user.</p>
          ) : (
            <ul className="max-h-64 divide-y divide-line overflow-y-auto rounded-xl border border-line">
              {orders.data.orders.map((o) => (
                <li key={o.order_id} className="flex flex-wrap items-start justify-between gap-2 px-3 py-2.5 text-[13px]">
                  <div className="min-w-0">
                    <p className="font-medium text-fg">{o.package_name || "Deleted package"}</p>
                    <p className="tabular-nums text-[12px] text-fg-muted">
                      {orderContents(o)} · {idr(o.amount_idr)}
                    </p>
                  </div>
                  <div className="text-right text-[12px] text-fg-muted">
                    <Badge tone={ORDER_STATUS_TONE[o.status] ?? "neutral"}>{ORDER_STATUS_LABEL[o.status] ?? o.status}</Badge>
                    <p className="mt-1 tabular-nums">{o.paid_at ? `Paid ${dateID(o.paid_at, true)}` : `Created ${dateID(o.created_at, true)}`}</p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section aria-labelledby="sub-history-adjustments">
          <h3 id="sub-history-adjustments" className="mb-2 flex items-center gap-1.5 text-[13px] font-medium text-fg">
            <History className="h-3.5 w-3.5 text-fg-muted" aria-hidden="true" />
            Balance changes by admin
          </h3>
          {adjustments.isLoading ? (
            <LoadingRegion label="Loading balance changes">
              <Skeleton className="h-10 w-full" />
            </LoadingRegion>
          ) : adjustments.isError ? (
            <ErrorBanner message={`Couldn't load balance changes. ${errorText(adjustments.error)}`} />
          ) : !adjustments.data?.adjustments.length ? (
            <p className="text-[12.5px] text-fg-muted">No manual changes. Each one shows here with its reason and the balance before and after.</p>
          ) : (
            <ul className="max-h-56 divide-y divide-line overflow-y-auto rounded-xl border border-line">
              {adjustments.data.adjustments.map((a) => (
                <li key={a.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2.5 text-[13px]">
                  <div className="min-w-0">
                    <p className={cn("font-medium tabular-nums", a.delta_tokens < 0 ? "text-bad" : "text-fg")}>
                      {a.delta_tokens < 0 ? "−" : "+"}
                      {exactTokens(Math.abs(a.delta_tokens))}
                    </p>
                    <p className="break-words text-[12px] text-fg-muted">{a.reason}</p>
                  </div>
                  <div className="text-right text-[12px] text-fg-muted">
                    <p className="tabular-nums">
                      {exactTokens(a.tokens_left_before)} → {exactTokens(a.tokens_left_after)} left
                    </p>
                    <p>{formatDateTime(a.created_at)}</p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
      <ModalFooter>
        <Button type="button" variant="ghost" onClick={onClose}>
          Close
        </Button>
        <Button type="button" variant="secondary" onClick={onAdjust}>
          <SlidersHorizontal aria-hidden="true" />
          Adjust tokens
        </Button>
      </ModalFooter>
    </Modal>
  );
}

const MAX_TOKENS_LEFT = 1_000_000_000_000;

type AdjustMode = "add" | "remove" | "set";

const ADJUST_MODES: { value: AdjustMode; label: string }[] = [
  { value: "add", label: "Add" },
  { value: "remove", label: "Remove" },
  { value: "set", label: "Set balance" },
];

/**
 * Adds tokens to a subscriber's balance, removes them, or sets the balance.
 * Add and Remove send a delta, so usage while the dialog is open does not
 * change what is added. Set sends the balance; the server adds the key's usage
 * at that moment to get the new limit.
 */
function AdjustTokensModal({ sub, onClose }: { sub: TokenSubscription; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const current = Math.max(0, sub.limit_tokens - sub.used_tokens);
  const [mode, setMode] = useState<AdjustMode>("add");
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [idem, setIdem] = useState(() => crypto.randomUUID());
  const [touched, setTouched] = useState(false);

  const amountStr = amount.trim();
  const whole = /^\d+$/.test(amountStr);
  const n = whole ? Number(amountStr) : NaN;
  const after = mode === "add" ? current + n : mode === "remove" ? current - n : n;

  let amountError: string | null = null;
  if (!whole || n > MAX_TOKENS_LEFT) amountError = mode === "set" ? "Enter a whole number of tokens, 0 or more." : "Enter a whole number of tokens.";
  else if (mode !== "set" && n === 0) amountError = "Enter more than 0 tokens.";
  else if (mode === "remove" && n > current) amountError = `Only ${exactTokens(current)} tokens are left, so at most that many can be removed.`;
  else if (after === 0 && sub.used_tokens === 0) amountError = "This key has used no tokens, so 0 left would make it unlimited. Disable the key instead.";
  else if (after > MAX_TOKENS_LEFT) amountError = "That balance is too large.";
  const unchanged = !amountError && mode === "set" && n === current;
  const reasonError = touched && !reason.trim() ? "Write why the balance changes. It is kept in the subscriber's history." : null;

  const save = useMutation({
    mutationFn: () =>
      api.adjustTokenKey(sub.google_sub, {
        ...(mode === "set" ? { tokens_left: n } : { delta_tokens: mode === "add" ? n : -n }),
        reason: reason.trim(),
        idempotency_key: idem,
      }),
    onSuccess: (res) => {
      toast.success("Tokens adjusted", `${sub.email || sub.google_sub} now has ${exactTokens(res.adjustment.tokens_left_after)} tokens left.`);
      qc.invalidateQueries({ queryKey: QK_SUBSCRIPTIONS });
      qc.invalidateQueries({ queryKey: QK_OVERVIEW });
      qc.invalidateQueries({ queryKey: tokenAdjustmentsKey(sub.google_sub) });
      onClose();
    },
  });

  // A new mode or amount is a new request, not a retry of the last one.
  const edit = (fn: () => void) => {
    fn();
    setIdem(crypto.randomUUID());
    save.reset();
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (amountError || !reason.trim() || unchanged || save.isPending) return;
    save.mutate();
  };

  const owner = sub.email || sub.google_sub;
  return (
    <Modal open onClose={save.isPending ? () => {} : onClose} title="Adjust tokens" subtitle={`Add or remove tokens for ${owner}. Use this to correct a balance, not to sell a package.`}>
      <form onSubmit={onSubmit} noValidate>
        <div className="space-y-4 px-5 py-4">
          <SegmentedControl value={mode} onChange={(m) => edit(() => setMode(m))} options={ADJUST_MODES} label="Adjustment type" />
          <Field
            label={mode === "add" ? "Tokens to add" : mode === "remove" ? "Tokens to remove" : "Tokens left"}
            error={touched || amountStr !== "" ? amountError : null}
            hint={
              mode === "set"
                ? `${exactTokens(sub.used_tokens)} tokens used so far. The new limit is used plus tokens left.`
                : `${exactTokens(current)} tokens left now.`
            }
            required
          >
            <Input
              type="number"
              inputMode="numeric"
              min="0"
              step="1"
              value={amount}
              onChange={(e) => edit(() => setAmount(e.target.value))}
              className="tabular-nums"
              autoFocus
            />
          </Field>
          {!amountError && (
            <div className="flex items-center justify-between rounded-xl border border-line bg-subtle px-4 py-3 text-[13px]">
              <span className="text-fg-muted">Tokens left</span>
              <span className="font-semibold tabular-nums text-fg">
                {exactTokens(current)} → {exactTokens(after)}
              </span>
            </div>
          )}
          {unchanged && touched && <p className="text-[12.5px] text-fg-muted">The balance is already {exactTokens(current)} tokens.</p>}
          {!sub.active && (
            <p className="text-[12.5px] text-fg-muted">
              This subscription has expired. The key cannot spend these tokens, and they are dropped when the next token package starts a new period.
            </p>
          )}
          <Field label="Reason" error={reasonError} required>
            <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="refund for failed requests" maxLength={500} autoComplete="off" />
          </Field>
          {save.isError && <ErrorBanner message={`Couldn't adjust tokens. ${errorText(save.error)}`} />}
        </div>
        <ModalFooter>
          <Button type="button" variant="ghost" onClick={onClose} disabled={save.isPending}>
            Cancel
          </Button>
          <Button type="submit" disabled={save.isPending || unchanged}>
            {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Check aria-hidden="true" />}
            {mode === "add" ? "Add tokens" : mode === "remove" ? "Remove tokens" : "Set balance"}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  );
}
