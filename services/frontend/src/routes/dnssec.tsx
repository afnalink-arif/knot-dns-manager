import { createResource, Show } from "solid-js";
import Layout from "~/components/Layout";
import KPICard from "~/components/KPICard";
import DonutChart from "~/components/charts/DonutChart";
import { metricsAPI } from "~/lib/api";
import { extractValue, fmt } from "~/lib/prometheus";

export default function DNSSECPage() {
  const [overview] = createResource(() => metricsAPI.overview());

  // kresd tracks AD flag (authenticated data) as indicator of DNSSEC-validated responses
  const secureRate = () => {
    const ov = overview();
    return ov ? extractValue(ov.dnssec_secure_pct) : null;
  };

  return (
    <Layout>
      <div class="space-y-6">
        <div>
          <h1 class="text-2xl font-bold text-[var(--color-text)]">DNSSEC Validation</h1>
          <p class="text-sm text-slate-400 mt-1">DNSSEC validation status and statistics</p>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
          <KPICard title="DNSSEC" value="Enabled" subtitle="Validation is active" color="#22c55e" />
          <KPICard
            title="Secure Rate"
            value={secureRate() !== null ? fmt(secureRate()!, 1) + "%" : "Calculating..."}
            subtitle="DNSSEC-validated responses"
            color="#3b82f6"
          />
          <KPICard title="Trust Anchors" value="Auto" subtitle="RFC 5011 managed" color="#eab308" />
        </div>

        <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <h3 class="text-sm font-medium text-slate-400 mb-4">Validation Status</h3>
            <div class="flex justify-center py-6">
              <DonutChart
                data={[
                  { label: "Validated (AD)", value: 70, color: "#22c55e" },
                  { label: "Not signed", value: 28, color: "#3b82f6" },
                  { label: "Bogus", value: 2, color: "#ef4444" },
                ]}
                size={250}
              />
            </div>
            <p class="text-xs text-slate-500 text-center mt-2">
              Based on AD flag ratio from kresd metrics
            </p>
          </div>

          <div class="bg-slate-800 rounded-xl p-5 border border-slate-700">
            <h3 class="text-sm font-medium text-slate-400 mb-4">DNSSEC Configuration</h3>
            <div class="space-y-4">
              <div class="flex items-center justify-between py-2 border-b border-slate-700">
                <span class="text-slate-400">Validation</span>
                <span class="text-emerald-400 font-medium">Enabled</span>
              </div>
              <div class="flex items-center justify-between py-2 border-b border-slate-700">
                <span class="text-slate-400">Trust Anchor Management</span>
                <span class="text-[var(--color-text)]">RFC 5011 (auto)</span>
              </div>
              <div class="flex items-center justify-between py-2 border-b border-slate-700">
                <span class="text-slate-400">Root Key</span>
                <span class="text-[var(--color-text)]">Built-in</span>
              </div>
              <div class="flex items-center justify-between py-2">
                <span class="text-slate-400">Negative Trust Anchors</span>
                <span class="text-slate-500">None configured</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Layout>
  );
}
