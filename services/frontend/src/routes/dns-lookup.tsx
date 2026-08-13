import { createSignal, Show, For } from "solid-js";
import Layout from "~/components/Layout";
import { authHeaders } from "~/lib/auth";

type DNSRecord = {
  name: string;
  ttl: number;
  type: string;
  value: string;
};

type LookupResult = {
  domain: string;
  server: string;
  server_addr: string;
  query_type: string;
  status: string;
  status_text: string;
  records: DNSRecord[];
  raw_output: string;
  query_time_ms: number;
  blocked: boolean;
  block_reason?: string;
};

const RECORD_TYPES = ["A", "AAAA", "CNAME", "MX", "NS", "TXT", "SOA", "SRV", "PTR"];
const SERVERS = [
  { value: "local", label: "Local Resolver (kresd)" },
  { value: "google", label: "Google DNS (8.8.8.8)" },
  { value: "cloudflare", label: "Cloudflare (1.1.1.1)" },
  { value: "quad9", label: "Quad9 (9.9.9.9)" },
];

export default function DNSLookupPage() {
  const [domain, setDomain] = createSignal("");
  const [qtype, setQtype] = createSignal("A");
  const [server, setServer] = createSignal("local");
  const [loading, setLoading] = createSignal(false);
  const [result, setResult] = createSignal<LookupResult | null>(null);
  const [compareResult, setCompareResult] = createSignal<LookupResult | null>(null);
  const [showRaw, setShowRaw] = createSignal(false);
  const [history, setHistory] = createSignal<{ domain: string; type: string; status: string; time: string }[]>([]);
  const [error, setError] = createSignal("");

  const doLookup = async (e?: Event) => {
    e?.preventDefault();
    const d = domain().trim();
    if (!d) return;

    setLoading(true);
    setResult(null);
    setCompareResult(null);
    setError("");
    setShowRaw(false);

    try {
      // Main lookup
      const res = await fetch("/api/admin/dns/lookup", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ domain: d, type: qtype(), server: server() }),
      });
      const data: LookupResult = await res.json();
      if (!res.ok) throw new Error((data as any).error || "Lookup failed");
      setResult(data);

      // If using local, also query Google DNS for comparison
      if (server() === "local") {
        const extRes = await fetch("/api/admin/dns/lookup", {
          method: "POST",
          headers: { "Content-Type": "application/json", ...authHeaders() },
          body: JSON.stringify({ domain: d, type: qtype(), server: "google" }),
        });
        if (extRes.ok) {
          setCompareResult(await extRes.json());
        }
      }

      // Add to history
      setHistory((prev) => [
        { domain: d, type: qtype(), status: data.status, time: new Date().toLocaleTimeString() },
        ...prev.slice(0, 19),
      ]);
    } catch (err: any) {
      setError(err.message);
    }
    setLoading(false);
  };

  const statusColor = (status: string) => {
    if (status === "ok") return "text-emerald-400";
    if (status === "blocked") return "text-red-400";
    if (status === "nxdomain") return "text-amber-400";
    return "text-slate-400";
  };

  const statusBg = (status: string) => {
    if (status === "ok") return "bg-emerald-500/10 border-emerald-500/20";
    if (status === "blocked") return "bg-red-500/10 border-red-500/20";
    if (status === "nxdomain") return "bg-amber-500/10 border-amber-500/20";
    return "bg-slate-700/50 border-slate-600";
  };

  const typeColor = (type: string) => {
    const colors: Record<string, string> = {
      A: "bg-blue-500/20 text-blue-300",
      AAAA: "bg-indigo-500/20 text-indigo-300",
      CNAME: "bg-purple-500/20 text-purple-300",
      MX: "bg-pink-500/20 text-pink-300",
      NS: "bg-cyan-500/20 text-cyan-300",
      TXT: "bg-amber-500/20 text-amber-300",
      SOA: "bg-slate-500/20 text-slate-300",
      SRV: "bg-teal-500/20 text-teal-300",
    };
    return colors[type] || "bg-slate-500/20 text-slate-300";
  };

  return (
    <Layout>
      <div class="space-y-5">
        {/* Header */}
        <div>
          <h1 class="text-xl font-bold text-[var(--color-text)]">DNS Lookup</h1>
          <p class="text-xs text-slate-500 mt-1">Test DNS resolution directly from the dashboard</p>
        </div>

        {/* Lookup Form */}
        <form onSubmit={doLookup} class="bg-slate-800 rounded-xl p-5 border border-slate-700">
          <div class="flex flex-col md:flex-row gap-3">
            {/* Domain input */}
            <div class="flex-1">
              <label class="block text-[10px] text-slate-500 mb-1">Domain</label>
              <input
                type="text"
                placeholder="e.g. google.com, reddit.com"
                value={domain()}
                onInput={(e) => setDomain(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && doLookup()}
                class="w-full bg-slate-900/70 border border-slate-700 rounded-lg px-4 py-2.5 text-sm text-[var(--color-text)] placeholder-slate-600 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500/30 transition font-mono"
                autofocus
              />
            </div>

            {/* Record type */}
            <div class="w-full md:w-28">
              <label class="block text-[10px] text-slate-500 mb-1">Type</label>
              <select
                value={qtype()}
                onChange={(e) => setQtype(e.target.value)}
                class="w-full bg-slate-900/70 border border-slate-700 rounded-lg px-3 py-2.5 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
              >
                <For each={RECORD_TYPES}>
                  {(t) => <option value={t}>{t}</option>}
                </For>
              </select>
            </div>

            {/* Server */}
            <div class="w-full md:w-56">
              <label class="block text-[10px] text-slate-500 mb-1">Server</label>
              <select
                value={server()}
                onChange={(e) => setServer(e.target.value)}
                class="w-full bg-slate-900/70 border border-slate-700 rounded-lg px-3 py-2.5 text-sm text-[var(--color-text)] focus:outline-none focus:border-blue-500 transition"
              >
                <For each={SERVERS}>
                  {(s) => <option value={s.value}>{s.label}</option>}
                </For>
              </select>
            </div>

            {/* Submit */}
            <div class="flex items-end">
              <button
                type="submit"
                disabled={loading() || !domain().trim()}
                class="w-full md:w-auto px-6 py-2.5 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors flex items-center gap-2"
              >
                <Show when={loading()}>
                  <svg class="w-4 h-4 animate-spin" viewBox="0 0 24 24" fill="none">
                    <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                    <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                  </svg>
                </Show>
                Lookup
              </button>
            </div>
          </div>

          {/* Quick domain buttons */}
          <div class="flex flex-wrap gap-1.5 mt-3">
            <span class="text-[10px] text-slate-600 self-center mr-1">Quick:</span>
            <For each={["google.com", "facebook.com", "reddit.com", "pornhub.com", "betting.com", "localhost"]}>
              {(d) => (
                <button
                  type="button"
                  onClick={() => { setDomain(d); setTimeout(doLookup, 50); }}
                  class="px-2 py-0.5 text-[10px] bg-slate-700/50 hover:bg-slate-700 text-slate-400 hover:text-[var(--color-text)] rounded transition-colors"
                >
                  {d}
                </button>
              )}
            </For>
          </div>
        </form>

        {/* Error */}
        <Show when={error()}>
          <div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400">{error()}</div>
        </Show>

        {/* Result */}
        <Show when={result()}>
          {(r) => (
            <div class="space-y-4">
              {/* Status banner */}
              <div class={`rounded-xl p-4 border ${statusBg(r().status)}`}>
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-3">
                    <div class={`w-10 h-10 rounded-full flex items-center justify-center ${
                      r().status === "ok" ? "bg-emerald-500/20" :
                      r().status === "blocked" ? "bg-red-500/20" :
                      r().status === "nxdomain" ? "bg-amber-500/20" : "bg-slate-600"
                    }`}>
                      <Show when={r().status === "ok"}>
                        <svg class="w-5 h-5 text-emerald-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
                        </svg>
                      </Show>
                      <Show when={r().status === "blocked"}>
                        <svg class="w-5 h-5 text-red-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" />
                        </svg>
                      </Show>
                      <Show when={r().status === "nxdomain"}>
                        <svg class="w-5 h-5 text-amber-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
                        </svg>
                      </Show>
                      <Show when={r().status === "error"}>
                        <svg class="w-5 h-5 text-slate-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
                        </svg>
                      </Show>
                    </div>
                    <div>
                      <p class={`text-sm font-semibold ${statusColor(r().status)}`}>{r().status_text}</p>
                      <p class="text-[10px] text-slate-500 mt-0.5">
                        {r().domain} · {r().query_type} · {r().server} · {r().query_time_ms}ms
                      </p>
                    </div>
                  </div>
                  <Show when={r().blocked && r().block_reason}>
                    <div class="text-right">
                      <span class="inline-flex items-center gap-1 px-2 py-1 bg-red-500/10 border border-red-500/20 rounded text-[10px] text-red-400">
                        <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
                        </svg>
                        {r().block_reason}
                      </span>
                    </div>
                  </Show>
                </div>
              </div>

              {/* Records table */}
              <Show when={r().records.length > 0}>
                <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
                  <div class="px-4 py-2.5 border-b border-slate-700 flex items-center justify-between">
                    <span class="text-xs text-slate-400 font-medium">{r().records.length} Record{r().records.length > 1 ? "s" : ""}</span>
                    <span class="text-[10px] text-slate-600">Query time: {r().query_time_ms}ms</span>
                  </div>
                  <div class="overflow-x-auto">
                    <table class="w-full text-xs">
                      <thead>
                        <tr class="border-b border-slate-700/50">
                          <th class="px-4 py-2 text-left text-slate-500 font-medium">Name</th>
                          <th class="px-4 py-2 text-left text-slate-500 font-medium w-16">TTL</th>
                          <th class="px-4 py-2 text-left text-slate-500 font-medium w-20">Type</th>
                          <th class="px-4 py-2 text-left text-slate-500 font-medium">Value</th>
                        </tr>
                      </thead>
                      <tbody>
                        <For each={r().records}>
                          {(rec) => (
                            <tr class="border-b border-slate-700/30 hover:bg-slate-700/20 transition-colors">
                              <td class="px-4 py-2 font-mono text-slate-300">{rec.name}</td>
                              <td class="px-4 py-2 text-slate-500">{rec.ttl}s</td>
                              <td class="px-4 py-2">
                                <span class={`px-1.5 py-0.5 rounded text-[10px] font-medium ${typeColor(rec.type)}`}>{rec.type}</span>
                              </td>
                              <td class="px-4 py-2 font-mono text-[var(--color-text)] break-all">{rec.value}</td>
                            </tr>
                          )}
                        </For>
                      </tbody>
                    </table>
                  </div>
                </div>
              </Show>

              {/* Comparison with external DNS */}
              <Show when={compareResult() && server() === "local"}>
                {(ext) => {
                  const localRec = () => r().records.map((r) => r.value).sort().join(", ");
                  const extRec = () => compareResult()!.records.map((r) => r.value).sort().join(", ");
                  const match = () => localRec() === extRec();
                  const localBlocked = () => r().blocked || r().status === "nxdomain";
                  const extOk = () => compareResult()!.records.length > 0;

                  return (
                    <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
                      <div class="px-4 py-2.5 border-b border-slate-700">
                        <span class="text-xs text-slate-400 font-medium">Comparison: Local vs Google DNS</span>
                      </div>
                      <div class="grid grid-cols-2 divide-x divide-slate-700">
                        <div class="p-4">
                          <div class="flex items-center gap-2 mb-2">
                            <span class={`w-2 h-2 rounded-full ${r().status === "ok" ? "bg-emerald-400" : r().blocked ? "bg-red-400" : "bg-amber-400"}`} />
                            <span class="text-[10px] text-slate-500">Local (kresd)</span>
                          </div>
                          <Show when={r().records.length > 0} fallback={
                            <p class={`text-xs font-mono ${r().blocked ? "text-red-400" : "text-amber-400"}`}>
                              {r().blocked ? "BLOCKED" : "NXDOMAIN"}
                            </p>
                          }>
                            <For each={r().records}>
                              {(rec) => <p class="text-xs font-mono text-[var(--color-text)]">{rec.value}</p>}
                            </For>
                          </Show>
                          <p class="text-[10px] text-slate-600 mt-1">{r().query_time_ms}ms</p>
                        </div>
                        <div class="p-4">
                          <div class="flex items-center gap-2 mb-2">
                            <span class={`w-2 h-2 rounded-full ${extOk() ? "bg-emerald-400" : "bg-amber-400"}`} />
                            <span class="text-[10px] text-slate-500">Google DNS (8.8.8.8)</span>
                          </div>
                          <Show when={compareResult()!.records.length > 0} fallback={
                            <p class="text-xs font-mono text-amber-400">NXDOMAIN</p>
                          }>
                            <For each={compareResult()!.records}>
                              {(rec) => <p class="text-xs font-mono text-[var(--color-text)]">{rec.value}</p>}
                            </For>
                          </Show>
                          <p class="text-[10px] text-slate-600 mt-1">{compareResult()!.query_time_ms}ms</p>
                        </div>
                      </div>
                      <Show when={localBlocked() && extOk()}>
                        <div class="px-4 py-2 bg-red-500/5 border-t border-red-500/10 text-[10px] text-red-400">
                          Blocked on local resolver but resolves on Google DNS — filtering is working
                        </div>
                      </Show>
                      <Show when={match() && !localBlocked()}>
                        <div class="px-4 py-2 bg-emerald-500/5 border-t border-emerald-500/10 text-[10px] text-emerald-400">
                          Results match — local resolver is working correctly
                        </div>
                      </Show>
                    </div>
                  );
                }}
              </Show>

              {/* Raw output */}
              <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
                <button
                  onClick={() => setShowRaw(!showRaw())}
                  class="w-full px-4 py-2.5 flex items-center justify-between hover:bg-slate-700/30 transition-colors"
                >
                  <span class="text-xs text-slate-400 font-medium">Raw Output</span>
                  <svg class={`w-4 h-4 text-slate-500 transition-transform ${showRaw() ? "rotate-180" : ""}`} fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M19 9l-7 7-7-7" />
                  </svg>
                </button>
                <Show when={showRaw()}>
                  <div class="px-4 pb-3 border-t border-slate-700">
                    <pre class="mt-2 text-[11px] font-mono text-slate-400 whitespace-pre-wrap leading-5 max-h-64 overflow-y-auto">{r().raw_output}</pre>
                  </div>
                </Show>
              </div>
            </div>
          )}
        </Show>

        {/* History */}
        <Show when={history().length > 0}>
          <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
            <div class="px-4 py-2.5 border-b border-slate-700 flex items-center justify-between">
              <span class="text-xs text-slate-400 font-medium">Lookup History</span>
              <button onClick={() => setHistory([])} class="text-[10px] text-slate-600 hover:text-slate-400 transition-colors">
                Clear
              </button>
            </div>
            <div class="divide-y divide-slate-700/30 max-h-48 overflow-y-auto">
              <For each={history()}>
                {(h) => (
                  <button
                    onClick={() => { setDomain(h.domain); setQtype(h.type); setTimeout(doLookup, 50); }}
                    class="w-full px-4 py-2 flex items-center gap-3 hover:bg-slate-700/20 transition-colors text-left"
                  >
                    <span class={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${
                      h.status === "ok" ? "bg-emerald-400" :
                      h.status === "blocked" ? "bg-red-400" :
                      h.status === "nxdomain" ? "bg-amber-400" : "bg-slate-500"
                    }`} />
                    <span class="text-xs font-mono text-[var(--color-text)] flex-1">{h.domain}</span>
                    <span class={`px-1.5 py-0.5 rounded text-[10px] font-medium ${typeColor(h.type)}`}>{h.type}</span>
                    <span class="text-[10px] text-slate-600">{h.time}</span>
                  </button>
                )}
              </For>
            </div>
          </div>
        </Show>
      </div>
    </Layout>
  );
}
