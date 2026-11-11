import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Loader2, Package, Pencil, Plus, Power, Settings2, ShoppingCart, Trash2, Users } from "lucide-react";
import { cn } from "@/lib/utils";
import { api, type TokenPackage, type TokenPackageInput, type TokenPackageKind, type TokenSettings, type TokenSubscription } from "../lib/api";
import { formatTokens } from "../lib/format";
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
  Toggle,
} from "../components/ui";
import { PortalOrders } from "./payments/PortalOrders";
import { Panel, REFETCH_MS, TH, THEAD, dateID, errorText, idr } from "./payments/shared";

const TABS = ["packages", "subscribers", "orders"] as const;
type Tab = (typeof TABS)[number];
const TABS_ID = "token-package-tabs";

const QK_PACKAGES = ["token-packages"] as const;
const QK_SETTINGS = ["token-settings"] as const;
const QK_SUBSCRIPTIONS = ["token-subscriptions"] as const;

const DESCRIPTION: Record<Tab, string> = {
  packages: "GPT-6 token packages sold in the portal: price, token amount, validity and rate limits.",
  subscribers: "Owners of token package keys: remaining tokens, expiry, and a switch to disable a key.",
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
  const [tab, setTab] = useState<Tab>("packages");
  return (
    <div className="space-y-6">
      <PageHeader title="Token Packages" description={DESCRIPTION[tab]} />
      <TabBar
        idPrefix={TABS_ID}
        label="Token package sections"
        active={tab}
        onChange={setTab}
        tabs={[
          { value: "packages", label: "Packages", icon: Package },
          { value: "subscribers", label: "Subscribers", icon: Users },
          { value: "orders", label: "Orders", icon: ShoppingCart },
        ]}
      />
      <TabPanel idPrefix={TABS_ID} value={tab} className="space-y-6">
        {tab === "packages" ? (
          <>
            <SettingsPanel />
            <PackagesPanel />
          </>
        ) : tab === "subscribers" ? (
          <SubscribersPanel />
        ) : (
          <PortalOrders product="token" />
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
                        {p.featured && <Badge tone="accent">Featured</Badge>}
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
                Mark as featured
              </p>
              <p className="text-[12px] text-fg-muted">The portal shows a Featured label on this package.</p>
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

// ─── Subscribers ────────────────────────────────────────────────────────────

function subscriptionStatus(s: TokenSubscription): { label: string; tone: "success" | "danger" | "neutral" } {
  if (s.disabled) return { label: "Key disabled", tone: "danger" };
  if (!s.active) return { label: "Inactive", tone: "neutral" };
  return { label: "Active", tone: "success" };
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

  const list = subs.data ?? [];

  return (
    <Panel icon={Users} title="Token package subscribers" subtitle="Remaining tokens are the package limit minus usage since the key was issued.">
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
      ) : list.length === 0 ? (
        <EmptyState icon={Users} size="sm" title="No subscribers yet" hint="Token package key owners show up here after their first payment completes." />
      ) : (
        <div className="overflow-x-auto" role="region" aria-label="Token package subscribers table" tabIndex={0}>
          <table className="w-full min-w-[800px] text-[13px]">
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
              {list.map((s) => {
                const status = subscriptionStatus(s);
                const remaining = Math.max(0, s.limit_tokens - s.used_tokens);
                const busy = toggle.isPending && toggle.variables === s.google_sub;
                return (
                  <tr key={s.google_sub} className="transition-colors hover:bg-hover">
                    <td className="px-4 py-2.5 text-fg">{s.email || s.google_sub}</td>
                    <td className="px-4 py-2.5 font-mono text-[12px] text-fg-muted">{s.key_display || s.key_id}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right tabular-nums">
                      <span className="font-semibold">{formatTokens(remaining)}</span>
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
                          title={s.disabled ? "Enable key again" : "Disable key"}
                          aria-label={`${s.disabled ? "Enable" : "Disable"} key for ${s.email || s.google_sub}`}
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
      )}
    </Panel>
  );
}
