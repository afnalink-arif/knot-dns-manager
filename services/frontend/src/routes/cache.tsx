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
          <KPICard
            title="Cache Max Size"
            value={cacheSize()}
            subtitle="Configured maximum cache size"
            color="#3b82f6"
          />
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
