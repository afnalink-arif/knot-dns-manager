import { createResource, createSignal, Show } from "solid-js";
import Layout from "~/components/Layout";
import TimeSeriesChart from "~/components/charts/TimeSeriesChart";
import DonutChart from "~/components/charts/DonutChart";
import { queriesAPI, metricsAPI } from "~/lib/api";
import { extractTimeSeries } from "~/lib/prometheus";

export default function QueryMetrics() {
  const [hours, setHours] = createSignal("1");
  const [qpsData] = createResource(hours, () => metricsAPI.qps({ step: "15s" }));
  const [latencyData] = createResource(hours, () => metricsAPI.latency());
  const [typeDist] = createResource(hours, (h) => queriesAPI.typeDistribution({ hours: h }));
  const [rcodeDist] = createResource(hours, (h) => queriesAPI.rcodeDistribution({ hours: h }));
  const [protoDist] = createResource(hours, (h) => queriesAPI.protocolDistribution({ hours: h }));

  const chartColors = ["#3b82f6", "#22c55e", "#eab308", "#ef4444", "#a855f7", "#ec4899", "#06b6d4", "#f97316"];

  const toDonutData = (dist: any[] | undefined) =>
    (dist || []).map((d, i) => ({
      label: d.label,
      value: d.count,
      color: chartColors[i % chartColors.length],
    }));

  const qpsChartData = () => {
    const ts = extractTimeSeries(qpsData());
    if (ts.timestamps.length === 0) return null;
    return [ts.timestamps, ts.values] as [number[], number[]];
  };

  return (
    <Layout>
      <div class="space-y-6">
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-bold text-[var(--color-text)]">Query Metrics</h1>
            <p class="text-sm text-slate-400 mt-1">DNS query analysis and breakdown</p>
          </div>
          <select
            class="bg-slate-800 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
            value={hours()}
            onChange={(e) => setHours(e.target.value)}
          >
            <option value="1">Last 1 hour</option>
            <option value="6">Last 6 hours</option>
            <option value="24">Last 24 hours</option>
          </select>
        </div>

        {/* QPS Timeline */}
        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-4">Query Rate Over Time</h3>
          <Show
            when={qpsChartData()}
            fallback={
              <div class="h-[320px] flex items-center justify-center text-slate-500">
                {qpsData.loading ? "Loading..." : "No data yet"}
              </div>
            }
          >
            {(data) => (
              <TimeSeriesChart
                data={data()}
                series={[{ label: "QPS", stroke: "#3b82f6", fill: "rgba(59,130,246,0.1)", width: 2 }]}
                height={320}
              />
            )}
          </Show>
        </div>

        {/* Distribution Charts */}
        <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <Show
              when={toDonutData(typeDist()).length > 0}
              fallback={<div class="h-[250px] flex items-center justify-center text-slate-500 text-sm">No type data (requires ClickHouse logs)</div>}
            >
              <DonutChart data={toDonutData(typeDist())} size={200} title="Query Type Distribution" />
            </Show>
          </div>
          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <Show
              when={toDonutData(rcodeDist()).length > 0}
              fallback={<div class="h-[250px] flex items-center justify-center text-slate-500 text-sm">No rcode data (requires ClickHouse logs)</div>}
            >
              <DonutChart data={toDonutData(rcodeDist())} size={200} title="Response Code Distribution" />
            </Show>
          </div>
          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <Show
              when={toDonutData(protoDist()).length > 0}
              fallback={<div class="h-[250px] flex items-center justify-center text-slate-500 text-sm">No protocol data (requires ClickHouse logs)</div>}
            >
              <DonutChart data={toDonutData(protoDist())} size={200} title="Protocol Distribution" />
            </Show>
          </div>
        </div>
      </div>
    </Layout>
  );
}
