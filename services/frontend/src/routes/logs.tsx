import { createResource, createSignal, For, Show } from "solid-js";
import Layout from "~/components/Layout";
import { queriesAPI, type QueryLogEntry } from "~/lib/api";

const QTYPE_MAP: Record<number, string> = {
  1: "A", 2: "NS", 5: "CNAME", 6: "SOA", 12: "PTR",
  15: "MX", 16: "TXT", 28: "AAAA", 33: "SRV", 43: "DS",
  46: "RRSIG", 48: "DNSKEY", 65: "HTTPS", 255: "ANY",
};

const RCODE_MAP: Record<number, string> = {
  0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN",
  4: "NOTIMP", 5: "REFUSED",
};

const RCODE_COLORS: Record<string, string> = {
  NOERROR: "text-emerald-400", NXDOMAIN: "text-yellow-400",
  SERVFAIL: "text-red-400", REFUSED: "text-red-400",
};

export default function QueryLogs() {
  const [filters, setFilters] = createSignal({
    domain: "", client_ip: "", qtype: "", rcode: "", protocol: "", limit: "100", offset: "0",
  });

  const [logs, { refetch }] = createResource(filters, (f) => {
    const params: Record<string, string> = {};
    Object.entries(f).forEach(([k, v]) => { if (v) params[k] = v; });
    return queriesAPI.search(params);
  });

  const updateFilter = (key: string, value: string) => {
    setFilters((prev) => ({ ...prev, [key]: value, offset: "0" }));
  };

  return (
    <Layout>
      <div class="space-y-6">
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-bold text-[var(--color-text)]">Query Logs</h1>
            <p class="text-sm text-slate-400 mt-1">Detailed DNS query log from ClickHouse</p>
          </div>
          <button
            onClick={() => refetch()}
            class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white text-sm rounded-lg transition-colors"
          >
            Refresh
          </button>
        </div>

        {/* Filters */}
        <div class="bg-slate-800 rounded-xl p-4 border border-slate-700">
          <div class="grid grid-cols-2 md:grid-cols-5 gap-3">
            <input
              type="text"
              placeholder="Domain filter..."
              class="bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300 placeholder:text-slate-600"
              value={filters().domain}
              onInput={(e) => updateFilter("domain", e.target.value)}
            />
            <input
              type="text"
              placeholder="Client IP..."
              class="bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300 placeholder:text-slate-600"
              value={filters().client_ip}
              onInput={(e) => updateFilter("client_ip", e.target.value)}
            />
            <select
              class="bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
              value={filters().qtype}
              onChange={(e) => updateFilter("qtype", e.target.value)}
            >
              <option value="">All Types</option>
              <option value="1">A</option>
              <option value="28">AAAA</option>
              <option value="5">CNAME</option>
              <option value="15">MX</option>
              <option value="16">TXT</option>
              <option value="33">SRV</option>
              <option value="65">HTTPS</option>
            </select>
            <select
              class="bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
              value={filters().rcode}
              onChange={(e) => updateFilter("rcode", e.target.value)}
            >
              <option value="">All RCodes</option>
              <option value="0">NOERROR</option>
              <option value="2">SERVFAIL</option>
              <option value="3">NXDOMAIN</option>
              <option value="5">REFUSED</option>
            </select>
            <select
              class="bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-300"
              value={filters().protocol}
              onChange={(e) => updateFilter("protocol", e.target.value)}
            >
              <option value="">All Protocols</option>
              <option value="udp">UDP</option>
              <option value="tcp">TCP</option>
              <option value="dot">DoT</option>
              <option value="doh">DoH</option>
              <option value="doq">DoQ</option>
            </select>
          </div>
        </div>

        {/* Logs Table */}
        <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="bg-slate-900/50 text-slate-400 border-b border-slate-700">
                  <th class="text-left py-3 px-3 whitespace-nowrap">Timestamp</th>
                  <th class="text-left py-3 px-3">Client IP</th>
                  <th class="text-left py-3 px-3">Domain</th>
                  <th class="text-left py-3 px-3">Type</th>
                  <th class="text-left py-3 px-3">RCode</th>
                  <th class="text-right py-3 px-3">Latency</th>
                  <th class="text-left py-3 px-3">Protocol</th>
                  <th class="text-left py-3 px-3">DNSSEC</th>
                  <th class="text-left py-3 px-3">Cached</th>
                </tr>
              </thead>
              <tbody>
                <Show when={logs()?.data} fallback={
                  <tr><td colspan="9" class="py-8 text-center text-slate-500">Loading...</td></tr>
                }>
                  <For each={logs()!.data}>
                    {(entry: QueryLogEntry) => {
                      const rcodeName = RCODE_MAP[entry.rcode] || `RCODE${entry.rcode}`;
                      const rcodeColor = RCODE_COLORS[rcodeName] || "text-slate-300";
                      return (
                        <tr class="border-b border-slate-700/50 hover:bg-slate-700/30">
                          <td class="py-2 px-3 text-slate-400 whitespace-nowrap font-mono text-xs">
                            {new Date(entry.timestamp).toLocaleTimeString()}
                          </td>
                          <td class="py-2 px-3 font-mono text-xs text-slate-300">{entry.client_ip}</td>
                          <td class="py-2 px-3 font-mono text-slate-200 max-w-[300px] truncate">{entry.qname}</td>
                          <td class="py-2 px-3">
                            <span class="px-1.5 py-0.5 bg-slate-700 text-slate-300 rounded text-xs">
                              {QTYPE_MAP[entry.qtype] || `TYPE${entry.qtype}`}
                            </span>
                          </td>
                          <td class={`py-2 px-3 font-medium text-xs ${rcodeColor}`}>{rcodeName}</td>
                          <td class="py-2 px-3 text-right text-slate-300 font-mono text-xs">
                            {entry.latency_us < 1000
                              ? `${entry.latency_us}us`
                              : `${(entry.latency_us / 1000).toFixed(1)}ms`}
                          </td>
                          <td class="py-2 px-3">
                            <span class="px-1.5 py-0.5 bg-blue-500/20 text-blue-400 rounded text-xs uppercase">
                              {entry.protocol}
                            </span>
                          </td>
                          <td class="py-2 px-3">
                            <span class={`text-xs ${
                              entry.dnssec_status === "secure" ? "text-emerald-400" :
                              entry.dnssec_status === "bogus" ? "text-red-400" : "text-slate-500"
                            }`}>
                              {entry.dnssec_status}
                            </span>
                          </td>
                          <td class="py-2 px-3">
                            {entry.cached ? (
                              <span class="text-emerald-400 text-xs">HIT</span>
                            ) : (
                              <span class="text-slate-500 text-xs">MISS</span>
                            )}
                          </td>
                        </tr>
                      );
                    }}
                  </For>
                </Show>
              </tbody>
            </table>
          </div>

          {/* Pagination */}
          <div class="flex items-center justify-between px-4 py-3 border-t border-slate-700">
            <p class="text-xs text-slate-500">
              Showing {logs()?.data?.length || 0} results
            </p>
            <div class="flex gap-2">
              <button
                class="px-3 py-1 bg-slate-700 text-slate-300 rounded text-sm hover:bg-slate-600 disabled:opacity-50"
                disabled={filters().offset === "0"}
                onClick={() => {
                  const newOffset = Math.max(0, parseInt(filters().offset) - parseInt(filters().limit));
                  setFilters((prev) => ({ ...prev, offset: String(newOffset) }));
                }}
              >
                Previous
              </button>
              <button
                class="px-3 py-1 bg-slate-700 text-slate-300 rounded text-sm hover:bg-slate-600"
                onClick={() => {
                  const newOffset = parseInt(filters().offset) + parseInt(filters().limit);
                  setFilters((prev) => ({ ...prev, offset: String(newOffset) }));
                }}
              >
                Next
              </button>
            </div>
          </div>
        </div>
      </div>
    </Layout>
  );
}
