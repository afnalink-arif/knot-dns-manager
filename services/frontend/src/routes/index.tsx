import { createResource, createSignal, onCleanup, Show } from "solid-js";
import Layout from "~/components/Layout";
import KPICard from "~/components/KPICard";
import TimeSeriesChart from "~/components/charts/TimeSeriesChart";
import DonutChart from "~/components/charts/DonutChart";
import { metricsAPI, queriesAPI, resolverAPI } from "~/lib/api";
import { extractValue, extractTimeSeries, fmt } from "~/lib/prometheus";
import { createRealtimeConnection } from "~/lib/ws";

export default function Overview() {
  const { data: realtime, connected } = createRealtimeConnection();
  const [refreshKey, setRefreshKey] = createSignal(0);

  const [overview, { refetch: refetchOverview }] = createResource(refreshKey, () => metricsAPI.overview());
  const [resolver] = createResource(() => resolverAPI.info());
  const [qpsTimeline, { refetch: refetchQps }] = createResource(refreshKey, () => metricsAPI.qps({ step: "15s" }));
  const [protocolDist] = createResource(refreshKey, () => queriesAPI.protocolDistribution({ hours: "1" }));
  const [topDomains] = createResource(refreshKey, () => queriesAPI.topDomains({ hours: "1", limit: "10" }));

  // Auto-refresh every 15s
  const interval = setInterval(() => {
    setRefreshKey((k) => k + 1);
  }, 15000);
  onCleanup(() => clearInterval(interval));

  // Extract KPI values from overview or realtime WebSocket data
  const qps = () => {
    const rt = realtime();
    if (rt?.qps) {
      const v = extractValue(rt.qps);
      if (v !== null) return v;
    }
    const ov = overview();
    return ov ? extractValue(ov.qps) : null;
  };

  const latency = () => {
    const rt = realtime();
    if (rt?.avg_latency_ms) {
      const v = extractValue(rt.avg_latency_ms);
      if (v !== null) return v;
    }
    const ov = overview();
    return ov ? extractValue(ov.avg_latency_ms) : null;
  };

  const cacheRatio = () => {
    const rt = realtime();
    if (rt?.cache_hit_ratio) {
      const v = extractValue(rt.cache_hit_ratio);
      if (v !== null) return v;
    }
    const ov = overview();
    return ov ? extractValue(ov.cache_hit_ratio) : null;
  };

  const cpuUsage = () => {
    const rt = realtime();
    if (rt?.cpu_usage) {
      const v = extractValue(rt.cpu_usage);
      if (v !== null) return v;
    }
    return null;
  };

  const memUsage = () => {
    const rt = realtime();
    if (rt?.memory_used_pct) {
      const v = extractValue(rt.memory_used_pct);
      if (v !== null) return v;
    }
    return null;
  };

  // QPS chart data from range query
  const chartData = () => {
    const ts = extractTimeSeries(qpsTimeline());
    if (ts.timestamps.length === 0) return null;
    return [ts.timestamps, ts.values] as [number[], number[]];
  };

  // Protocol distribution for donut chart
  const protocolChartData = () => {
    const dist = protocolDist();
    if (!dist || dist.length === 0) return [];
    const colors = ["#3b82f6", "#22c55e", "#eab308", "#ef4444", "#a855f7"];
    return dist.map((d, i) => ({
      label: d.label,
      value: d.count,
      color: colors[i % colors.length],
    }));
  };

  return (
    <Layout>
      <div class="space-y-6">
        {/* Header */}
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-bold text-[var(--color-text)]">Overview</h1>
            <p class="text-sm text-slate-400 mt-1">
              Knot Resolver real-time monitoring
              <span class={`ml-2 inline-flex items-center gap-1 text-xs ${connected() ? "text-emerald-400" : "text-slate-400"}`}>
                <span class={`w-1.5 h-1.5 rounded-full ${connected() ? "bg-emerald-400 animate-pulse" : "bg-slate-500"}`} />
                {connected() ? "Live Stream" : "Auto-refresh 15s"}
              </span>
            </p>
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
            title="Queries/sec"
            value={qps() !== null ? fmt(qps()!, 1) : "--"}
            subtitle="Total QPS across all protocols"
            color="#3b82f6"
          />
          <KPICard
            title="Avg Latency"
            value={latency() !== null ? fmt(latency()!, 0) + " ms" : "--"}
            subtitle="Median response time"
            color="#22c55e"
          />
          <KPICard
            title="Cache Hit Ratio"
            value={cacheRatio() !== null ? fmt(cacheRatio()! * 100, 1) + "%" : "--"}
            subtitle="Cache efficiency"
            color="#eab308"
          />
          <KPICard
            title="CPU / Memory"
            value={
              cpuUsage() !== null
                ? fmt(cpuUsage()!, 1) + "%" + (memUsage() !== null ? " / " + fmt(memUsage()!, 1) + "%" : "")
                : "--"
            }
            subtitle="Server resource utilization"
            color="#ef4444"
          />
        </div>

        {/* Charts Row */}
        <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
          {/* QPS Timeline */}
          <div class="lg:col-span-2 bg-slate-800 rounded-xl p-5 border border-slate-700">
            <h3 class="text-sm font-medium text-slate-400 mb-4">Query Rate (Last Hour)</h3>
            <Show
              when={chartData()}
              fallback={
                <div class="h-[280px] flex items-center justify-center text-slate-500">
                  {qpsTimeline.loading ? "Loading chart data..." : "No data yet — send DNS queries to see metrics"}
                </div>
              }
            >
              {(data) => (
                <TimeSeriesChart
                  data={data()}
                  series={[{ label: "QPS", stroke: "#3b82f6", fill: "rgba(59,130,246,0.1)", width: 2 }]}
                  height={280}
                />
              )}
            </Show>
          </div>

          {/* Protocol Distribution */}
          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <h3 class="text-sm font-medium text-slate-400 mb-4">Protocol Distribution</h3>
            <Show
              when={protocolChartData().length > 0}
              fallback={
                <div class="h-[220px] flex items-center justify-center text-slate-500 text-sm">
                  No protocol data yet
                </div>
              }
            >
              <DonutChart data={protocolChartData()} size={220} />
            </Show>
          </div>
        </div>

        {/* Top Domains */}
        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-4">Top Queried Domains (Last Hour)</h3>
          <Show
            when={topDomains() && topDomains()!.length > 0}
            fallback={
              <div class="py-8 text-center text-slate-500 text-sm">
                No domain data yet — query logs require dnstap pipeline
              </div>
            }
          >
            <div class="overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="text-slate-500 border-b border-slate-700">
                    <th class="text-left py-2 px-3">#</th>
                    <th class="text-left py-2 px-3">Domain</th>
                    <th class="text-right py-2 px-3">Queries</th>
                    <th class="text-left py-2 px-3 w-1/3">Volume</th>
                  </tr>
                </thead>
                <tbody>
                  {(topDomains() || []).map((d, i) => {
                    const maxCount = topDomains()?.[0]?.query_count || 1;
                    const pct = (d.query_count / maxCount) * 100;
                    return (
                      <tr class="border-b border-slate-700/50 hover:bg-slate-700/30">
                        <td class="py-2 px-3 text-slate-500">{i + 1}</td>
                        <td class="py-2 px-3 font-mono text-slate-300">{d.qname}</td>
                        <td class="py-2 px-3 text-right text-slate-300">{d.query_count.toLocaleString()}</td>
                        <td class="py-2 px-3">
                          <div class="h-2 rounded-full bg-slate-700 overflow-hidden">
                            <div class="h-full rounded-full bg-blue-500" style={{ width: `${pct}%` }} />
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </Show>
        </div>

        {/* Live Stats Summary */}
        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-3">Resolver Status</h3>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            <div>
              <p class="text-slate-500">Total Queries</p>
              <p class="text-lg font-medium text-[var(--color-text)]">
                {overview()?.qps ? (() => {
                  const r = overview()!.qps?.data?.result;
                  return r?.[0] ? "Active" : "Idle";
                })() : "--"}
              </p>
            </div>
            <div>
              <p class="text-slate-500">DNSSEC</p>
              <p class="text-lg font-medium text-emerald-400">Enabled</p>
            </div>
            <div>
              <p class="text-slate-500">Cache Size</p>
              <p class="text-lg font-medium text-[var(--color-text)]">{resolver()?.cache?.["size-max"] || "--"}</p>
            </div>
            <div>
              <p class="text-slate-500">Serve Stale</p>
              <p class="text-lg font-medium text-emerald-400">Enabled</p>
            </div>
          </div>
        </div>
      </div>
    </Layout>
  );
}
