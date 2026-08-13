import { createResource, createSignal, For, Show } from "solid-js";
import Layout from "~/components/Layout";
import { alertsAPI, type AlertRule } from "~/lib/api";

export default function AlertsPage() {
  const [rules, { refetch }] = createResource(() => alertsAPI.list());
  const [history] = createResource(() => alertsAPI.history());
  const [showForm, setShowForm] = createSignal(false);

  const [form, setForm] = createSignal<Partial<AlertRule>>({
    name: "",
    metric: "qps",
    condition: ">",
    threshold: 0,
    duration_sec: 60,
    enabled: true,
    notify_channels: [],
  });

  const handleSubmit = async (e: Event) => {
    e.preventDefault();
    await alertsAPI.create(form());
    setShowForm(false);
    refetch();
  };

  const handleDelete = async (id: number) => {
    await alertsAPI.delete(id);
    refetch();
  };

  const handleToggle = async (rule: AlertRule) => {
    await alertsAPI.update(rule.id, { ...rule, enabled: !rule.enabled });
    refetch();
  };

  return (
    <Layout>
      <div class="space-y-6">
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-bold text-[var(--color-text)]">Alerts</h1>
            <p class="text-sm text-slate-400 mt-1">Alert rules and notification management</p>
          </div>
          <button
            onClick={() => setShowForm(!showForm())}
            class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white text-sm rounded-lg transition-colors"
          >
            {showForm() ? "Cancel" : "+ New Alert Rule"}
          </button>
        </div>

        {/* Create Alert Form */}
        <Show when={showForm()}>
          <div class="bg-slate-800 rounded-xl p-5 border border-blue-500/30">
            <h3 class="text-sm font-medium text-[var(--color-text)] mb-4">Create Alert Rule</h3>
            <form onSubmit={handleSubmit} class="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <label class="block text-xs text-slate-400 mb-1">Name</label>
                <input
                  type="text"
                  required
                  class="w-full bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
                  value={form().name}
                  onInput={(e) => setForm({ ...form(), name: e.target.value })}
                />
              </div>
              <div>
                <label class="block text-xs text-slate-400 mb-1">Metric</label>
                <select
                  class="w-full bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
                  value={form().metric}
                  onChange={(e) => setForm({ ...form(), metric: e.target.value })}
                >
                  <option value="qps">QPS (Queries per second)</option>
                  <option value="latency_p95">Latency P95</option>
                  <option value="cache_hit_ratio">Cache Hit Ratio</option>
                  <option value="dnssec_bogus_rate">DNSSEC Bogus Rate</option>
                  <option value="cpu_usage">CPU Usage %</option>
                  <option value="memory_usage">Memory Usage %</option>
                  <option value="servfail_rate">SERVFAIL Rate</option>
                </select>
              </div>
              <div>
                <label class="block text-xs text-slate-400 mb-1">Condition</label>
                <div class="flex gap-2">
                  <select
                    class="bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
                    value={form().condition}
                    onChange={(e) => setForm({ ...form(), condition: e.target.value })}
                  >
                    <option value=">">Greater than</option>
                    <option value="<">Less than</option>
                    <option value=">=">Greater or equal</option>
                    <option value="<=">Less or equal</option>
                  </select>
                  <input
                    type="number"
                    step="any"
                    required
                    class="flex-1 bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
                    value={form().threshold}
                    onInput={(e) => setForm({ ...form(), threshold: parseFloat(e.target.value) })}
                  />
                </div>
              </div>
              <div>
                <label class="block text-xs text-slate-400 mb-1">Duration (seconds)</label>
                <input
                  type="number"
                  required
                  class="w-full bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
                  value={form().duration_sec}
                  onInput={(e) => setForm({ ...form(), duration_sec: parseInt(e.target.value) })}
                />
              </div>
              <div class="md:col-span-2">
                <button
                  type="submit"
                  class="px-6 py-2 bg-blue-600 hover:bg-blue-700 text-white text-sm rounded-lg transition-colors"
                >
                  Create Rule
                </button>
              </div>
            </form>
          </div>
        </Show>

        {/* Alert Rules Table */}
        <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
          <div class="px-5 py-3 border-b border-slate-700">
            <h3 class="text-sm font-medium text-slate-400">Alert Rules</h3>
          </div>
          <table class="w-full text-sm">
            <thead>
              <tr class="bg-slate-900/50 text-slate-400 border-b border-slate-700">
                <th class="text-left py-3 px-4">Status</th>
                <th class="text-left py-3 px-4">Name</th>
                <th class="text-left py-3 px-4">Metric</th>
                <th class="text-left py-3 px-4">Condition</th>
                <th class="text-left py-3 px-4">Duration</th>
                <th class="text-right py-3 px-4">Actions</th>
              </tr>
            </thead>
            <tbody>
              <Show when={rules() && rules()!.length > 0} fallback={
                <tr><td colspan="6" class="py-8 text-center text-slate-500">No alert rules configured</td></tr>
              }>
                <For each={rules()}>
                  {(rule) => (
                    <tr class="border-b border-slate-700/50 hover:bg-slate-700/30">
                      <td class="py-3 px-4">
                        <button onClick={() => handleToggle(rule)}>
                          <span class={`w-2.5 h-2.5 rounded-full inline-block ${rule.enabled ? "bg-emerald-500" : "bg-slate-600"}`} />
                        </button>
                      </td>
                      <td class="py-3 px-4 text-slate-300 font-medium">{rule.name}</td>
                      <td class="py-3 px-4 text-slate-400">{rule.metric}</td>
                      <td class="py-3 px-4 font-mono text-slate-300">
                        {rule.condition} {rule.threshold}
                      </td>
                      <td class="py-3 px-4 text-slate-400">{rule.duration_sec}s</td>
                      <td class="py-3 px-4 text-right">
                        <button
                          onClick={() => handleDelete(rule.id)}
                          class="text-red-400 hover:text-red-300 text-xs"
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  )}
                </For>
              </Show>
            </tbody>
          </table>
        </div>

        {/* Alert History */}
        <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
          <div class="px-5 py-3 border-b border-slate-700">
            <h3 class="text-sm font-medium text-slate-400">Alert History</h3>
          </div>
          <div class="p-5">
            <Show when={history() && history()!.length > 0} fallback={
              <p class="text-sm text-slate-500">No alerts fired yet.</p>
            }>
              <For each={history()}>
                {(event) => (
                  <div class="flex items-start gap-3 py-3 border-b border-slate-700/50 last:border-0">
                    <span class={`w-2 h-2 rounded-full mt-1.5 flex-shrink-0 ${
                      event.status === "firing" ? "bg-red-500" : "bg-emerald-500"
                    }`} />
                    <div>
                      <p class="text-sm text-slate-300">
                        <span class="font-medium">{event.rule_name}</span>
                        {" "}{event.message}
                      </p>
                      <p class="text-xs text-slate-500 mt-0.5">
                        {new Date(event.fired_at).toLocaleString()}
                        {event.resolved_at && ` - Resolved: ${new Date(event.resolved_at).toLocaleString()}`}
                      </p>
                    </div>
                  </div>
                )}
              </For>
            </Show>
          </div>
        </div>
      </div>
    </Layout>
  );
}
