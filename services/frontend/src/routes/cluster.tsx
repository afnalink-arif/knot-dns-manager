import { createResource, createSignal, createEffect, on, Show, For } from "solid-js";
import Layout from "~/components/Layout";
import KPICard from "~/components/KPICard";
import { clusterAPI, dockerCleanupAPI } from "~/lib/api";
import { authHeaders } from "~/lib/auth";
import { extractValue, fmt } from "~/lib/prometheus";

export default function ClusterPage() {
  const [overview, { refetch }] = createResource(() => clusterAPI.getOverview());
  const [updatingNodes, setUpdatingNodes] = createSignal<Set<number>>(new Set());
  const [updateOutputs, setUpdateOutputs] = createSignal<Record<number, string[]>>({});
  const [finishedNodes, setFinishedNodes] = createSignal<Set<number>>(new Set());
  const [failedNodes, setFailedNodes] = createSignal<Set<number>>(new Set());
  const [updateProgress, setUpdateProgress] = createSignal<Record<number, { step: string; pct: number }>>({});
  const [showErrorLog, setShowErrorLog] = createSignal<Set<number>>(new Set());

  // Map update.sh output lines to progress steps
  const progressSteps: [RegExp, string, number][] = [
    [/Pulling latest code/,              "Pulling latest code...",         10],
    [/Regenerating configs/,             "Regenerating configs...",        20],
    [/Rebuilding custom images/,         "Building images...",            30],
    [/Cleaning up old images/,           "Cleaning up...",                45],
    [/Restarting infrastructure/,        "Restarting infrastructure...",  50],
    [/Restarting dnstap/,                "Restarting dnstap-ingester...", 58],
    [/Restarting kresd/,                 "Restarting kresd...",           65],
    [/Restarting monitoring/,            "Restarting monitoring...",      72],
    [/Restarting frontend/,              "Restarting frontend...",        78],
    [/Restarting caddy/,                 "Restarting caddy...",           82],
    [/Running health checks/,            "Running health checks...",      88],
    [/Restarting backend/,               "Restarting backend...",         95],
    [/Update complete/,                  "Update complete",              100],
  ];

  const matchProgress = (line: string): { step: string; pct: number } | null => {
    for (const [regex, step, pct] of progressSteps) {
      if (regex.test(line)) return { step, pct };
    }
    return null;
  };
  const [cleaningNodes, setCleaningNodes] = createSignal<Set<number>>(new Set());
  const [cleanResults, setCleanResults] = createSignal<Record<number, string[]>>({});

  const onlineCount = () => {
    const o = overview();
    if (!o) return 0;
    return o.nodes.filter((n: any) => n.status === "online" || n.status === "degraded").length;
  };

  const totalNodes = () => overview()?.node_count || 0;

  const aggregateMetric = (key: string): number | null => {
    const o = overview();
    if (!o || o.nodes.length === 0) return null;
    let sum = 0;
    let count = 0;
    for (const n of o.nodes) {
      if (!n.metrics) continue;
      const v = extractValue(n.metrics[key]);
      if (v !== null && !isNaN(v)) {
        sum += v;
        count++;
      }
    }
    return count > 0 ? sum : null;
  };

  const avgMetric = (key: string): number | null => {
    const o = overview();
    if (!o || o.nodes.length === 0) return null;
    let sum = 0;
    let count = 0;
    for (const n of o.nodes) {
      if (!n.metrics) continue;
      const v = extractValue(n.metrics[key]);
      if (v !== null && !isNaN(v)) {
        sum += v;
        count++;
      }
    }
    return count > 0 ? sum / count : null;
  };

  const statusColor = (status: string) => {
    switch (status) {
      case "online": return "bg-emerald-500";
      case "degraded": return "bg-amber-500";
      case "offline": return "bg-red-500";
      default: return "bg-slate-500";
    }
  };

  const timeSince = (date: string | null) => {
    if (!date) return "Never";
    const diff = Date.now() - new Date(date).getTime();
    if (diff < 60000) return `${Math.floor(diff / 1000)}s ago`;
    if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
    return `${Math.floor(diff / 3600000)}h ago`;
  };

  const handleNodeUpdate = async (nodeId: number, isLocal: boolean) => {
    // Reset state
    setUpdatingNodes((prev) => new Set([...prev, nodeId]));
    setFinishedNodes((prev) => { const s = new Set(prev); s.delete(nodeId); return s; });
    setFailedNodes((prev) => { const s = new Set(prev); s.delete(nodeId); return s; });
    setShowErrorLog((prev) => { const s = new Set(prev); s.delete(nodeId); return s; });
    setUpdateOutputs((prev) => ({ ...prev, [nodeId]: [] }));
    setUpdateProgress((prev) => ({ ...prev, [nodeId]: { step: "Starting update...", pct: 2 } }));

    const url = isLocal
      ? "/api/admin/update/execute"
      : `/api/cluster/nodes/${nodeId}/update`;

    const appendOutput = (nodeId: number, line: string) => {
      setUpdateOutputs((prev) => ({ ...prev, [nodeId]: [...(prev[nodeId] || []), line] }));
    };

    let hasError = false;

    try {
      const res = await fetch(url, { method: "POST", headers: authHeaders() });
      if (!res.ok) {
        appendOutput(nodeId, "[ERROR] Failed to start update");
        hasError = true;
        setUpdatingNodes((prev) => { const s = new Set(prev); s.delete(nodeId); return s; });
        setFailedNodes((prev) => new Set([...prev, nodeId]));
        setUpdateProgress((prev) => ({ ...prev, [nodeId]: { step: "Failed to start update", pct: 0 } }));
        return;
      }
      const reader = res.body!.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split("\n");
        buffer = lines.pop() || "";
        for (const line of lines) {
          if (line.startsWith("data: ")) {
            const text = line.slice(6);
            appendOutput(nodeId, text);
            if (text.includes("[ERROR]")) hasError = true;
            const prog = matchProgress(text);
            if (prog) setUpdateProgress((prev) => ({ ...prev, [nodeId]: prog }));
          } else if (line.startsWith("event: error")) {
            hasError = true;
          } else if (line.startsWith("event: done")) {
            setUpdatingNodes((prev) => { const s = new Set(prev); s.delete(nodeId); return s; });
            if (!hasError) {
              setFinishedNodes((prev) => new Set([...prev, nodeId]));
              setUpdateProgress((prev) => ({ ...prev, [nodeId]: { step: "Update complete", pct: 100 } }));
            } else {
              setFailedNodes((prev) => new Set([...prev, nodeId]));
            }
          }
        }
      }
    } catch {
      appendOutput(nodeId, "[ERROR] Connection failed");
      hasError = true;
    }
    setUpdatingNodes((prev) => { const s = new Set(prev); s.delete(nodeId); return s; });
    if (hasError) {
      setFailedNodes((prev) => new Set([...prev, nodeId]));
    } else {
      setFinishedNodes((prev) => new Set([...prev, nodeId]));
      setUpdateProgress((prev) => ({ ...prev, [nodeId]: { step: "Update complete", pct: 100 } }));
    }
    refetch();
  };

  const [cleanupInfo, setCleanupInfo] = createSignal<Record<number, { total_reclaimable: string }>>({});

  // Fetch cleanup info for all nodes on load
  const fetchCleanupInfo = async (nodeId: number, isLocal: boolean) => {
    try {
      const url = isLocal
        ? "/api/admin/docker/cleanup"
        : `/api/cluster/nodes/${nodeId}/cleanup`;
      const res = await fetch(url, { headers: authHeaders() });
      if (res.ok) {
        const data = await res.json();
        setCleanupInfo((prev) => ({ ...prev, [nodeId]: { total_reclaimable: data.total_reclaimable || "0 B" } }));
      }
    } catch { /* ignore */ }
  };

  // Fetch cleanup info when overview loads/refreshes
  createEffect(on(() => overview(), (o) => {
    if (!o) return;
    for (const node of o.nodes) {
      fetchCleanupInfo(node.id, node.is_local);
    }
  }, { defer: true }));

  const handleNodeCleanup = async (nodeId: number, isLocal: boolean) => {
    setCleaningNodes((prev) => new Set([...prev, nodeId]));
    setCleanResults((prev) => ({ ...prev, [nodeId]: [] }));
    try {
      const url = isLocal
        ? "/api/admin/docker/cleanup"
        : `/api/cluster/nodes/${nodeId}/cleanup`;
      const res = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json", ...authHeaders() } });
      const data = await res.json();
      const lines: string[] = [];
      if (data.before_reclaimable) lines.push(`Before: ${data.before_reclaimable}`);
      if (data.details?.length) lines.push(...data.details);
      if (data.after_reclaimable) lines.push(`After: ${data.after_reclaimable}`);
      setCleanResults((prev) => ({ ...prev, [nodeId]: lines.length > 0 ? lines : ["Cleanup complete"] }));
      // Refresh cleanup info
      fetchCleanupInfo(nodeId, isLocal);
    } catch {
      setCleanResults((prev) => ({ ...prev, [nodeId]: ["Failed to run cleanup"] }));
    }
    setCleaningNodes((prev) => { const s = new Set(prev); s.delete(nodeId); return s; });
  };

  const fmtBytes = (bytes: number | null) => {
    if (bytes === null || bytes === undefined) return "--";
    if (bytes > 1e9) return (bytes / 1e9).toFixed(1) + " GB";
    if (bytes > 1e6) return (bytes / 1e6).toFixed(1) + " MB";
    return (bytes / 1e3).toFixed(0) + " KB";
  };

  // Auto-refresh every 15s, but pause during updates
  setInterval(() => {
    if (updatingNodes().size === 0) refetch();
  }, 15000);

  return (
    <Layout>
      <div class="space-y-6">
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-bold text-[var(--color-text)]">Cluster Overview</h1>
            <p class="text-sm text-slate-400 mt-1">Monitoring all DNS nodes</p>
          </div>
          <span class="text-sm text-slate-500">{totalNodes()} nodes</span>
        </div>

        {/* Aggregated KPIs */}
        <div class="grid grid-cols-1 md:grid-cols-4 gap-4">
          <KPICard
            title="Nodes Online"
            value={`${onlineCount()} / ${totalNodes()}`}
            subtitle="Active nodes in cluster"
            color={onlineCount() === totalNodes() ? "#22c55e" : "#eab308"}
          />
          <KPICard
            title="Total QPS"
            value={aggregateMetric("qps") !== null ? fmt(aggregateMetric("qps")!, 1) : "--"}
            subtitle="Queries/sec across all nodes"
            color="#3b82f6"
          />
          <KPICard
            title="Avg Cache Hit"
            value={avgMetric("cache_hit_ratio") !== null ? fmt(avgMetric("cache_hit_ratio")! * 100, 1) + "%" : "--"}
            subtitle="Average cache hit ratio"
            color="#22c55e"
          />
          <KPICard
            title="Avg Latency"
            value={avgMetric("avg_latency_ms") !== null ? fmt(avgMetric("avg_latency_ms")!, 1) + "ms" : "--"}
            subtitle="Average query latency"
            color="#a855f7"
          />
        </div>

        {/* Node Grid */}
        <Show
          when={overview() && overview()!.nodes.length > 0}
          fallback={
            <div class="bg-slate-800 rounded-xl p-8 border border-slate-700 text-center">
              <p class="text-slate-400">No nodes registered yet.</p>
              <p class="text-sm text-slate-500 mt-1">Go to Settings → Cluster to add agent nodes.</p>
            </div>
          }
        >
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            <For each={overview()!.nodes}>
              {(node: any) => {
                const nodeQps = () => node.metrics ? extractValue(node.metrics.qps) : null;
                const nodeCacheHit = () => node.metrics ? extractValue(node.metrics.cache_hit_ratio) : null;
                const nodeLatency = () => node.metrics ? extractValue(node.metrics.avg_latency_ms) : null;
                const isUpdating = () => updatingNodes().has(node.id);
                const isFinished = () => finishedNodes().has(node.id);
                const isFailed = () => failedNodes().has(node.id);
                const isCleaning = () => cleaningNodes().has(node.id);
                const nodeOutput = () => updateOutputs()[node.id] || [];
                const nodeProgress = () => updateProgress()[node.id];
                const isErrorLogOpen = () => showErrorLog().has(node.id);
                const nodeCleanResult = () => cleanResults()[node.id] || [];
                const nodeReclaimable = () => cleanupInfo()[node.id]?.total_reclaimable;

                // System metrics
                const cpuPct = () => node.system_metrics ? extractValue(node.system_metrics.cpu_usage) : null;
                const memUsed = () => node.system_metrics ? extractValue(node.system_metrics.memory_used) : null;
                const memTotal = () => node.system_metrics ? extractValue(node.system_metrics.memory_total) : null;
                const memPct = () => { const u = memUsed(), t = memTotal(); return u && t ? (u / t) * 100 : null; };
                const diskPct = () => node.system_metrics ? extractValue(node.system_metrics.disk_usage_pct) : null;
                const barColor = (pct: number) => pct > 90 ? "bg-red-500" : pct > 70 ? "bg-amber-500" : "bg-emerald-500";

                return (
                  <div class={`bg-slate-800 rounded-xl p-5 border transition-colors ${
                    node.is_local ? "border-blue-500/30" : "border-slate-700 hover:border-slate-600"
                  }`}>
                    {/* Header */}
                    <div class="flex items-center justify-between mb-4">
                      <div class="flex items-center gap-3">
                        <span class={`w-2.5 h-2.5 rounded-full ${statusColor(node.status)} ${node.status === "online" ? "animate-pulse" : ""}`} />
                        <div>
                          <div class="flex items-center gap-2">
                            <h3 class="text-[var(--color-text)] font-medium text-sm">{node.name || node.domain}</h3>
                            <Show when={node.is_local}>
                              <span class="px-1.5 py-0.5 bg-blue-500/15 text-blue-400 text-[9px] font-medium rounded">LOCAL</span>
                            </Show>
                          </div>
                          <p class="text-[10px] text-slate-500">{node.domain || "localhost"}</p>
                        </div>
                      </div>
                    </div>

                    {/* Version badge + update available */}
                    <div class="mb-4 flex items-center gap-2 flex-wrap">
                      <Show when={node.version} fallback={
                        <span class="text-[10px] text-slate-600">Version unknown</span>
                      }>
                        <span class="inline-flex items-center gap-1.5 px-2 py-1 bg-slate-700/50 rounded text-[11px] font-mono text-slate-300">
                          <svg class="w-3 h-3 text-slate-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                            <path stroke-linecap="round" stroke-linejoin="round" d="M7 7h.01M7 3h5c.512 0 1.024.195 1.414.586l7 7a2 2 0 010 2.828l-7 7a2 2 0 01-2.828 0l-7-7A1.994 1.994 0 013 12V7a4 4 0 014-4z" />
                          </svg>
                          v{node.version}
                        </span>
                      </Show>
                      <Show when={node.update_info?.update_available}>
                        <span
                          class="inline-flex items-center gap-1 px-2 py-1 bg-amber-500/15 rounded text-[10px] font-medium text-amber-400 cursor-default"
                          title={node.update_info?.commit_log?.join("\n") || ""}
                        >
                          <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                            <path stroke-linecap="round" stroke-linejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                          </svg>
                          {node.update_info!.commits_behind} update{node.update_info!.commits_behind > 1 ? "s" : ""} available
                        </span>
                      </Show>
                    </div>

                    {/* Metrics */}
                    <div class="grid grid-cols-3 gap-3">
                      <div>
                        <p class="text-[10px] text-slate-500">QPS</p>
                        <p class="text-lg font-medium text-[var(--color-text)]">{nodeQps() !== null ? fmt(nodeQps()!, 1) : "--"}</p>
                      </div>
                      <div>
                        <p class="text-[10px] text-slate-500">Cache</p>
                        <p class="text-lg font-medium text-emerald-400">
                          {nodeCacheHit() !== null ? fmt(nodeCacheHit()! * 100, 1) + "%" : "--"}
                        </p>
                      </div>
                      <div>
                        <p class="text-[10px] text-slate-500">Latency</p>
                        <p class="text-lg font-medium text-purple-400">
                          {nodeLatency() !== null ? fmt(nodeLatency()!, 1) + "ms" : "--"}
                        </p>
                      </div>
                    </div>

                    {/* Resource Usage */}
                    <div class="mt-3 pt-3 border-t border-slate-700 space-y-2">
                      <p class="text-[10px] text-slate-500 font-medium">Resource Usage</p>
                      <div class="space-y-1.5">
                        <div class="flex items-center gap-2">
                          <span class="text-[9px] text-slate-500 w-8">CPU</span>
                          <div class="flex-1 h-1.5 rounded-full bg-slate-700 overflow-hidden">
                            <div class={`h-full rounded-full transition-all ${cpuPct() ? barColor(cpuPct()!) : "bg-slate-600"}`}
                              style={{ width: `${cpuPct() || 0}%` }} />
                          </div>
                          <span class="text-[9px] text-slate-400 w-10 text-right">{cpuPct() !== null ? fmt(cpuPct()!, 0) + "%" : "--"}</span>
                        </div>
                        <div class="flex items-center gap-2">
                          <span class="text-[9px] text-slate-500 w-8">MEM</span>
                          <div class="flex-1 h-1.5 rounded-full bg-slate-700 overflow-hidden">
                            <div class={`h-full rounded-full transition-all ${memPct() ? barColor(memPct()!) : "bg-slate-600"}`}
                              style={{ width: `${memPct() || 0}%` }} />
                          </div>
                          <span class="text-[9px] text-slate-400 w-10 text-right">{memPct() !== null ? fmt(memPct()!, 0) + "%" : "--"}</span>
                        </div>
                        <div class="flex items-center gap-2">
                          <span class="text-[9px] text-slate-500 w-8">DISK</span>
                          <div class="flex-1 h-1.5 rounded-full bg-slate-700 overflow-hidden">
                            <div class={`h-full rounded-full transition-all ${diskPct() ? barColor(diskPct()!) : "bg-slate-600"}`}
                              style={{ width: `${diskPct() || 0}%` }} />
                          </div>
                          <span class="text-[9px] text-slate-400 w-10 text-right">{diskPct() !== null ? fmt(diskPct()!, 0) + "%" : "--"}</span>
                        </div>
                      </div>
                    </div>

                    {/* Footer: status + actions */}
                    <div class="mt-3 pt-3 border-t border-slate-700">
                      <div class="flex items-center justify-between mb-2">
                        <div class="flex items-center gap-2">
                          <span class={`text-xs capitalize ${
                            node.status === "online" ? "text-emerald-400" :
                            node.status === "degraded" ? "text-amber-400" :
                            node.status === "offline" ? "text-red-400" : "text-slate-400"
                          }`}>{node.status}</span>
                          <span class="text-[10px] text-slate-600">{timeSince(node.last_seen_at)}</span>
                        </div>
                      </div>
                      <div class="flex gap-1.5">
                        <button
                          onClick={() => handleNodeUpdate(node.id, node.is_local)}
                          disabled={isUpdating() || !node.update_info?.update_available}
                          class="px-2 py-1 text-[10px] bg-amber-500/10 text-amber-400 rounded hover:bg-amber-500/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                          {isUpdating() ? "Updating..." : "Update"}
                        </button>
                        <button
                          onClick={() => handleNodeUpdate(node.id, node.is_local)}
                          disabled={isUpdating()}
                          class="px-2 py-1 text-[10px] bg-slate-700 text-slate-400 rounded hover:bg-slate-600 transition-colors disabled:opacity-50"
                        >
                          {isUpdating() ? "..." : "Rebuild"}
                        </button>
                        <button
                          onClick={() => handleNodeCleanup(node.id, node.is_local)}
                          disabled={isCleaning()}
                          class="px-2 py-1 text-[10px] bg-red-500/10 text-red-400 rounded hover:bg-red-500/20 transition-colors disabled:opacity-50"
                        >
                          {isCleaning() ? "Cleaning..." : <>Cleanup{nodeReclaimable() && nodeReclaimable() !== "0 B" ? <span class="ml-1 text-red-400/60">({nodeReclaimable()})</span> : ""}</>}
                        </button>
                      </div>
                    </div>

                    {/* Cleanup result */}
                    <Show when={nodeCleanResult().length > 0}>
                      <div class="mt-2 p-2.5 bg-slate-900 rounded-lg border border-slate-700/50 space-y-1.5">
                        <For each={nodeCleanResult()}>
                          {(line) => (
                            <p class={`text-[10px] font-mono ${
                              line.startsWith("Before:") ? "text-slate-400" :
                              line.startsWith("After:") ? "text-emerald-400 font-medium" :
                              line.includes("Failed") ? "text-red-400" :
                              "text-slate-300"
                            }`}>{line}</p>
                          )}
                        </For>
                      </div>
                    </Show>

                    <Show when={node.last_error && !isUpdating()}>
                      <div class="mt-2 p-2 bg-red-500/10 rounded text-[10px] text-red-400 truncate" title={node.last_error}>
                        {node.last_error}
                      </div>
                    </Show>

                    {/* Update progress */}
                    <Show when={isUpdating() && nodeProgress()}>
                      <div class="mt-2 p-2.5 bg-slate-900 rounded-lg border border-slate-700/50 space-y-2">
                        <div class="flex items-center justify-between">
                          <span class="text-[10px] text-blue-400 font-medium">{nodeProgress()!.step}</span>
                          <span class="text-[10px] text-slate-500">{nodeProgress()!.pct}%</span>
                        </div>
                        <div class="h-1.5 rounded-full bg-slate-700 overflow-hidden">
                          <div
                            class="h-full rounded-full bg-blue-500 transition-all duration-500 ease-out"
                            style={{ width: `${nodeProgress()!.pct}%` }}
                          />
                        </div>
                      </div>
                    </Show>

                    {/* Update success */}
                    <Show when={!isUpdating() && isFinished()}>
                      <div class="mt-2 p-2 bg-emerald-500/10 rounded-lg border border-emerald-500/20 flex items-center gap-2">
                        <svg class="w-3.5 h-3.5 text-emerald-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
                        </svg>
                        <span class="text-[11px] text-emerald-400 font-medium">Update complete</span>
                      </div>
                    </Show>

                    {/* Update failed */}
                    <Show when={!isUpdating() && isFailed()}>
                      <div class="mt-2 space-y-1.5">
                        <button
                          class="w-full p-2 bg-red-500/10 rounded-lg border border-red-500/20 flex items-center justify-between cursor-pointer hover:bg-red-500/15 transition-colors"
                          onClick={() => setShowErrorLog((prev) => {
                            const s = new Set(prev);
                            s.has(node.id) ? s.delete(node.id) : s.add(node.id);
                            return s;
                          })}
                        >
                          <div class="flex items-center gap-2">
                            <svg class="w-3.5 h-3.5 text-red-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
                              <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
                            </svg>
                            <span class="text-[11px] text-red-400 font-medium">Update failed</span>
                          </div>
                          <svg class={`w-3 h-3 text-red-400/60 transition-transform ${isErrorLogOpen() ? "rotate-180" : ""}`} fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                            <path stroke-linecap="round" stroke-linejoin="round" d="M19 9l-7 7-7-7" />
                          </svg>
                        </button>
                        <Show when={isErrorLogOpen()}>
                          <div class="bg-slate-950 rounded-lg p-2.5 font-mono text-[10px] leading-4 max-h-40 overflow-y-auto border border-slate-700/50">
                            <For each={nodeOutput()}>
                              {(line) => (
                                <div class={
                                  line.includes("[OK]") ? "text-emerald-400" :
                                  line.includes("[WARN]") ? "text-amber-400" :
                                  line.includes("[ERROR]") ? "text-red-400" :
                                  line.includes("[INFO]") ? "text-blue-400" :
                                  "text-slate-500"
                                }>{line}</div>
                              )}
                            </For>
                          </div>
                        </Show>
                      </div>
                    </Show>
                  </div>
                );
              }}
            </For>
          </div>
        </Show>
      </div>
    </Layout>
  );
}
