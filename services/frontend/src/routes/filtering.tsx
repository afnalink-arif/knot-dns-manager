import { createSignal, onMount, Show, For } from "solid-js";
import Layout from "~/components/Layout";
import KPICard from "~/components/KPICard";
import { authHeaders } from "~/lib/auth";
import { filterAPI, blockpageAPI, rpzAPI, type FilterRule, type BlockPageConfig, type RPZConfig, type RPZStats } from "~/lib/api";

type Tab = "rules" | "rpz" | "blockpage";

export default function FilteringPage() {
  const [activeTab, setActiveTab] = createSignal<Tab>("rules");
  const [rules, setRules] = createSignal<FilterRule[]>([]);
  const [stats, setStats] = createSignal<any>(null);
  const [loading, setLoading] = createSignal(true);
  const [applying, setApplying] = createSignal(false);
  const [msg, setMsg] = createSignal("");
  const [msgError, setMsgError] = createSignal(false);

  // Block page config
  const [bpConfig, setBpConfig] = createSignal<BlockPageConfig>({
    title: "Situs Ini Tidak Dapat Diakses",
    subtitle: "Berdasarkan kebijakan Kementerian Komunikasi dan Digital (Komdigi) Republik Indonesia, akses ke situs ini telah dibatasi demi perlindungan pengguna internet Indonesia.",
    message: "Pemblokiran ini merupakan bagian dari upaya menciptakan ruang digital yang aman, sehat, dan bertanggung jawab bagi seluruh masyarakat. Terima kasih atas pengertian dan kerja sama Anda.",
    contact: "",
    bg_color: "#0a0e1a",
    accent_color: "#dc2626",
    show_domain: true,
    show_logo: true,
    footer_text: "Internet Sehat dan Aman — Kementerian Komunikasi dan Digital RI",
  });
  const [bpSaving, setBpSaving] = createSignal(false);

  // RPZ
  const [rpzConfig, setRpzConfig] = createSignal<RPZConfig | null>(null);
  const [rpzMemoryMB, setRpzMemoryMB] = createSignal(0);
  const [rpzSyncing, setRpzSyncing] = createSignal(false);
  const [rpzOutput, setRpzOutput] = createSignal<string[]>([]);

  // Add domain
  const [newDomain, setNewDomain] = createSignal("");
  const [newCategory, setNewCategory] = createSignal("custom");

  // Import
  const [importMode, setImportMode] = createSignal(false);
  const [importUrl, setImportUrl] = createSignal("");
  const [importBulk, setImportBulk] = createSignal("");
  const [importCategory, setImportCategory] = createSignal("imported");
  const [importing, setImporting] = createSignal(false);

  // Search
  const [search, setSearch] = createSignal("");

  onMount(() => { loadData(); loadBlockPageConfig(); loadRPZ(); });

  const loadData = async () => {
    setLoading(true);
    try {
      const [filterData, statsData] = await Promise.all([
        filterAPI.list(),
        filterAPI.stats(),
      ]);
      setRules(filterData.rules || []);
      setStats(statsData);
    } catch {}
    setLoading(false);
  };

  const loadBlockPageConfig = async () => {
    try { setBpConfig(await blockpageAPI.getConfig()); } catch {}
  };

  const handleSaveBlockPage = async () => {
    setBpSaving(true);
    try {
      await blockpageAPI.updateConfig(bpConfig());
      showMsg("Block page saved");
    } catch (err: any) { showMsg(err.message, true); }
    setBpSaving(false);
  };

  const updateBp = (key: keyof BlockPageConfig, value: any) => {
    setBpConfig((prev) => ({ ...prev, [key]: value }));
  };

  const loadRPZ = async () => {
    try {
      const stats = await rpzAPI.getStats();
      setRpzConfig(stats.config);
      setRpzMemoryMB(stats.kresd_memory_mb || 0);
    } catch {}
  };

  const handleRPZToggle = async () => {
    const current = rpzConfig();
    if (!current) return;
    const newEnabled = !current.enabled;
    try {
      await rpzAPI.updateConfig({ enabled: newEnabled });
      setRpzConfig({ ...current, enabled: newEnabled });
      showMsg(newEnabled ? "RPZ Komdigi enabled" : "RPZ Komdigi disabled");
    } catch (err: any) { showMsg(err.message, true); }
  };

  const handleRPZSync = async () => {
    setRpzSyncing(true);
    setRpzOutput([]);
    try {
      const res = await fetch("/api/admin/rpz/sync", { method: "POST", headers: authHeaders() });
      if (!res.ok) { setRpzSyncing(false); return; }
      const reader = res.body!.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split("\n");
        buffer = lines.pop() || "";
        for (const line of lines) {
          if (line.startsWith("data: ")) setRpzOutput((prev) => [...prev, line.slice(6)]);
          else if (line.startsWith("event: done")) { setRpzSyncing(false); }
        }
      }
    } catch {}
    setRpzSyncing(false);
    loadRPZ();
  };

  const showMsg = (text: string, error = false) => {
    setMsg(text);
    setMsgError(error);
    setTimeout(() => setMsg(""), 4000);
  };

  const handleAdd = async (e: Event) => {
    e.preventDefault();
    if (!newDomain()) return;
    try {
      await filterAPI.add({ domain: newDomain(), category: newCategory() });
      setNewDomain("");
      showMsg("Domain added");
      loadData();
    } catch (err: any) { showMsg(err.message, true); }
  };

  const handleDelete = async (id: number) => {
    try {
      await filterAPI.delete(id);
      setRules((prev) => prev.filter((r) => r.id !== id));
    } catch {}
  };

  const handleToggle = async (id: number) => {
    try {
      await filterAPI.toggle(id);
      setRules((prev) => prev.map((r) => r.id === id ? { ...r, enabled: !r.enabled } : r));
    } catch {}
  };

  const handleImport = async (e: Event) => {
    e.preventDefault();
    setImporting(true);
    try {
      const result = await filterAPI.import({
        url: importUrl(),
        domains: importBulk(),
        category: importCategory(),
      });
      showMsg(`${result.imported} domains imported`);
      setImportUrl("");
      setImportBulk("");
      setImportMode(false);
      loadData();
    } catch (err: any) { showMsg(err.message, true); }
    setImporting(false);
  };

  const handleApply = async () => {
    setApplying(true);
    try {
      const result = await filterAPI.apply();
      showMsg(`Filter applied: ${result.domains_blocked} domains blocked`);
    } catch (err: any) { showMsg(err.message, true); }
    setApplying(false);
  };

  const filteredRules = () => {
    const q = search().toLowerCase();
    if (!q) return rules();
    return rules().filter((r) => r.domain.includes(q) || r.category.includes(q));
  };

  const categories = () => {
    const cats = new Set(rules().map((r) => r.category));
    return Array.from(cats).sort();
  };

  const categoryColors: Record<string, string> = {
    custom: "#3b82f6",
    ads: "#f59e0b",
    malware: "#ef4444",
    tracking: "#8b5cf6",
    adult: "#ec4899",
    gambling: "#f97316",
    social: "#06b6d4",
    imported: "#6b7280",
  };

  return (
    <Layout>
      <div class="space-y-5">
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-xl font-bold text-[var(--color-text)]">DNS Filtering</h1>
            <p class="text-xs text-slate-500 mt-0.5">Manage blocked domains and filtering rules</p>
          </div>
          <button
            onClick={handleApply}
            disabled={applying()}
            class="px-4 py-2 bg-emerald-600 hover:bg-emerald-700 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors flex items-center gap-2"
          >
            <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
            </svg>
            {applying() ? "Applying..." : "Apply Filter"}
          </button>
        </div>

        {/* Alert */}
        <Show when={msg()}>
          <div class={`p-2.5 rounded-lg text-xs ${msgError() ? "bg-red-500/10 text-red-400" : "bg-emerald-500/10 text-emerald-400"}`}>
            {msg()}
          </div>
        </Show>

        {/* Rules Tab Content */}
        <Show when={activeTab() === "rules"}>

        {/* KPI */}
        <div class="grid grid-cols-1 md:grid-cols-4 gap-4">
          <KPICard
            title="Total Rules"
            value={String(stats()?.total_rules || 0)}
            subtitle="All filter rules"
            color="#3b82f6"
          />
          <KPICard
            title="Active"
            value={String(stats()?.enabled_rules || 0)}
            subtitle="Blocked domains"
            color="#22c55e"
          />
          <KPICard
            title="Categories"
            value={String(stats()?.categories?.length || 0)}
            subtitle="Filter types"
            color="#a855f7"
          />
          <KPICard
            title="Status"
            value={applying() ? "Updating..." : "Ready"}
            subtitle="Filter engine"
            color="#eab308"
          />
        </div>

        {/* Add domain + Import */}
        <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
          <div class="flex items-center justify-between mb-3">
            <h3 class="text-sm font-medium text-[var(--color-text)]">Add Domain</h3>
            <button
              onClick={() => setImportMode(!importMode())}
              class="text-xs text-blue-400 hover:text-blue-300 transition-colors"
            >
              {importMode() ? "Close Import" : "Import List"}
            </button>
          </div>

          {/* Quick add */}
          <form onSubmit={handleAdd} class="flex gap-2 items-end">
            <div class="flex-1">
              <input
                type="text"
                placeholder="e.g. ads.example.com"
                class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] placeholder-slate-600 focus:outline-none focus:border-blue-500 transition"
                value={newDomain()}
                onInput={(e) => setNewDomain(e.target.value)}
              />
            </div>
            <select
              class="bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
              value={newCategory()}
              onChange={(e) => setNewCategory(e.target.value)}
            >
              <option value="custom">Custom</option>
              <option value="ads">Ads</option>
              <option value="malware">Malware</option>
              <option value="tracking">Tracking</option>
              <option value="adult">Adult</option>
              <option value="gambling">Gambling</option>
              <option value="social">Social</option>
            </select>
            <button type="submit" class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white text-sm rounded-lg transition-colors flex-shrink-0">
              + Block
            </button>
          </form>

          {/* Import panel */}
          <Show when={importMode()}>
            <form onSubmit={handleImport} class="mt-4 pt-4 border-t border-slate-700 space-y-3">
              <div class="grid grid-cols-2 gap-3">
                <div>
                  <label class="block text-xs text-slate-500 mb-1">URL Blocklist (format hosts)</label>
                  <input
                    type="url"
                    placeholder="https://raw.githubusercontent.com/..."
                    class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] placeholder-slate-600 focus:outline-none focus:border-blue-500 transition"
                    value={importUrl()}
                    onInput={(e) => setImportUrl(e.target.value)}
                  />
                </div>
                <div>
                  <label class="block text-xs text-slate-500 mb-1">Kategori</label>
                  <select
                    class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
                    value={importCategory()}
                    onChange={(e) => setImportCategory(e.target.value)}
                  >
                    <option value="ads">Ads</option>
                    <option value="malware">Malware</option>
                    <option value="tracking">Tracking</option>
                    <option value="adult">Adult</option>
                    <option value="gambling">Gambling</option>
                    <option value="imported">Other</option>
                  </select>
                </div>
              </div>
              <div>
                <label class="block text-xs text-slate-500 mb-1">Or paste domains (one per line)</label>
                <textarea
                  placeholder={"ads.example.com\ntracker.example.com\n..."}
                  rows={3}
                  class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] placeholder-slate-600 focus:outline-none focus:border-blue-500 transition font-mono"
                  value={importBulk()}
                  onInput={(e) => setImportBulk(e.target.value)}
                />
              </div>
              <button
                type="submit"
                disabled={importing() || (!importUrl() && !importBulk())}
                class="px-4 py-2 bg-purple-600 hover:bg-purple-700 disabled:opacity-50 text-white text-sm rounded-lg transition-colors"
              >
                {importing() ? "Importing..." : "Import"}
              </button>
            </form>
          </Show>
        </div>

        {/* Rules list */}
        <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
          <div class="p-4 border-b border-slate-700 flex items-center justify-between">
            <div class="flex items-center gap-3">
              <h3 class="text-sm font-medium text-[var(--color-text)]">Blocked Domains</h3>
              <span class="text-xs text-slate-500">{filteredRules().length} rules</span>
            </div>
            <input
              type="text"
              placeholder="Search domains..."
              class="bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-1.5 text-xs text-[var(--color-text)] placeholder-slate-600 focus:outline-none focus:border-blue-500 transition w-48"
              value={search()}
              onInput={(e) => setSearch(e.target.value)}
            />
          </div>

          <Show when={!loading()} fallback={<div class="p-8 text-center text-slate-500 text-sm">Loading...</div>}>
            <Show
              when={filteredRules().length > 0}
              fallback={<div class="p-8 text-center text-slate-500 text-sm">No blocked domains yet</div>}
            >
              <div class="max-h-[500px] overflow-y-auto">
                <For each={filteredRules()}>
                  {(rule) => (
                    <div class="flex items-center justify-between px-4 py-2.5 border-b border-slate-700/50 hover:bg-slate-700/20 group">
                      <div class="flex items-center gap-3 min-w-0">
                        <button
                          onClick={() => handleToggle(rule.id)}
                          class={`w-8 h-4 rounded-full transition-colors relative flex-shrink-0 ${
                            rule.enabled ? "bg-emerald-600" : "bg-slate-600"
                          }`}
                        >
                          <span class={`absolute top-0.5 w-3 h-3 bg-white rounded-full transition-transform ${
                            rule.enabled ? "left-4" : "left-0.5"
                          }`} />
                        </button>
                        <span class={`text-sm font-mono truncate ${rule.enabled ? "text-[var(--color-text)]" : "text-slate-500 line-through"}`}>
                          {rule.domain}
                        </span>
                        <span
                          class="text-[10px] px-1.5 py-0.5 rounded-full flex-shrink-0"
                          style={{ background: (categoryColors[rule.category] || "#6b7280") + "20", color: categoryColors[rule.category] || "#6b7280" }}
                        >
                          {rule.category}
                        </span>
                      </div>
                      <button
                        onClick={() => handleDelete(rule.id)}
                        class="text-red-400 opacity-0 group-hover:opacity-100 transition-opacity px-2 py-1 text-xs hover:bg-red-500/10 rounded"
                      >
                        Delete
                      </button>
                    </div>
                  )}
                </For>
              </div>
            </Show>
          </Show>
        </div>

        </Show>

        {/* Tabs */}
        <div class="flex gap-1 bg-slate-800/50 rounded-lg p-1">
          <button onClick={() => setActiveTab("rules")}
            class={`flex-1 px-3 py-2 rounded-md text-xs font-medium transition-all ${activeTab() === "rules" ? "bg-slate-700 text-[var(--color-text)] shadow-sm" : "text-slate-400 hover:text-slate-300"}`}>
            Filter Rules
          </button>
          <button onClick={() => setActiveTab("rpz")}
            class={`flex-1 px-3 py-2 rounded-md text-xs font-medium transition-all ${activeTab() === "rpz" ? "bg-slate-700 text-[var(--color-text)] shadow-sm" : "text-slate-400 hover:text-slate-300"}`}>
            RPZ Komdigi
          </button>
          <button onClick={() => setActiveTab("blockpage")}
            class={`flex-1 px-3 py-2 rounded-md text-xs font-medium transition-all ${activeTab() === "blockpage" ? "bg-slate-700 text-[var(--color-text)] shadow-sm" : "text-slate-400 hover:text-slate-300"}`}>
            Block Page
          </button>
        </div>

        {/* RPZ Tab */}
        <Show when={activeTab() === "rpz"}>
          <div class="space-y-4">
            {/* RPZ Enable/Disable Card */}
            <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
              <div class="flex items-center justify-between mb-4">
                <div>
                  <h3 class="text-sm font-medium text-[var(--color-text)]">RPZ Trust Positif Komdigi</h3>
                  <p class="text-[10px] text-slate-500 mt-0.5">
                    Response Policy Zone from Indonesia's Ministry of Communication and Digital
                  </p>
                </div>
                <button
                  onClick={handleRPZToggle}
                  class={`relative w-12 h-6 rounded-full transition-colors ${rpzConfig()?.enabled ? "bg-emerald-600" : "bg-slate-600"}`}
                >
                  <span class={`absolute top-1 w-4 h-4 bg-white rounded-full transition-transform ${rpzConfig()?.enabled ? "left-7" : "left-1"}`} />
                </button>
              </div>

              <Show when={rpzConfig()?.enabled}>
                <div class="p-2.5 bg-emerald-500/10 rounded-lg text-xs text-emerald-400 mb-4">
                  RPZ active — Trust Positif Komdigi blocklist is enforced
                </div>
              </Show>
              <Show when={!rpzConfig()?.enabled}>
                <div class="p-2.5 bg-slate-700/50 rounded-lg text-xs text-slate-400 mb-4">
                  RPZ disabled — Komdigi blocklist is not applied
                </div>
              </Show>

              {/* Sync button */}
              <div class="flex gap-2">
                <button
                  onClick={handleRPZSync}
                  disabled={rpzSyncing()}
                  class="px-4 py-2 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors"
                >
                  {rpzSyncing() ? "Syncing..." : "Sync Zone Transfer (AXFR)"}
                </button>
                <button onClick={loadRPZ}
                  class="px-3 py-2 bg-slate-700 hover:bg-slate-600 text-slate-300 text-xs rounded-lg transition-colors">
                  Refresh
                </button>
              </div>

              {/* Auto-sync */}
              <div class="mt-4 pt-4 border-t border-slate-700">
                <div class="flex items-center justify-between">
                  <div>
                    <h4 class="text-xs font-medium text-[var(--color-text)]">Auto Sync</h4>
                    <p class="text-[10px] text-slate-500 mt-0.5">Automatically sync zone on a schedule</p>
                  </div>
                  <button
                    onClick={async () => {
                      const current = rpzConfig();
                      if (!current) return;
                      const newVal = !current.auto_sync_enabled;
                      try {
                        await rpzAPI.updateConfig({ auto_sync_enabled: newVal } as any);
                        setRpzConfig({ ...current, auto_sync_enabled: newVal });
                        showMsg(newVal ? "Auto sync enabled" : "Auto sync disabled");
                      } catch (err: any) { showMsg(err.message, true); }
                    }}
                    class={`relative w-10 h-5 rounded-full transition-colors ${rpzConfig()?.auto_sync_enabled ? "bg-blue-600" : "bg-slate-600"}`}
                  >
                    <span class={`absolute top-0.5 w-4 h-4 bg-white rounded-full transition-transform ${rpzConfig()?.auto_sync_enabled ? "left-5" : "left-0.5"}`} />
                  </button>
                </div>
                <Show when={rpzConfig()?.auto_sync_enabled}>
                  <div class="mt-3 flex flex-wrap items-center gap-3">
                    <div class="flex items-center gap-1.5">
                      <label class="text-[10px] text-slate-400">Every:</label>
                      <select
                        value={rpzConfig()?.auto_sync_interval_hours || 24}
                        onChange={async (e) => {
                          const hours = parseInt(e.currentTarget.value);
                          const current = rpzConfig();
                          if (!current) return;
                          try {
                            await rpzAPI.updateConfig({ auto_sync_interval_hours: hours });
                            setRpzConfig({ ...current, auto_sync_interval_hours: hours });
                            showMsg(`Auto sync interval: ${hours}h`);
                          } catch (err: any) { showMsg(err.message, true); }
                        }}
                        class="bg-slate-700 text-[var(--color-text)] text-xs rounded-md px-2 py-1 border border-slate-600"
                      >
                        <option value={6}>6 hours</option>
                        <option value={12}>12 hours</option>
                        <option value={24}>24 hours</option>
                        <option value={48}>2 days</option>
                        <option value={72}>3 days</option>
                        <option value={168}>7 days</option>
                      </select>
                    </div>
                    <div class="flex items-center gap-1.5">
                      <label class="text-[10px] text-slate-400">Sync hour (WIB):</label>
                      <select
                        value={rpzConfig()?.auto_sync_hour ?? 2}
                        onChange={async (e) => {
                          const hour = parseInt(e.currentTarget.value);
                          const current = rpzConfig();
                          if (!current) return;
                          try {
                            await rpzAPI.updateConfig({ auto_sync_hour: hour });
                            setRpzConfig({ ...current, auto_sync_hour: hour });
                            showMsg(`Sync hour: ${hour.toString().padStart(2, "0")}:00 WIB`);
                          } catch (err: any) { showMsg(err.message, true); }
                        }}
                        class="bg-slate-700 text-[var(--color-text)] text-xs rounded-md px-2 py-1 border border-slate-600"
                      >
                        {Array.from({ length: 24 }, (_, i) => (
                          <option value={i}>{i.toString().padStart(2, "0")}:00</option>
                        ))}
                      </select>
                    </div>
                  </div>
                  <div class="mt-2 p-2 bg-slate-700/30 rounded-lg">
                    <p class="text-[10px] text-slate-400">
                      {(() => {
                        const cfg = rpzConfig()!;
                        const h = (cfg.auto_sync_hour ?? 2).toString().padStart(2, "0");
                        const interval = cfg.auto_sync_interval_hours || 24;
                        let desc = `Auto sync at ${h}:00 WIB`;
                        if (interval <= 24) desc += `, every ${interval} hours`;
                        else desc += `, every ${interval / 24} days`;
                        return desc;
                      })()}
                    </p>
                    <Show when={rpzConfig()?.last_sync}>
                      <p class="text-[10px] text-slate-500 mt-1">
                        Next sync: {(() => {
                          const cfg = rpzConfig()!;
                          const lastSync = new Date(cfg.last_sync!);
                          const intervalMs = cfg.auto_sync_interval_hours * 3600000;
                          const nextByInterval = new Date(lastSync.getTime() + intervalMs);
                          const now = new Date();

                          // Find next occurrence of preferred hour (WIB = UTC+7)
                          const preferredHour = cfg.auto_sync_hour ?? 2;
                          const nowWIB = new Date(now.getTime() + 7 * 3600000);
                          let nextRun = new Date(Date.UTC(nowWIB.getUTCFullYear(), nowWIB.getUTCMonth(), nowWIB.getUTCDate(), preferredHour - 7, 0, 0));
                          if (nextRun <= now) nextRun = new Date(nextRun.getTime() + 86400000);

                          // Sync runs when BOTH conditions met: interval elapsed AND preferred hour
                          const effectiveNext = nextRun > nextByInterval ? nextRun : nextByInterval;
                          return effectiveNext.toLocaleString("id-ID", { timeZone: "Asia/Jakarta" });
                        })()}
                      </p>
                    </Show>
                  </div>
                </Show>
              </div>
            </div>

            {/* Stats */}
            <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
              <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
                <p class="text-[10px] text-slate-500">Blocked Domains</p>
                <p class="text-xl font-bold text-[var(--color-text)] mt-1">{(rpzConfig()?.domain_count || 0).toLocaleString()}</p>
              </div>
              <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
                <p class="text-[10px] text-slate-500">Zone Size</p>
                <p class="text-xl font-bold text-[var(--color-text)] mt-1">
                  {rpzConfig()?.file_size_bytes ? (rpzConfig()!.file_size_bytes / 1024 / 1024).toFixed(1) + " MB" : "—"}
                </p>
              </div>
              <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
                <p class="text-[10px] text-slate-500">Kresd Memory</p>
                <p class={`text-xl font-bold mt-1 ${rpzMemoryMB() > 2048 ? "text-amber-400" : "text-[var(--color-text)]"}`}>
                  {rpzMemoryMB() > 0 ? (rpzMemoryMB() >= 1024 ? (rpzMemoryMB() / 1024).toFixed(1) + " GB" : rpzMemoryMB().toFixed(0) + " MB") : "—"}
                </p>
              </div>
              <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
                <p class="text-[10px] text-slate-500">Last Sync</p>
                <p class="text-sm font-medium text-[var(--color-text)] mt-1">
                  {rpzConfig()?.last_sync ? new Date(rpzConfig()!.last_sync!).toLocaleString() : "Never"}
                </p>
                <Show when={rpzConfig()?.sync_duration_ms}>
                  <p class="text-[10px] text-slate-500 mt-0.5">{(rpzConfig()!.sync_duration_ms / 1000).toFixed(1)}s</p>
                </Show>
              </div>
              <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
                <p class="text-[10px] text-slate-500">Status</p>
                <p class={`text-sm font-medium mt-1 ${
                  rpzConfig()?.last_sync_status === "success" ? "text-emerald-400" :
                  rpzConfig()?.last_sync_status === "error" ? "text-red-400" : "text-slate-400"
                }`}>
                  {rpzConfig()?.last_sync_status === "success" ? "Success" :
                   rpzConfig()?.last_sync_status === "error" ? "Failed" : "—"}
                </p>
                <Show when={rpzConfig()?.last_sync_error}>
                  <p class="text-[10px] text-red-400 mt-1 truncate" title={rpzConfig()!.last_sync_error}>{rpzConfig()!.last_sync_error}</p>
                </Show>
              </div>
              {/* Filtering is verified separately from syncing: a zone can land on
                  disk and still not be applied (ruledb loaded only halfway). */}
              <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
                <p class="text-[10px] text-slate-500">Filter Aktif</p>
                <p class={`text-sm font-medium mt-1 ${
                  rpzConfig()?.last_verify_status === "ok" ? "text-emerald-400" :
                  rpzConfig()?.last_verify_status === "failed" ? "text-red-400" :
                  rpzConfig()?.last_verify_status === "loading" ? "text-amber-400" : "text-slate-400"
                }`}>
                  {rpzConfig()?.last_verify_status === "ok" ? "Terverifikasi" :
                   rpzConfig()?.last_verify_status === "failed" ? "Tidak menapis" :
                   rpzConfig()?.last_verify_status === "loading" ? "Memuat ruledb" : "—"}
                </p>
                <Show when={rpzConfig()?.last_verify_detail}>
                  <p class="text-[10px] text-slate-500 mt-1 truncate" title={rpzConfig()!.last_verify_detail}>{rpzConfig()!.last_verify_detail}</p>
                </Show>
                <Show when={rpzConfig()?.last_verify_at}>
                  <p class="text-[10px] text-slate-500 mt-0.5">{new Date(rpzConfig()!.last_verify_at!).toLocaleString()}</p>
                </Show>
              </div>
            </div>

            {/* Performance info */}
            <div class="bg-emerald-500/5 rounded-xl p-4 border border-emerald-500/10">
              <div class="flex items-start gap-2">
                <svg class="w-4 h-4 text-emerald-400 mt-0.5 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                  <path stroke-linecap="round" stroke-linejoin="round" d="M13 10V3L4 14h7v7l9-11h-7z"/>
                </svg>
                <div class="text-xs text-slate-400 leading-relaxed">
                  <span class="text-emerald-400 font-medium">Native RPZ Engine</span> — Zone file loaded directly by kresd using
                  <code class="text-emerald-300 bg-emerald-500/10 px-1 rounded">policy.rpz()</code> with
                  internal trie data structure. ~10x more memory-efficient than local-data record conversion.
                </div>
              </div>
            </div>

            {/* Sync output */}
            <Show when={rpzOutput().length > 0}>
              <div class="bg-slate-950 rounded-lg p-3 font-mono text-[11px] leading-5 max-h-64 overflow-y-auto border border-slate-700/50"
                ref={(el) => {
                  const observer = new MutationObserver(() => { el.scrollTop = el.scrollHeight; });
                  observer.observe(el, { childList: true, subtree: true });
                }}>
                <For each={rpzOutput()}>
                  {(line) => (
                    <div class={
                      line.includes("[OK]") ? "text-emerald-400" :
                      line.includes("[ERROR]") ? "text-red-400" :
                      line.includes("[WARN]") ? "text-amber-400" :
                      line.includes("[INFO]") ? "text-blue-400" :
                      "text-slate-400"
                    }>{line}</div>
                  )}
                </For>
                <Show when={rpzSyncing()}>
                  <div class="text-blue-400 animate-pulse mt-1">Syncing zone transfer...</div>
                </Show>
              </div>
            </Show>

            {/* Info */}
            <div class="bg-slate-800/50 rounded-xl p-4 border border-slate-700/50 space-y-2">
              <p class="text-xs text-slate-500">
                RPZ Trust Positif uses <strong class="text-slate-400">DNS zone transfer (AXFR)</strong> from Komdigi servers.
                Your server IP must be registered first.
              </p>
              <p class="text-xs text-slate-500">
                Master: <code class="text-slate-400">{rpzConfig()?.master_servers || "139.255.196.202, 182.23.79.202, 103.154.123.130"}</code>
              </p>
              <p class="text-xs text-slate-500">
                Registrasi: <a href="https://s.komdigi.go.id/FormKoneksiRPZ" target="_blank" class="text-blue-400 hover:underline">s.komdigi.go.id/FormKoneksiRPZ</a>
              </p>
              <p class="text-xs text-amber-500/80">
                Zone file is ~1.3 GB. Sync takes several minutes depending on connection speed.
                Rekomendasi: RAM minimal 16 GB.
              </p>
            </div>
          </div>
        </Show>

        {/* Block Page Settings Tab */}
        <Show when={activeTab() === "blockpage"}>
          <div class="grid grid-cols-1 lg:grid-cols-2 gap-5">
            {/* Settings Form */}
            <div class="bg-slate-800 rounded-xl p-5 border border-slate-700 space-y-4">
              <h3 class="text-sm font-medium text-[var(--color-text)] mb-3">Customize Block Page</h3>

              <div>
                <label class="block text-xs text-slate-500 mb-1">Title</label>
                <input type="text" class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
                  value={bpConfig().title} onInput={(e) => updateBp("title", e.target.value)} />
              </div>

              <div>
                <label class="block text-xs text-slate-500 mb-1">Subtitle</label>
                <textarea rows={2} class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
                  value={bpConfig().subtitle} onInput={(e) => updateBp("subtitle", e.target.value)} />
              </div>

              <div>
                <label class="block text-xs text-slate-500 mb-1">Additional Message</label>
                <textarea rows={2} class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
                  value={bpConfig().message} onInput={(e) => updateBp("message", e.target.value)} />
              </div>

              <div>
                <label class="block text-xs text-slate-500 mb-1">Contact Info (optional)</label>
                <input type="text" placeholder="Email: admin@example.com" class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] placeholder-slate-600 focus:outline-none focus:border-blue-500 transition"
                  value={bpConfig().contact} onInput={(e) => updateBp("contact", e.target.value)} />
              </div>

              <div>
                <label class="block text-xs text-slate-500 mb-1">Footer</label>
                <input type="text" class="w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
                  value={bpConfig().footer_text} onInput={(e) => updateBp("footer_text", e.target.value)} />
              </div>

              <div class="grid grid-cols-2 gap-3">
                <div>
                  <label class="block text-xs text-slate-500 mb-1">Background</label>
                  <div class="flex items-center gap-2">
                    <input type="color" class="w-8 h-8 rounded border-0 cursor-pointer"
                      value={bpConfig().bg_color} onInput={(e) => updateBp("bg_color", e.target.value)} />
                    <input type="text" class="flex-1 bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-xs text-[var(--color-text)] font-mono focus:outline-none focus:border-blue-500 transition"
                      value={bpConfig().bg_color} onInput={(e) => updateBp("bg_color", e.target.value)} />
                  </div>
                </div>
                <div>
                  <label class="block text-xs text-slate-500 mb-1">Accent</label>
                  <div class="flex items-center gap-2">
                    <input type="color" class="w-8 h-8 rounded border-0 cursor-pointer"
                      value={bpConfig().accent_color} onInput={(e) => updateBp("accent_color", e.target.value)} />
                    <input type="text" class="flex-1 bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-xs text-[var(--color-text)] font-mono focus:outline-none focus:border-blue-500 transition"
                      value={bpConfig().accent_color} onInput={(e) => updateBp("accent_color", e.target.value)} />
                  </div>
                </div>
              </div>

              <div class="flex items-center gap-4">
                <label class="flex items-center gap-2 cursor-pointer">
                  <input type="checkbox" class="w-4 h-4 rounded bg-slate-900 border-slate-700 text-blue-600 focus:ring-blue-500"
                    checked={bpConfig().show_domain} onChange={(e) => updateBp("show_domain", e.target.checked)} />
                  <span class="text-xs text-slate-400">Show domain</span>
                </label>
                <label class="flex items-center gap-2 cursor-pointer">
                  <input type="checkbox" class="w-4 h-4 rounded bg-slate-900 border-slate-700 text-blue-600 focus:ring-blue-500"
                    checked={bpConfig().show_logo} onChange={(e) => updateBp("show_logo", e.target.checked)} />
                  <span class="text-xs text-slate-400">Show icon</span>
                </label>
              </div>

              <button onClick={handleSaveBlockPage} disabled={bpSaving()}
                class="w-full px-4 py-2.5 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors">
                {bpSaving() ? "Saving..." : "Save Block Page"}
              </button>
            </div>

            {/* Live Preview */}
            <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
              <div class="px-4 py-2.5 border-b border-slate-700 flex items-center justify-between">
                <span class="text-xs text-slate-400">Preview</span>
                <a href="/blockpage" target="_blank" class="text-xs text-blue-400 hover:text-blue-300 transition-colors">
                  Open in new tab
                </a>
              </div>
              <div class="aspect-[4/3] relative overflow-hidden" style={`background:${bpConfig().bg_color}`}>
                {/* Mini preview */}
                <div class="absolute inset-0 flex items-center justify-center p-6">
                  <div class="w-full max-w-[280px] text-center p-5 rounded-xl border border-white/10" style="background:rgba(255,255,255,0.04);backdrop-filter:blur(20px)">
                    <Show when={bpConfig().show_logo}>
                      <div class="w-10 h-10 mx-auto mb-3 rounded-full flex items-center justify-center" style={`background:${bpConfig().accent_color}`}>
                        <svg class="w-5 h-5 text-[var(--color-text)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636"/>
                        </svg>
                      </div>
                    </Show>
                    <h3 class="text-sm font-bold text-[var(--color-text)] mb-1">{bpConfig().title}</h3>
                    <Show when={bpConfig().show_domain}>
                      <div class="text-[10px] font-mono px-2 py-1 rounded my-2" style={`color:${bpConfig().accent_color};background:rgba(0,0,0,0.3)`}>
                        blocked-domain.com
                      </div>
                    </Show>
                    <p class="text-[10px] text-slate-400 leading-relaxed">{bpConfig().subtitle}</p>
                    <p class="text-[10px] text-slate-500 mt-1">{bpConfig().message}</p>
                    <Show when={bpConfig().contact}>
                      <div class="mt-2 text-[9px] text-blue-300 bg-blue-500/10 rounded px-2 py-1">{bpConfig().contact}</div>
                    </Show>
                    <div class="mt-3 text-[8px] text-slate-600">{bpConfig().footer_text}</div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </Show>

        {/* Info */}
        <Show when={activeTab() === "rules"}>
          <div class="bg-slate-800/50 rounded-xl p-4 border border-slate-700/50">
            <p class="text-xs text-slate-500">
              After adding/removing domains, click <strong class="text-emerald-400">Apply Filter</strong> to
              update the DNS resolver configuration. Blocked domains will be redirected to the block page.
            </p>
          </div>
        </Show>
      </div>
    </Layout>
  );
}
