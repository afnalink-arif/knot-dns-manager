import { createResource, createSignal, onCleanup, Show, For } from "solid-js";
import Layout from "~/components/Layout";
import KPICard from "~/components/KPICard";
import TimeSeriesChart from "~/components/charts/TimeSeriesChart";
import { metricsAPI, healthAPI, resolverAPI, dockerCleanupAPI } from "~/lib/api";
import { extractValue, extractTimeSeries, fmt } from "~/lib/prometheus";

export default function SystemPage() {
  const [refreshKey, setRefreshKey] = createSignal(0);

  const [system, { refetch }] = createResource(refreshKey, () => metricsAPI.system());
  const [health] = createResource(refreshKey, () => healthAPI.check());
  const [resolver] = createResource(() => resolverAPI.info());

  // CPU timeline (range query)
  const [cpuTimeline] = createResource(refreshKey, () =>
    metricsAPI.qps({ step: "15s" }).then(() =>
      fetch("/api/metrics/system/cpu-timeline").then(r => r.json()).catch(() => null)
    ).catch(() => null)
  );

  // Auto-refresh every 10s
  const interval = setInterval(() => setRefreshKey((k) => k + 1), 10000);
  onCleanup(() => clearInterval(interval));

  // Extractors
  const cpu = () => extractValue(system()?.cpu_usage);
  const memUsed = () => extractValue(system()?.memory_used);
  const memTotal = () => extractValue(system()?.memory_total);
  const memPct = () => {
    const used = memUsed();
    const total = memTotal();
    return used && total ? (used / total) * 100 : null;
  };
  const diskPct = () => extractValue(system()?.disk_usage_pct);
  const load = () => extractValue(system()?.load_average);
  const netRx = () => extractValue(system()?.network_rx);
  const netTx = () => extractValue(system()?.network_tx);
  const diskRead = () => extractValue(system()?.disk_read);
  const diskWrite = () => extractValue(system()?.disk_write);

  const fmtBytes = (bytes: number | null) => {
    if (bytes === null) return "--";
    if (bytes > 1e9) return (bytes / 1e9).toFixed(1) + " GB";
    if (bytes > 1e6) return (bytes / 1e6).toFixed(1) + " MB";
    if (bytes > 1e3) return (bytes / 1e3).toFixed(1) + " KB";
    return bytes.toFixed(0) + " B";
  };

  const fmtBytesPerSec = (bps: number | null) => {
    if (bps === null) return "--";
    if (bps > 1e9) return (bps / 1e9).toFixed(1) + " GB/s";
    if (bps > 1e6) return (bps / 1e6).toFixed(1) + " MB/s";
    if (bps > 1e3) return (bps / 1e3).toFixed(1) + " KB/s";
    return bps.toFixed(0) + " B/s";
  };

  // Progress bar color based on percentage
  const barColor = (pct: number) => {
    if (pct > 90) return "bg-red-500";
    if (pct > 70) return "bg-yellow-500";
    return "bg-emerald-500";
  };

  return (
    <Layout>
      <div class="space-y-6">
        {/* Header */}
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-bold text-[var(--color-text)]">System Monitor</h1>
            <p class="text-sm text-slate-400 mt-1">Server resources and service health</p>
          </div>
          <button
            onClick={() => setRefreshKey((k) => k + 1)}
            class="px-3 py-1.5 bg-slate-700 hover:bg-slate-600 text-slate-300 text-sm rounded-lg transition-colors"
          >
            Refresh
          </button>
        </div>

        {/* KPI Cards */}
        <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
          <KPICard
            title="CPU Usage"
            value={cpu() !== null ? fmt(cpu()!, 1) + "%" : "--"}
            subtitle="Current utilization"
            color={cpu() && cpu()! > 80 ? "#ef4444" : "#3b82f6"}
          />
          <KPICard
            title="Memory"
            value={memPct() !== null ? fmt(memPct()!, 1) + "%" : "--"}
            subtitle={memUsed() && memTotal() ? `${fmtBytes(memUsed())} / ${fmtBytes(memTotal())}` : ""}
            color={memPct() && memPct()! > 80 ? "#ef4444" : "#22c55e"}
          />
          <KPICard
            title="Disk Usage"
            value={diskPct() !== null ? fmt(diskPct()!, 1) + "%" : "--"}
            subtitle="Root filesystem"
            color={diskPct() && diskPct()! > 80 ? "#ef4444" : "#eab308"}
          />
          <KPICard
            title="Load Average"
            value={load() !== null ? fmt(load()!, 2) : "--"}
            subtitle={`${resolver()?.server?.cpus || "?"} cores available`}
            color="#a855f7"
          />
        </div>

        {/* Resource Gauges */}
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {/* CPU + Memory */}
          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <h3 class="text-sm font-medium text-slate-400 mb-5">Resource Usage</h3>
            <div class="space-y-5">
              {/* CPU bar */}
              <div>
                <div class="flex justify-between text-sm mb-1.5">
                  <span class="text-slate-300">CPU</span>
                  <span class="text-[var(--color-text)] font-medium">{cpu() !== null ? fmt(cpu()!, 1) + "%" : "--"}</span>
                </div>
                <div class="h-3 rounded-full bg-slate-700 overflow-hidden">
                  <div
                    class={`h-full rounded-full transition-all duration-500 ${cpu() ? barColor(cpu()!) : "bg-slate-600"}`}
                    style={{ width: `${cpu() || 0}%` }}
                  />
                </div>
              </div>

              {/* Memory bar */}
              <div>
                <div class="flex justify-between text-sm mb-1.5">
                  <span class="text-slate-300">Memory</span>
                  <span class="text-[var(--color-text)] font-medium">
                    {memUsed() ? fmtBytes(memUsed()) : "--"} / {memTotal() ? fmtBytes(memTotal()) : "--"}
                  </span>
                </div>
                <div class="h-3 rounded-full bg-slate-700 overflow-hidden">
                  <div
                    class={`h-full rounded-full transition-all duration-500 ${memPct() ? barColor(memPct()!) : "bg-slate-600"}`}
                    style={{ width: `${memPct() || 0}%` }}
                  />
                </div>
              </div>

              {/* Disk bar */}
              <div>
                <div class="flex justify-between text-sm mb-1.5">
                  <span class="text-slate-300">Disk (/)</span>
                  <span class="text-[var(--color-text)] font-medium">{diskPct() !== null ? fmt(diskPct()!, 1) + "%" : "--"}</span>
                </div>
                <div class="h-3 rounded-full bg-slate-700 overflow-hidden">
                  <div
                    class={`h-full rounded-full transition-all duration-500 ${diskPct() ? barColor(diskPct()!) : "bg-slate-600"}`}
                    style={{ width: `${diskPct() || 0}%` }}
                  />
                </div>
              </div>
            </div>
          </div>

          {/* Network + Disk I/O */}
          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <h3 class="text-sm font-medium text-slate-400 mb-5">I/O Throughput</h3>
            <div class="grid grid-cols-2 gap-6">
              <div>
                <p class="text-xs text-slate-500 mb-3 uppercase tracking-wider">Network</p>
                <div class="space-y-3">
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                      <span class="w-2 h-2 rounded-full bg-emerald-500" />
                      <span class="text-sm text-slate-400">RX</span>
                    </div>
                    <span class="text-sm text-[var(--color-text)] font-mono">{fmtBytesPerSec(netRx())}</span>
                  </div>
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                      <span class="w-2 h-2 rounded-full bg-blue-500" />
                      <span class="text-sm text-slate-400">TX</span>
                    </div>
                    <span class="text-sm text-[var(--color-text)] font-mono">{fmtBytesPerSec(netTx())}</span>
                  </div>
                </div>
              </div>
              <div>
                <p class="text-xs text-slate-500 mb-3 uppercase tracking-wider">Disk</p>
                <div class="space-y-3">
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                      <span class="w-2 h-2 rounded-full bg-yellow-500" />
                      <span class="text-sm text-slate-400">Read</span>
                    </div>
                    <span class="text-sm text-[var(--color-text)] font-mono">{fmtBytesPerSec(diskRead())}</span>
                  </div>
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                      <span class="w-2 h-2 rounded-full bg-purple-500" />
                      <span class="text-sm text-slate-400">Write</span>
                    </div>
                    <span class="text-sm text-[var(--color-text)] font-mono">{fmtBytesPerSec(diskWrite())}</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        {/* Service Health */}
        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-4">Service Health</h3>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
            <Show when={health()}>
              {(h) => (
                <For each={Object.entries(h().checks)}>
                  {([name, status]) => (
                    <div class={`flex items-center gap-3 p-3 rounded-lg border ${
                      status === "ok"
                        ? "border-emerald-500/30 bg-emerald-500/5"
                        : "border-red-500/30 bg-red-500/5"
                    }`}>
                      <span class={`w-2.5 h-2.5 rounded-full flex-shrink-0 ${
                        status === "ok" ? "bg-emerald-500" : "bg-red-500"
                      }`} />
                      <div>
                        <p class="text-sm text-[var(--color-text)] font-medium capitalize">{name}</p>
                        <p class={`text-xs ${status === "ok" ? "text-emerald-400" : "text-red-400"}`}>
                          {status === "ok" ? "Healthy" : status}
                        </p>
                      </div>
                    </div>
                  )}
                </For>
              )}
            </Show>
          </div>
        </div>

        {/* Docker Cleanup */}
        <DockerCleanupCard />

        {/* Server Info */}
        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-4">Server Information</h3>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-y-4 gap-x-6 text-sm">
            <div>
              <p class="text-slate-500">CPU Cores</p>
              <p class="text-[var(--color-text)]">{resolver()?.server?.cpus || "--"}</p>
            </div>
            <div>
              <p class="text-slate-500">Total RAM</p>
              <p class="text-[var(--color-text)]">{memTotal() ? fmtBytes(memTotal()) : "--"}</p>
            </div>
            <div>
              <p class="text-slate-500">DNS Resolver</p>
              <p class="text-[var(--color-text)]">Knot Resolver 6.2</p>
            </div>
            <div>
              <p class="text-slate-500">kresd Workers</p>
              <p class="text-[var(--color-text)]">{resolver()?.workers ?? "--"}</p>
            </div>
            <div>
              <p class="text-slate-500">DNS Cache</p>
              <p class="text-[var(--color-text)]">{resolver()?.cache?.["size-max"] || "--"}</p>
            </div>
            <div>
              <p class="text-slate-500">Serve Stale</p>
              <p class={`${resolver()?.options?.["serve-stale"] ? "text-emerald-400" : "text-[var(--color-text)]"}`}>
                {resolver()?.options?.["serve-stale"] === true ? "Enabled" : resolver()?.options?.["serve-stale"] === false ? "Disabled" : "--"}
              </p>
            </div>
            <div>
              <p class="text-slate-500">Monitoring</p>
              <p class="text-[var(--color-text)]">{resolver()?.monitoring?.metrics || "--"}</p>
            </div>
            <div>
              <p class="text-slate-500">Hostname</p>
              <p class="text-[var(--color-text)] font-mono text-xs">{resolver()?.server?.hostname || "--"}</p>
            </div>
          </div>
        </div>
      </div>
    </Layout>
  );
}

function DockerCleanupCard() {
  const [info, setInfo] = createSignal<any>(null);
  const [loading, setLoading] = createSignal(true);
  const [cleaning, setCleaning] = createSignal(false);
  const [result, setResult] = createSignal<string[] | null>(null);

  const load = async () => {
    setLoading(true);
    try { setInfo(await dockerCleanupAPI.getInfo()); } catch {}
    setLoading(false);
  };

  // Load on mount
  load();

  const handleCleanup = async () => {
    setCleaning(true);
    setResult(null);
    try {
      const res = await dockerCleanupAPI.run();
      const lines: string[] = [];
      if (res.before_reclaimable) lines.push(`Before: ${res.before_reclaimable}`);
      if (res.details?.length) lines.push(...res.details);
      if (res.after_reclaimable) lines.push(`After: ${res.after_reclaimable}`);
      setResult(lines.length > 0 ? lines : ["Cleanup complete"]);
      load();
    } catch { setResult(["Failed to run cleanup"]); }
    setCleaning(false);
  };

  return (
    <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
      <div class="flex items-center justify-between mb-4">
        <div>
          <h3 class="text-sm font-medium text-slate-400">Docker Cleanup</h3>
          <p class="text-[10px] text-slate-500 mt-0.5">Remove unused images and build cache</p>
        </div>
        <div class="flex gap-2">
          <button onClick={load}
            class="px-2.5 py-1.5 bg-slate-700 hover:bg-slate-600 text-slate-300 text-xs rounded-lg transition-colors">
            Refresh
          </button>
          <button onClick={handleCleanup} disabled={cleaning()}
            class="px-3 py-1.5 bg-red-600 hover:bg-red-700 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors">
            {cleaning() ? "Cleaning..." : "Clean Up"}
          </button>
        </div>
      </div>

      <Show when={!loading() && info()} fallback={<p class="text-xs text-slate-500">Loading...</p>}>
        <div class="flex items-center gap-3">
          <span class="text-[10px] text-slate-500">Reclaimable:</span>
          <span class={`text-sm font-bold ${info().total_reclaimable !== "0 B" ? "text-amber-400" : "text-emerald-400"}`}>
            {info().total_reclaimable}
          </span>
        </div>
      </Show>

      <Show when={result()}>
        <div class="mt-3 p-2.5 bg-slate-900 rounded-lg border border-slate-700/50">
          <For each={result()!}>
            {(line) => (
              <p class={`text-xs font-mono ${
                line.startsWith("Before:") ? "text-slate-400" :
                line.startsWith("After:") ? "text-emerald-400 font-medium" :
                line.includes("Failed") ? "text-red-400" :
                "text-slate-300"
              }`}>{line}</p>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}
