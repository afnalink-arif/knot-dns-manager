import { createResource } from "solid-js";
import Layout from "~/components/Layout";
import { metricsAPI } from "~/lib/api";

export default function UpstreamsPage() {
  const [upstreams] = createResource(() => metricsAPI.upstreams());

  return (
    <Layout>
      <div class="space-y-6">
        <div>
          <h1 class="text-2xl font-bold text-[var(--color-text)]">Upstream Servers</h1>
          <p class="text-sm text-slate-400 mt-1">Health and performance of upstream DNS servers</p>
        </div>

        {/* Upstream Table */}
        <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
          <table class="w-full text-sm">
            <thead>
              <tr class="bg-slate-900/50 text-slate-400 border-b border-slate-700">
                <th class="text-left py-3 px-4">Status</th>
                <th class="text-left py-3 px-4">Server</th>
                <th class="text-left py-3 px-4">Protocol</th>
                <th class="text-right py-3 px-4">Latency (ms)</th>
                <th class="text-right py-3 px-4">Queries/s</th>
                <th class="text-right py-3 px-4">Failures/s</th>
                <th class="text-left py-3 px-4">Trend</th>
              </tr>
            </thead>
            <tbody>
              {/* Placeholder rows - will be populated from Prometheus data */}
              <tr class="border-b border-slate-700/50 hover:bg-slate-700/30">
                <td class="py-3 px-4">
                  <span class="w-2.5 h-2.5 rounded-full bg-emerald-500 inline-block" />
                </td>
                <td class="py-3 px-4 font-mono text-slate-300">Root Servers (auto)</td>
                <td class="py-3 px-4">
                  <span class="px-2 py-0.5 bg-blue-500/20 text-blue-400 rounded text-xs">UDP/TCP</span>
                </td>
                <td class="py-3 px-4 text-right text-slate-300">--</td>
                <td class="py-3 px-4 text-right text-slate-300">--</td>
                <td class="py-3 px-4 text-right text-slate-300">--</td>
                <td class="py-3 px-4 text-slate-500">Waiting for data...</td>
              </tr>
            </tbody>
          </table>
        </div>

        {/* Info Card */}
        <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <h3 class="text-sm font-medium text-slate-400 mb-3">Resolver Mode</h3>
          <p class="text-sm text-slate-300">
            Knot Resolver is operating as a <span class="text-blue-400 font-medium">full recursive resolver</span>,
            querying authoritative name servers directly from root. No forwarding configured.
          </p>
          <p class="text-xs text-slate-500 mt-2">
            To configure forwarding to upstream resolvers, edit <code class="text-slate-400">config.yaml</code> and add a
            <code class="text-slate-400"> forward</code> section.
          </p>
        </div>
      </div>
    </Layout>
  );
}
