import { createResource, Show } from "solid-js";
import Layout from "~/components/Layout";
import KPICard from "~/components/KPICard";
import TimeSeriesChart from "~/components/charts/TimeSeriesChart";
import { metricsAPI, resolverAPI } from "~/lib/api";
import { extractValue, extractTimeSeries, fmt } from "~/lib/prometheus";

export default function CachePage() {
  const [cacheData] = createResource(() => metricsAPI.cache());
  const [resolver] = createResource(() => resolverAPI.info());

  const cacheSize = () => resolver()?.cache?.["size-max"] || "...";
  const usage = () => resolver()?.cache_usage;

  const fmtBytes = (b: number) => {
    if (!b || b < 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    let v = b;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
  };
  const serveStale = () => resolver()?.options?.["serve-stale"] ?? null;
  const monitoringMode = () => resolver()?.monitoring?.metrics || "...";

  const hitRatio = () => {
    const d = cacheData();
    if (!d?.hit_ratio) return null;
    // hit_ratio comes from a range query, so extract the latest value from the time series
    const ts = extractTimeSeries(d.hit_ratio);
    if (ts.values.length === 0) return null;
    return ts.values[ts.values.length - 1];
  };

  const hitRatioChart = () => {
    const d = cacheData();
    if (!d?.hit_ratio) return null;
    const ts = extractTimeSeries(d.hit_ratio);
    if (ts.timestamps.length === 0) return null;
    return [ts.timestamps, ts.values.map((v) => v * 100)] as [number[], number[]];
  };

  return (
    <Layout>
      <div class="space-y-6">
        <div>
          <h1 class="text-2xl font-bold text-[var(--color-text)]">Cache Performance</h1>
          <p class="text-sm text-slate-400 mt-1">DNS cache metrics and efficiency</p>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
          <KPICard
            title="Cache Hit Ratio"
            value={hitRatio() !== null ? fmt(hitRatio()! * 100, 1) + "%" : "--"}
            subtitle="Percentage of queries served from cache"
            color="#22c55e"
          />
          <div class="glass glass-sheen rounded-xl p-5">
            <p class="text-xs text-[var(--color-text-muted)] mb-1">Cache Usage</p>
            <Show
              when={usage()?.available}
              fallback={
                <>
                  <p class="text-2xl font-bold text-[var(--color-text)]">{cacheSize()}</p>
                  <p class="text-[11px] text-[var(--color-text-faint)] mt-1">
                    {usage()?.error ? "Usage unavailable" : "Configured maximum"}
                  </p>
                </>
              }
            >
              <p class="text-2xl font-bold text-[var(--color-text)]">
                {fmtBytes(usage()!.live_bytes)}
                <span class="text-sm font-normal text-[var(--color-text-faint)]"> / {fmtBytes(usage()!.map_size_bytes)}</span>
              </p>
              <div class="mt-2 h-1.5 rounded-full bg-[var(--color-border)] overflow-hidden">
                <div
                  class={`h-full rounded-full transition-all duration-500 ${
                    usage()!.percent_used >= 90 ? "bg-red-500"
                      : usage()!.percent_used >= 70 ? "bg-amber-500"
                      : "bg-blue-500"
                  }`}
                  style={{ width: `${Math.min(100, Math.max(1, usage()!.percent_used))}%` }}
                />
              </div>
              <p class="text-[11px] text-[var(--color-text-faint)] mt-1.5">
                {usage()!.percent_used.toFixed(1)}%
                {usage()!.high_water_only ? " peak used" : " used"}
                <Show when={usage()!.entries > 0}>
                  {" · "}{usage()!.entries.toLocaleString()} records
                </Show>
              </p>
              <Show when={usage()!.high_water_only}>
                <p class="text-[10px] text-[var(--color-text-faint)]/70 mt-0.5">
                  High-water mark — kresd holds the cache open, so live occupancy can't be read
                </p>
              </Show>
            </Show>
          </div>
          <KPICard
            title="Serve Stale"
            value={serveStale() === true ? "Enabled" : serveStale() === false ? "Disabled" : "--"}
            subtitle="Serve expired cache on upstream failure"
            color="#eab308"
          />
        </div>

        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-4">Cache Hit Ratio Over Time (%)</h3>
          <Show
            when={hitRatioChart()}
            fallback={
              <div class="h-[300px] flex items-center justify-center text-slate-500">
                Collecting data — hit ratio will appear after sustained queries...
              </div>
            }
          >
            {(data) => (
              <TimeSeriesChart
                data={data()}
                series={[{ label: "Hit %", stroke: "#22c55e", fill: "rgba(34,197,94,0.1)", width: 2 }]}
                height={300}
                yLabel="%"
              />
            )}
          </Show>
        </div>

        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-4">Cache Configuration (live from kresd)</h3>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
            <div>
              <p class="text-xs text-slate-500">Max Size</p>
              <p class="text-lg font-medium text-[var(--color-text)]">{cacheSize()}</p>
            </div>
            <div>
              <p class="text-xs text-slate-500">Serve Stale</p>
              <p class={`text-lg font-medium ${serveStale() ? "text-emerald-400" : "text-slate-400"}`}>
                {serveStale() === true ? "Enabled" : serveStale() === false ? "Disabled" : "--"}
              </p>
            </div>
            <div>
              <p class="text-xs text-slate-500">DNSSEC</p>
              <p class="text-lg font-medium text-emerald-400">Validating</p>
            </div>
            <div>
              <p class="text-xs text-slate-500">Storage</p>
              <p class="text-lg font-medium text-[var(--color-text)]">{resolver()?.cache?.storage || "--"}</p>
            </div>
          </div>
        </div>
      </div>
    </Layout>
  );
}
