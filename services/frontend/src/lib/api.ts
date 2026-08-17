import { authHeaders, logout } from "./auth";

const API_BASE = import.meta.env.VITE_API_URL || "";

async function fetchAPI<T>(path: string, params?: Record<string, string>): Promise<T> {
  const url = new URL(`${API_BASE}${path}`, window.location.origin);
  if (params) {
    Object.entries(params).forEach(([k, v]) => url.searchParams.set(k, v));
  }

  const res = await fetch(url.toString(), {
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(),
    },
  });

  if (res.status === 401) {
    logout();
    throw new Error("Session expired");
  }

  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`);
  }

  return res.json();
}

// Metrics API
export const metricsAPI = {
  overview: () => fetchAPI<any>("/api/metrics/overview"),
  qps: (params?: Record<string, string>) => fetchAPI<any>("/api/metrics/qps", params),
  latency: (params?: Record<string, string>) => fetchAPI<any>("/api/metrics/latency", params),
  cache: (params?: Record<string, string>) => fetchAPI<any>("/api/metrics/cache", params),
  dnssec: (params?: Record<string, string>) => fetchAPI<any>("/api/metrics/dnssec", params),
  system: (params?: Record<string, string>) => fetchAPI<any>("/api/metrics/system", params),
  upstreams: () => fetchAPI<any>("/api/metrics/upstreams"),
};

// Query Log API
export const queriesAPI = {
  search: (params?: Record<string, string>) =>
    fetchAPI<{ data: QueryLogEntry[]; limit: number; offset: number }>("/api/queries", params),
  topDomains: (params?: Record<string, string>) =>
    fetchAPI<TopDomain[]>("/api/queries/top-domains", params),
  typeDistribution: (params?: Record<string, string>) =>
    fetchAPI<Distribution[]>("/api/queries/type-distribution", params),
  rcodeDistribution: (params?: Record<string, string>) =>
    fetchAPI<Distribution[]>("/api/queries/rcode-distribution", params),
  protocolDistribution: (params?: Record<string, string>) =>
    fetchAPI<Distribution[]>("/api/queries/protocol-distribution", params),
  timeline: (params?: Record<string, string>) =>
    fetchAPI<TimelinePoint[]>("/api/queries/timeline", params),
};

// Alerts API
export const alertsAPI = {
  list: () => fetchAPI<AlertRule[]>("/api/alerts"),
  create: (rule: Partial<AlertRule>) =>
    fetch(`/api/alerts`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(rule),
    }).then((r) => r.json()),
  update: (id: number, rule: Partial<AlertRule>) =>
    fetch(`/api/alerts/${id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(rule),
    }).then((r) => r.json()),
  delete: (id: number) =>
    fetch(`/api/alerts/${id}`, { method: "DELETE", headers: authHeaders() }).then((r) => r.json()),
  history: (params?: Record<string, string>) =>
    fetchAPI<AlertEvent[]>("/api/alerts/history", params),
};

// Health API
export const healthAPI = {
  check: () => fetchAPI<HealthStatus>("/api/health"),
};

// Version API
export const versionAPI = {
  get: () => fetchAPI<{ version: string }>("/api/version"),
};

// Docker Cleanup API
export const dockerCleanupAPI = {
  getInfo: () => fetchAPI<any>("/api/admin/docker/cleanup"),
  run: () =>
    fetch("/api/admin/docker/cleanup", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
};

// Server Config API
export const serverConfigAPI = {
  get: () => fetchAPI<ServerConfig>("/api/admin/server/config"),
  update: (data: Partial<ServerConfig>) =>
    fetch("/api/admin/server/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(data),
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
};

export interface ServerConfig {
  timezone: string;
  allowed_subnets: string[];
  retention_query_days: number;
  retention_metrics_days: number;
}

// Admin Update API
export const updateAPI = {
  check: () => fetchAPI<UpdateCheckResult>("/api/admin/update/check"),
  status: () => fetchAPI<{ in_progress: boolean }>("/api/admin/update/status"),
  getAutoConfig: () => fetchAPI<AutoUpdateConfig>("/api/admin/update/auto"),
  updateAutoConfig: (data: Partial<AutoUpdateConfig>) =>
    fetch("/api/admin/update/auto", {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(data),
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
};

export interface AutoUpdateConfig {
  auto_update_enabled: boolean;
  auto_update_hour: number;
  auto_update_day: number;
  last_auto_update: string | null;
}

// DNS Filtering API
export const filterAPI = {
  list: () => fetchAPI<{ rules: FilterRule[]; lists: any[]; total_rules: number; total_enabled: number }>("/api/admin/filters"),
  add: (rule: { domain: string; category?: string; action?: string }) =>
    fetch("/api/admin/filters", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(rule),
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
  delete: (id: number) =>
    fetch(`/api/admin/filters/${id}`, { method: "DELETE", headers: authHeaders() }).then((r) => r.json()),
  toggle: (id: number) =>
    fetch(`/api/admin/filters/${id}/toggle`, { method: "PUT", headers: authHeaders() }).then((r) => r.json()),
  import: (data: { url?: string; domains?: string; category?: string }) =>
    fetch("/api/admin/filters/import", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(data),
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
  stats: () => fetchAPI<any>("/api/admin/filters/stats"),
  apply: () =>
    fetch("/api/admin/filters/apply", {
      method: "POST",
      headers: authHeaders(),
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
};

export interface FilterRule {
  id: number;
  domain: string;
  action: string;
  category: string;
  enabled: boolean;
  created_at: string;
}

// RPZ Komdigi API
export const rpzAPI = {
  getConfig: () => fetchAPI<RPZConfig>("/api/admin/rpz/config"),
  updateConfig: (data: Partial<{ enabled: boolean; master_servers: string; zone_name: string; auto_sync_enabled: boolean; auto_sync_interval_hours: number; auto_sync_hour: number }>) =>
    fetch("/api/admin/rpz/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(data),
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
  getStats: () => fetchAPI<RPZStats>("/api/admin/rpz/stats"),
};

export interface RPZStats {
  config: RPZConfig;
  file_exists: boolean;
  kresd_memory_mb: number;
}

export interface RPZConfig {
  enabled: boolean;
  master_servers: string;
  zone_name: string;
  last_sync: string | null;
  last_sync_status: string;
  last_sync_error: string;
  domain_count: number;
  file_size_bytes: number;
  sync_duration_ms: number;
  auto_sync_enabled: boolean;
  auto_sync_interval_hours: number;
  auto_sync_hour: number;
  zone_serial: number;
  // Proof that the loaded zone is actually filtering — "sync success" only
  // means the file landed on disk, not that kresd is applying it.
  last_verify_at: string | null;
  last_verify_status: string; // "" | ok | loading | failed
  last_verify_detail: string;
}

// Block Page Config API
export const blockpageAPI = {
  getConfig: () => fetchAPI<BlockPageConfig>("/api/admin/blockpage/config"),
  updateConfig: (config: BlockPageConfig) =>
    fetch("/api/admin/blockpage/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(config),
    }).then(async (r) => { const d = await r.json(); if (!r.ok) throw new Error(d.error); return d; }),
};

export interface BlockPageConfig {
  title: string;
  subtitle: string;
  message: string;
  contact: string;
  bg_color: string;
  accent_color: string;
  show_domain: boolean;
  show_logo: boolean;
  footer_text: string;
}

// Admin Services API
export const servicesAPI = {
  list: () => fetchAPI<ServiceInfo[]>("/api/admin/services"),
  restart: (service: string) =>
    fetch("/api/admin/services/restart", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ service }),
    }).then(async (r) => {
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || "Failed to restart");
      return data;
    }),
};

export interface ServiceInfo {
  name: string;
  status: string;
  health: string;
}

export interface UpdateCheckResult {
  current_version: string;
  current_commit: string;
  latest_commit: string;
  update_available: boolean;
  commits_behind: number;
  commit_log: string[];
}

// Resolver info API
export const resolverAPI = {
  info: () => fetchAPI<ResolverInfo>("/api/resolver/info"),
};

export interface CacheUsage {
  map_size_bytes: number;
  used_bytes: number;
  free_bytes: number;
  live_bytes: number;
  percent_used: number;
  entries: number;
  available: boolean;
  high_water_only: boolean;
  error?: string;
}

export interface ResolverInfo {
  config: any;
  cache: { storage: string; "size-max": string };
  cache_usage?: CacheUsage;
  network: any;
  options: any;
  monitoring: any;
  workers: any;
  server: { hostname: string; cpus: number };
}

// Cluster API
export const clusterAPI = {
  getConfig: () => fetchAPI<ClusterConfig>("/api/cluster/config"),
  updateConfig: (config: Partial<ClusterConfig>) =>
    fetch("/api/cluster/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(config),
    }).then(async (r) => {
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || "Failed to update config");
      return data;
    }),
  listNodes: () => fetchAPI<ClusterNode[]>("/api/cluster/nodes"),
  addNode: (node: { name: string; domain: string }) =>
    fetch("/api/cluster/nodes", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(node),
    }).then(async (r) => {
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || "Failed to add node");
      return data as { id: number; name: string; domain: string; api_token: string };
    }),
  updateNode: (id: number, node: Partial<{ name: string; domain: string }>) =>
    fetch(`/api/cluster/nodes/${id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(node),
    }).then((r) => r.json()),
  deleteNode: (id: number) =>
    fetch(`/api/cluster/nodes/${id}`, { method: "DELETE", headers: authHeaders() }).then((r) => r.json()),
  getNodeMetrics: (id: number) => fetchAPI<any>(`/api/cluster/nodes/${id}/metrics`),
  getOverview: () => fetchAPI<ClusterOverview>("/api/cluster/overview"),
};

export interface ClusterConfig {
  node_role: string;
  node_name: string;
  node_domain: string;
  controller_domain: string;
  controller_token: string;
}

export interface ClusterNode {
  id: number;
  name: string;
  domain: string;
  api_token?: string;
  status: string;
  version: string;
  last_seen_at: string | null;
  last_error: string;
  created_at: string;
}

export interface ClusterOverview {
  nodes: ClusterNodeOverview[];
  node_count: number;
}

export interface ClusterNodeUpdateInfo {
  update_available: boolean;
  commits_behind: number;
  commit_log?: string[];
}

export interface ClusterNodeOverview {
  id: number;
  name: string;
  domain: string;
  status: string;
  version: string;
  last_seen_at: string | null;
  last_error: string;
  metrics: any;
  update_info?: ClusterNodeUpdateInfo;
}

// Auth API (public, no auth header needed)
export const authAPI = {
  login: (username: string, password: string) =>
    fetch("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
    }).then(async (r) => {
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || "Login failed");
      return data;
    }),
  register: (username: string, password: string) =>
    fetch("/api/auth/register", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ username, password }),
    }).then(async (r) => {
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || "Registration failed");
      return data;
    }),
  check: () =>
    fetch("/api/auth/check").then((r) => r.json()) as Promise<{ has_users: boolean; setup_needed: boolean }>,
};

// Types
export interface QueryLogEntry {
  timestamp: string;
  client_ip: string;
  qname: string;
  qtype: number;
  rcode: number;
  latency_us: number;
  protocol: string;
  dnssec_status: string;
  upstream_ip: string;
  cached: boolean;
  response_size: number;
}

export interface TopDomain {
  qname: string;
  query_count: number;
}

export interface Distribution {
  label: string;
  count: number;
}

export interface TimelinePoint {
  timestamp: string;
  count: number;
  latency: number;
}

export interface AlertRule {
  id: number;
  name: string;
  metric: string;
  condition: string;
  threshold: number;
  duration_sec: number;
  enabled: boolean;
  notify_channels: string[];
  created_at: string;
  updated_at: string;
}

export interface AlertEvent {
  id: number;
  rule_id: number;
  rule_name: string;
  status: string;
  value: number;
  message: string;
  fired_at: string;
  resolved_at?: string;
}

export interface HealthStatus {
  status: string;
  checks: Record<string, string>;
}
