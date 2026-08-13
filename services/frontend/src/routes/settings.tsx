import { createSignal, onMount, Show, For, createMemo } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import Layout from "~/components/Layout";
import { authHeaders, getToken, logout } from "~/lib/auth";
import { updateAPI, clusterAPI, servicesAPI, serverConfigAPI, type UpdateCheckResult, type ClusterConfig, type ClusterNode, type ServiceInfo, type ServerConfig, type AutoUpdateConfig } from "~/lib/api";
import { t } from "~/lib/i18n";

type Tab = "account" | "users" | "server" | "update" | "cluster";

export default function SettingsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = createMemo<Tab>(() => (searchParams.tab as Tab) || "account");
  const setActiveTab = (tab: Tab) => setSearchParams({ tab });
  const [username, setUsername] = createSignal("");
  const [role, setRole] = createSignal("");

  // Change password
  const [oldPass, setOldPass] = createSignal("");
  const [newPass, setNewPass] = createSignal("");
  const [confirmPass, setConfirmPass] = createSignal("");
  const [passMsg, setPassMsg] = createSignal("");
  const [passError, setPassError] = createSignal(false);
  const [passLoading, setPassLoading] = createSignal(false);

  // Add user
  const [newUser, setNewUser] = createSignal("");
  const [newUserPass, setNewUserPass] = createSignal("");
  const [newUserRole, setNewUserRole] = createSignal("viewer");
  const [userMsg, setUserMsg] = createSignal("");
  const [userError, setUserError] = createSignal(false);

  // System update
  const [updateInfo, setUpdateInfo] = createSignal<UpdateCheckResult | null>(null);
  const [updateChecking, setUpdateChecking] = createSignal(false);
  const [updateRunning, setUpdateRunning] = createSignal(false);
  const [updateOutput, setUpdateOutput] = createSignal<string[]>([]);
  const [updateDone, setUpdateDone] = createSignal(false);
  const [updateFailed, setUpdateFailed] = createSignal(false);
  const [updateError, setUpdateError] = createSignal("");
  const [restarting, setRestarting] = createSignal(false);
  const [updateProgress, setUpdateProgress] = createSignal<{ step: string; pct: number }>({ step: "", pct: 0 });
  const [showUpdateLog, setShowUpdateLog] = createSignal(false);

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

  // Auto-update
  const [autoUpdateCfg, setAutoUpdateCfg] = createSignal<AutoUpdateConfig | null>(null);

  // Services
  const [services, setServices] = createSignal<ServiceInfo[]>([]);
  const [restartingService, setRestartingService] = createSignal("");
  const [restartAllRunning, setRestartAllRunning] = createSignal(false);
  const [restartOutput, setRestartOutput] = createSignal<string[]>([]);

  // Server config
  const [srvConfig, setSrvConfig] = createSignal<ServerConfig | null>(null);
  const [srvMsg, setSrvMsg] = createSignal("");
  const [srvError, setSrvError] = createSignal(false);
  const [newSubnet, setNewSubnet] = createSignal("");

  // Cluster
  const [clusterConfig, setClusterConfig] = createSignal<ClusterConfig | null>(null);
  const [clusterNodes, setClusterNodes] = createSignal<ClusterNode[]>([]);
  const [clusterMsg, setClusterMsg] = createSignal("");
  const [clusterError, setClusterError] = createSignal(false);
  const [newNodeName, setNewNodeName] = createSignal("");
  const [newNodeDomain, setNewNodeDomain] = createSignal("");
  const [generatedToken, setGeneratedToken] = createSignal("");
  const [agentControllerDomain, setAgentControllerDomain] = createSignal("");
  const [agentToken, setAgentToken] = createSignal("");

  onMount(async () => {
    try {
      const res = await fetch("/api/auth/me", { headers: authHeaders() });
      if (res.ok) {
        const data = await res.json();
        setUsername(data.username);
        setRole(data.role);
      }
    } catch {}
    loadClusterConfig();
    loadServices();
    loadServerConfig();
    loadAutoUpdateConfig();
  });

  const loadServices = async () => {
    try { setServices(await servicesAPI.list()); } catch {}
  };

  const loadServerConfig = async () => {
    try { setSrvConfig(await serverConfigAPI.get()); } catch {}
  };

  const loadAutoUpdateConfig = async () => {
    try { setAutoUpdateCfg(await updateAPI.getAutoConfig()); } catch {}
  };

  const showSrvMsg = (text: string, error = false) => {
    setSrvMsg(text); setSrvError(error);
    setTimeout(() => setSrvMsg(""), 4000);
  };

  const handleRestartService = async (name: string) => {
    setRestartingService(name);
    try {
      await servicesAPI.restart(name);
      if (name === "backend") {
        setTimeout(async () => {
          for (let i = 0; i < 20; i++) {
            await new Promise((r) => setTimeout(r, 2000));
            try {
              const res = await fetch("/api/health", { signal: AbortSignal.timeout(2000) });
              if (res.ok) { setRestartingService(""); loadServices(); return; }
            } catch {}
          }
          setRestartingService("");
        }, 1000);
      } else {
        setTimeout(() => { setRestartingService(""); loadServices(); }, 2000);
      }
    } catch { setRestartingService(""); }
  };

  const handleRestartAll = async () => {
    setRestartAllRunning(true);
    setRestartOutput([]);
    try {
      const res = await fetch("/api/admin/services/restart-all", { method: "POST", headers: authHeaders() });
      if (!res.ok) { setRestartAllRunning(false); return; }
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
          if (line.startsWith("data: ")) setRestartOutput((prev) => [...prev, line.slice(6)]);
          else if (line.startsWith("event: done")) { setRestartAllRunning(false); }
        }
      }
    } catch {}
    for (let i = 0; i < 20; i++) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        const res = await fetch("/api/health", { signal: AbortSignal.timeout(2000) });
        if (res.ok) { setRestartAllRunning(false); loadServices(); return; }
      } catch {}
    }
    setRestartAllRunning(false);
  };

  const loadClusterConfig = async () => {
    try {
      const cfg = await clusterAPI.getConfig();
      setClusterConfig(cfg);
      setAgentControllerDomain(cfg.controller_domain || "");
      setAgentToken(cfg.controller_token || "");
      if (cfg.node_role === "controller") {
        const nodes = await clusterAPI.listNodes();
        setClusterNodes(nodes);
      }
    } catch {}
  };

  const handleRoleChange = async (newRole: string) => {
    setClusterMsg(""); setClusterError(false);
    try {
      await clusterAPI.updateConfig({ node_role: newRole });
      setClusterConfig((prev) => prev ? { ...prev, node_role: newRole } : null);
      setClusterMsg(`Role changed to ${newRole}`);
      if (newRole === "controller") {
        const nodes = await clusterAPI.listNodes();
        setClusterNodes(nodes);
      }
      setTimeout(() => window.location.reload(), 1000);
    } catch (err: any) { setClusterMsg(err.message); setClusterError(true); }
  };

  const handleAddNode = async (e: Event) => {
    e.preventDefault();
    setClusterMsg(""); setClusterError(false); setGeneratedToken("");
    try {
      const result = await clusterAPI.addNode({ name: newNodeName(), domain: newNodeDomain() });
      setGeneratedToken(result.api_token);
      setClusterMsg(`Node "${result.name}" added. Copy the token below.`);
      setNewNodeName(""); setNewNodeDomain("");
      const nodes = await clusterAPI.listNodes();
      setClusterNodes(nodes);
    } catch (err: any) { setClusterMsg(err.message); setClusterError(true); }
  };

  const handleDeleteNodeAction = async (id: number) => {
    try {
      await clusterAPI.deleteNode(id);
      setClusterNodes((prev) => prev.filter((n) => n.id !== id));
    } catch {}
  };

  const handleSaveAgentConfig = async () => {
    setClusterMsg(""); setClusterError(false);
    try {
      await clusterAPI.updateConfig({
        controller_domain: agentControllerDomain(),
        controller_token: agentToken(),
      });
      setClusterMsg("Agent configuration saved");
    } catch (err: any) { setClusterMsg(err.message); setClusterError(true); }
  };

  const handleRemoteNodeUpdate = async (id: number) => {
    setActiveTab("update");
    setUpdateRunning(true); setUpdateOutput([]); setUpdateDone(false); setUpdateFailed(false);
    setUpdateError(""); setShowUpdateLog(false);
    setUpdateProgress({ step: "Starting update...", pct: 2 });
    let hasError = false;
    try {
      const res = await fetch(`/api/cluster/nodes/${id}/update`, { method: "POST", headers: authHeaders() });
      if (!res.ok) { const data = await res.json(); setUpdateError(data.error || "Failed"); setUpdateRunning(false); setUpdateFailed(true); return; }
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
            setUpdateOutput((prev) => [...prev, text]);
            if (text.includes("[ERROR]")) hasError = true;
            const prog = matchProgress(text);
            if (prog) setUpdateProgress(prog);
          }
          else if (line.startsWith("event: error")) { hasError = true; }
          else if (line.startsWith("event: done")) {
            if (!hasError) { setUpdateDone(true); setUpdateProgress({ step: "Update complete", pct: 100 }); }
            else { setUpdateFailed(true); }
            setUpdateRunning(false);
          }
        }
      }
      if (!updateDone() && !hasError) { setUpdateDone(true); setUpdateRunning(false); setUpdateProgress({ step: "Update complete", pct: 100 }); }
      else if (hasError) { setUpdateFailed(true); setUpdateRunning(false); }
    } catch { setUpdateDone(true); setUpdateRunning(false); }
  };

  const handleChangePassword = async (e: Event) => {
    e.preventDefault();
    setPassMsg(""); setPassError(false);
    if (newPass() !== confirmPass()) { setPassMsg("Passwords do not match"); setPassError(true); return; }
    if (newPass().length < 8) { setPassMsg("Password must be at least 8 characters"); setPassError(true); return; }
    setPassLoading(true);
    try {
      const res = await fetch("/api/auth/change-password", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ old_password: oldPass(), new_password: newPass() }),
      });
      const data = await res.json();
      if (!res.ok) { setPassMsg(data.error || "Failed to change password"); setPassError(true); }
      else { setPassMsg("Password changed! Logging out..."); setPassError(false); setOldPass(""); setNewPass(""); setConfirmPass(""); setTimeout(() => logout(), 2000); }
    } catch (err: any) { setPassMsg(err.message); setPassError(true); }
    finally { setPassLoading(false); }
  };

  const handleAddUser = async (e: Event) => {
    e.preventDefault();
    setUserMsg(""); setUserError(false);
    if (newUserPass().length < 8) { setUserMsg("Password must be at least 8 characters"); setUserError(true); return; }
    try {
      const res = await fetch("/api/auth/register", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ username: newUser(), password: newUserPass(), role: newUserRole() }),
      });
      const data = await res.json();
      if (!res.ok) { setUserMsg(data.error || "Failed to create user"); setUserError(true); }
      else { setUserMsg(`User "${data.username}" (${data.role}) created`); setUserError(false); setNewUser(""); setNewUserPass(""); }
    } catch (err: any) { setUserMsg(err.message); setUserError(true); }
  };

  const checkForUpdates = async () => {
    setUpdateChecking(true); setUpdateError("");
    try { setUpdateInfo(await updateAPI.check()); }
    catch (err: any) { setUpdateError(err.message || "Failed to check"); }
    finally { setUpdateChecking(false); }
  };

  const pollHealth = () => {
    setRestarting(true);
    const interval = setInterval(async () => {
      try {
        const res = await fetch("/api/health", { signal: AbortSignal.timeout(3000) });
        if (res.ok) { clearInterval(interval); setRestarting(false); setUpdateDone(true); setUpdateRunning(false); checkForUpdates(); }
      } catch {}
    }, 3000);
  };

  const executeUpdate = async () => {
    setUpdateRunning(true); setUpdateOutput([]); setUpdateDone(false); setUpdateFailed(false);
    setUpdateError(""); setShowUpdateLog(false);
    setUpdateProgress({ step: "Starting update...", pct: 2 });
    let hasError = false;
    try {
      const res = await fetch("/api/admin/update/execute", { method: "POST", headers: authHeaders() });
      if (!res.ok) { const data = await res.json(); setUpdateError(data.error || "Failed"); setUpdateRunning(false); setUpdateFailed(true); return; }
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
            setUpdateOutput((prev) => [...prev, text]);
            if (text.includes("[ERROR]")) hasError = true;
            const prog = matchProgress(text);
            if (prog) setUpdateProgress(prog);
          }
          else if (line.startsWith("event: error")) { hasError = true; }
          else if (line.startsWith("event: done")) {
            if (!hasError) { setUpdateDone(true); setUpdateProgress({ step: "Update complete", pct: 100 }); }
            else { setUpdateFailed(true); }
            setUpdateRunning(false);
          }
        }
      }
      if (!updateDone() && !hasError) pollHealth();
      else if (hasError) { setUpdateFailed(true); setUpdateRunning(false); }
    } catch {
      if (!updateDone()) pollHealth();
    }
  };

  const inputClass = "w-full bg-slate-900/50 border border-slate-700 rounded-lg px-3 py-2 text-sm text-[var(--color-text)] placeholder-slate-600 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500/30 transition";
  const labelClass = "block text-xs font-medium text-slate-500 mb-1";

  const Alert = (props: { msg: string; error: boolean }) => (
    <div class={`p-2.5 rounded-lg text-xs ${
      props.error ? "bg-red-500/10 text-red-400 border border-red-500/20" : "bg-emerald-500/10 text-emerald-400 border border-emerald-500/20"
    }`}>{props.msg}</div>
  );

  const SectionCard = (props: { title: string; description?: string; children: any; class?: string }) => (
    <div class={`bg-slate-800 rounded-xl border border-slate-700 ${props.class || ""}`}>
      <div class="px-5 py-4 border-b border-slate-700/50">
        <h3 class="text-sm font-semibold text-[var(--color-text)]">{props.title}</h3>
        <Show when={props.description}>
          <p class="text-[11px] text-slate-500 mt-0.5">{props.description}</p>
        </Show>
      </div>
      <div class="p-5">{props.children}</div>
    </div>
  );

  return (
    <Layout>
      <div class="max-w-3xl space-y-4">

          {/* ===================== ACCOUNT ===================== */}
          <Show when={activeTab() === "account"}>
            <SectionCard title={t("settings.change_password")} description={t("settings.change_password_desc")}>
              {passMsg() && <div class="mb-4"><Alert msg={passMsg()} error={passError()} /></div>}
              <form onSubmit={handleChangePassword} class="space-y-4">
                <div>
                  <label class={labelClass}>{t("settings.current_password")}</label>
                  <input type="password" required class={inputClass} value={oldPass()} onInput={(e) => setOldPass(e.target.value)} />
                </div>
                <div class="grid grid-cols-2 gap-3">
                  <div>
                    <label class={labelClass}>{t("settings.new_password")}</label>
                    <input type="password" required class={inputClass} placeholder={t("settings.min_8_chars")} value={newPass()} onInput={(e) => setNewPass(e.target.value)} />
                  </div>
                  <div>
                    <label class={labelClass}>{t("settings.confirm_password")}</label>
                    <input type="password" required class={inputClass} value={confirmPass()} onInput={(e) => setConfirmPass(e.target.value)} />
                  </div>
                </div>
                <button type="submit" disabled={passLoading()} class="px-4 py-2 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors">
                  {passLoading() ? t("settings.saving") : t("settings.change_password")}
                </button>
              </form>
            </SectionCard>

            <SectionCard title={t("settings.session")}>
              <div class="flex items-center justify-between">
                <div>
                  <p class="text-sm text-slate-300">{t("settings.sign_out")}</p>
                  <p class="text-[10px] text-slate-500">{t("settings.session_end")}</p>
                </div>
                <button onClick={() => logout()} class="px-4 py-2 bg-red-500/10 hover:bg-red-500/20 text-red-400 text-xs font-medium rounded-lg border border-red-500/20 transition-colors">
                  {t("settings.logout")}
                </button>
              </div>
            </SectionCard>
          </Show>

          {/* ===================== USERS ===================== */}
          <Show when={activeTab() === "users"}>
            <SectionCard title={t("settings.add_user")} description={t("settings.add_user_desc")}>
              {userMsg() && <div class="mb-4"><Alert msg={userMsg()} error={userError()} /></div>}
              <form onSubmit={handleAddUser} class="space-y-4">
                <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <div>
                    <label class={labelClass}>Username</label>
                    <input type="text" required class={inputClass} value={newUser()} onInput={(e) => setNewUser(e.target.value)} />
                  </div>
                  <div>
                    <label class={labelClass}>Password</label>
                    <input type="password" required class={inputClass} placeholder="Min. 8 characters" value={newUserPass()} onInput={(e) => setNewUserPass(e.target.value)} />
                  </div>
                  <div>
                    <label class={labelClass}>Role</label>
                    <select class={inputClass} value={newUserRole()} onChange={(e) => setNewUserRole(e.target.value)}>
                      <option value="viewer">Viewer</option>
                      <option value="admin">Admin</option>
                    </select>
                  </div>
                </div>
                <button type="submit" class="px-4 py-2 bg-emerald-600 hover:bg-emerald-700 text-white text-xs font-medium rounded-lg transition-colors">
                  {t("settings.create_user")}
                </button>
              </form>
            </SectionCard>
          </Show>

          {/* ===================== SERVER ===================== */}
          <Show when={activeTab() === "server"}>
            {srvMsg() && <Alert msg={srvMsg()} error={srvError()} />}

            <SectionCard title={t("settings.timezone")} description={t("settings.timezone_desc")}>
              <select
                value={srvConfig()?.timezone || "Asia/Jakarta"}
                onChange={async (e) => {
                  const tz = e.currentTarget.value;
                  try {
                    await serverConfigAPI.update({ timezone: tz });
                    setSrvConfig((prev) => prev ? { ...prev, timezone: tz } : prev);
                    showSrvMsg(`Timezone set to ${tz}`);
                  } catch (err: any) { showSrvMsg(err.message, true); }
                }}
                class="bg-slate-900 text-[var(--color-text)] text-xs rounded-lg px-3 py-2 border border-slate-600 w-full max-w-sm"
              >
                <option value="Asia/Jakarta">Asia/Jakarta (WIB, UTC+7)</option>
                <option value="Asia/Makassar">Asia/Makassar (WITA, UTC+8)</option>
                <option value="Asia/Jayapura">Asia/Jayapura (WIT, UTC+9)</option>
                <option value="Asia/Singapore">Asia/Singapore (SGT, UTC+8)</option>
                <option value="Asia/Tokyo">Asia/Tokyo (JST, UTC+9)</option>
                <option value="Asia/Kolkata">Asia/Kolkata (IST, UTC+5:30)</option>
                <option value="Asia/Shanghai">Asia/Shanghai (CST, UTC+8)</option>
                <option value="Asia/Dubai">Asia/Dubai (GST, UTC+4)</option>
                <option value="Europe/London">Europe/London (GMT/BST)</option>
                <option value="Europe/Berlin">Europe/Berlin (CET/CEST)</option>
                <option value="America/New_York">America/New_York (EST/EDT)</option>
                <option value="America/Chicago">America/Chicago (CST/CDT)</option>
                <option value="America/Denver">America/Denver (MST/MDT)</option>
                <option value="America/Los_Angeles">America/Los_Angeles (PST/PDT)</option>
                <option value="Pacific/Auckland">Pacific/Auckland (NZST, UTC+12)</option>
                <option value="Australia/Sydney">Australia/Sydney (AEST, UTC+10)</option>
                <option value="UTC">UTC</option>
              </select>
            </SectionCard>

            <SectionCard title={t("settings.data_retention")} description={t("settings.data_retention_desc")}>
              <div class="space-y-4">
                <div class="flex items-center justify-between">
                  <div>
                    <p class="text-xs text-[var(--color-text)] font-medium">{t("settings.query_logs")}</p>
                    <p class="text-[10px] text-slate-500">dns_queries, top_domains</p>
                  </div>
                  <div class="flex items-center gap-2">
                    <select
                      value={srvConfig()?.retention_query_days || 30}
                      onChange={async (e) => {
                        const days = parseInt(e.currentTarget.value);
                        try {
                          await serverConfigAPI.update({ retention_query_days: days } as any);
                          setSrvConfig((prev) => prev ? { ...prev, retention_query_days: days } : prev);
                          showSrvMsg(t("settings.retention_updated"));
                        } catch (err: any) { showSrvMsg(err.message, true); }
                      }}
                      class="bg-slate-900 text-[var(--color-text)] text-xs rounded-lg px-3 py-1.5 border border-slate-600 w-32"
                    >
                      <option value={7}>7 {t("settings.days")}</option>
                      <option value={14}>14 {t("settings.days")}</option>
                      <option value={30}>30 {t("settings.days")}</option>
                      <option value={60}>60 {t("settings.days")}</option>
                      <option value={90}>90 {t("settings.days")}</option>
                      <option value={180}>180 {t("settings.days")}</option>
                      <option value={365}>365 {t("settings.days")}</option>
                    </select>
                  </div>
                </div>
                <div class="flex items-center justify-between">
                  <div>
                    <p class="text-xs text-[var(--color-text)] font-medium">{t("settings.metrics")}</p>
                    <p class="text-[10px] text-slate-500">QPS, latency, cache, system</p>
                  </div>
                  <div class="flex items-center gap-2">
                    <select
                      value={srvConfig()?.retention_metrics_days || 15}
                      onChange={async (e) => {
                        const days = parseInt(e.currentTarget.value);
                        try {
                          await serverConfigAPI.update({ retention_metrics_days: days } as any);
                          setSrvConfig((prev) => prev ? { ...prev, retention_metrics_days: days } : prev);
                          showSrvMsg(t("settings.retention_updated"));
                        } catch (err: any) { showSrvMsg(err.message, true); }
                      }}
                      class="bg-slate-900 text-[var(--color-text)] text-xs rounded-lg px-3 py-1.5 border border-slate-600 w-32"
                    >
                      <option value={7}>7 {t("settings.days")}</option>
                      <option value={15}>15 {t("settings.days")}</option>
                      <option value={30}>30 {t("settings.days")}</option>
                      <option value={60}>60 {t("settings.days")}</option>
                      <option value={90}>90 {t("settings.days")}</option>
                    </select>
                  </div>
                </div>
              </div>
            </SectionCard>

            <SectionCard title={t("settings.allowed_subnets")} description={t("settings.allowed_subnets_desc")}>
              <div class="space-y-2 mb-4">
                <For each={srvConfig()?.allowed_subnets || []} fallback={
                  <p class="text-xs text-slate-500 py-2">{t("settings.no_subnets")}</p>
                }>
                  {(subnet, i) => (
                    <div class="flex items-center justify-between bg-slate-900/50 rounded-lg px-3.5 py-2.5 group border border-slate-700/50">
                      <div class="flex items-center gap-2.5">
                        <span class="w-2 h-2 rounded-full bg-emerald-500 flex-shrink-0" />
                        <span class="text-xs text-[var(--color-text)] font-mono">{subnet}</span>
                      </div>
                      <button
                        onClick={async () => {
                          const current = srvConfig();
                          if (!current) return;
                          const updated = current.allowed_subnets.filter((_, idx) => idx !== i());
                          try {
                            await serverConfigAPI.update({ allowed_subnets: updated });
                            setSrvConfig({ ...current, allowed_subnets: updated });
                            showSrvMsg("Subnet removed, kresd restarted");
                          } catch (err: any) { showSrvMsg(err.message, true); }
                        }}
                        class="px-2 py-1 text-[10px] text-red-400 rounded opacity-0 group-hover:opacity-100 transition-all hover:bg-red-500/10"
                      >
                        Remove
                      </button>
                    </div>
                  )}
                </For>
              </div>

              <form
                onSubmit={async (e) => {
                  e.preventDefault();
                  const subnet = newSubnet().trim();
                  if (!subnet) return;
                  if (!/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$/.test(subnet)) {
                    showSrvMsg("Invalid format. Use CIDR notation (e.g. 10.0.0.0/24)", true);
                    return;
                  }
                  const current = srvConfig();
                  if (!current) return;
                  if (current.allowed_subnets.includes(subnet)) {
                    showSrvMsg("Subnet already exists", true);
                    return;
                  }
                  const updated = [...current.allowed_subnets, subnet];
                  try {
                    await serverConfigAPI.update({ allowed_subnets: updated });
                    setSrvConfig({ ...current, allowed_subnets: updated });
                    setNewSubnet("");
                    showSrvMsg("Subnet added, kresd restarted");
                  } catch (err: any) { showSrvMsg(err.message, true); }
                }}
                class="flex gap-2"
              >
                <input
                  type="text"
                  placeholder="e.g. 10.0.0.0/24"
                  class="flex-1 bg-slate-900 text-[var(--color-text)] text-xs rounded-lg px-3 py-2 border border-slate-600 placeholder-slate-600 font-mono"
                  value={newSubnet()}
                  onInput={(e) => setNewSubnet(e.currentTarget.value)}
                />
                <button type="submit"
                  class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white text-xs font-medium rounded-lg transition-colors flex-shrink-0">
                  {t("settings.add_subnet")}
                </button>
              </form>
            </SectionCard>
          </Show>

          {/* ===================== UPDATE & SERVICES ===================== */}
          <Show when={activeTab() === "update"}>
            <SectionCard title={t("settings.system_update")} description={
              updateInfo() ? `v${updateInfo()!.current_version}${updateInfo()!.current_commit ? ` (${updateInfo()!.current_commit})` : ""}` : "Check for updates from GitHub"
            }>
              <div class="space-y-3">
                <div class="flex gap-2">
                  <button onClick={checkForUpdates} disabled={updateChecking() || updateRunning()}
                    class="px-3 py-1.5 bg-slate-700 hover:bg-slate-600 disabled:opacity-50 text-[var(--color-text)] text-xs rounded-lg transition-colors">
                    {updateChecking() ? t("settings.checking") : t("settings.check_updates")}
                  </button>
                  <Show when={updateInfo()?.update_available}>
                    <button onClick={executeUpdate} disabled={updateRunning()}
                      class="px-3 py-1.5 bg-amber-600 hover:bg-amber-700 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors">
                      {updateRunning() ? t("settings.updating") : t("settings.update_now")}
                    </button>
                  </Show>
                  <Show when={updateInfo() && !updateInfo()!.update_available}>
                    <button onClick={executeUpdate} disabled={updateRunning()}
                      class="px-3 py-1.5 bg-slate-700 hover:bg-slate-600 disabled:opacity-50 text-[var(--color-text)] text-xs rounded-lg transition-colors">
                      {updateRunning() ? t("settings.updating") : t("settings.force_rebuild")}
                    </button>
                  </Show>
                </div>

                <Show when={updateError()}>
                  <Alert msg={updateError()} error={true} />
                </Show>

                <Show when={updateInfo()?.update_available}>
                  <div class="p-3 rounded-lg bg-amber-500/10 border border-amber-500/20">
                    <div class="flex items-center gap-2 mb-2">
                      <span class="w-1.5 h-1.5 bg-amber-400 rounded-full animate-pulse" />
                      <span class="text-xs font-medium text-amber-400">
                        {updateInfo()!.commits_behind} {t("settings.updates_available")}
                      </span>
                    </div>
                    <div class="space-y-0.5 ml-3.5">
                      <For each={updateInfo()!.commit_log.slice(0, 5)}>
                        {(commit) => <p class="text-[10px] text-slate-400 font-mono truncate">{commit}</p>}
                      </For>
                      <Show when={updateInfo()!.commit_log.length > 5}>
                        <p class="text-[10px] text-slate-600">+{updateInfo()!.commit_log.length - 5} {t("settings.more")}</p>
                      </Show>
                    </div>
                  </div>
                </Show>

                <Show when={updateInfo() && !updateInfo()!.update_available && !updateDone()}>
                  <Alert msg={t("settings.up_to_date")} error={false} />
                </Show>

                {/* Progress bar during update */}
                <Show when={updateRunning() || restarting()}>
                  <div class="p-3 bg-slate-900 rounded-lg border border-slate-700/50 space-y-2.5">
                    <div class="flex items-center justify-between">
                      <span class="text-xs text-blue-400 font-medium">
                        {restarting() ? "Restarting services..." : updateProgress().step}
                      </span>
                      <span class="text-[10px] text-slate-500">{restarting() ? "95%" : `${updateProgress().pct}%`}</span>
                    </div>
                    <div class="h-2 rounded-full bg-slate-700 overflow-hidden">
                      <div
                        class={`h-full rounded-full transition-all duration-500 ease-out ${restarting() ? "bg-amber-500 animate-pulse" : "bg-blue-500"}`}
                        style={{ width: `${restarting() ? 95 : updateProgress().pct}%` }}
                      />
                    </div>
                  </div>
                </Show>

                {/* Success */}
                <Show when={updateDone() && !updateRunning()}>
                  <div class="p-3 bg-emerald-500/10 rounded-lg border border-emerald-500/20 flex items-center gap-2">
                    <svg class="w-4 h-4 text-emerald-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
                      <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
                    </svg>
                    <span class="text-xs text-emerald-400 font-medium">{t("settings.update_complete")}</span>
                  </div>
                </Show>

                {/* Failed */}
                <Show when={updateFailed() && !updateRunning()}>
                  <div class="space-y-2">
                    <button
                      class="w-full p-3 bg-red-500/10 rounded-lg border border-red-500/20 flex items-center justify-between cursor-pointer hover:bg-red-500/15 transition-colors"
                      onClick={() => setShowUpdateLog(!showUpdateLog())}
                    >
                      <div class="flex items-center gap-2">
                        <svg class="w-4 h-4 text-red-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
                          <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
                        </svg>
                        <span class="text-xs text-red-400 font-medium">Update failed</span>
                      </div>
                      <svg class={`w-3.5 h-3.5 text-red-400/60 transition-transform ${showUpdateLog() ? "rotate-180" : ""}`} fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                        <path stroke-linecap="round" stroke-linejoin="round" d="M19 9l-7 7-7-7" />
                      </svg>
                    </button>
                    <Show when={showUpdateLog()}>
                      <div class="bg-slate-950 rounded-lg p-3 font-mono text-[11px] leading-5 max-h-64 overflow-y-auto border border-slate-700/50">
                        <For each={updateOutput()}>
                          {(line) => (
                            <div class={
                              line.includes("[OK]") ? "text-emerald-400" :
                              line.includes("[WARN]") ? "text-amber-400" :
                              line.includes("[ERROR]") ? "text-red-400" :
                              line.includes("[INFO]") ? "text-blue-400" :
                              "text-slate-400"
                            }>{line}</div>
                          )}
                        </For>
                      </div>
                    </Show>
                  </div>
                </Show>
              </div>
            </SectionCard>

            <SectionCard title={t("settings.auto_update")} description={t("settings.auto_update_desc")}>
              <div class="space-y-4">
                {/* Enable/disable toggle */}
                <div class="flex items-center justify-between">
                  <div>
                    <p class="text-xs text-[var(--color-text)] font-medium">{t("settings.auto_update")}</p>
                    <p class="text-[10px] text-slate-500">
                      <Show when={autoUpdateCfg()?.last_auto_update}>
                        {t("settings.last_auto_update")} {new Date(autoUpdateCfg()!.last_auto_update!).toLocaleString()}
                      </Show>
                      <Show when={!autoUpdateCfg()?.last_auto_update}>
                        {t("settings.last_auto_update")} {t("settings.never")}
                      </Show>
                    </p>
                  </div>
                  <button
                    onClick={async () => {
                      const cur = autoUpdateCfg();
                      if (!cur) return;
                      const val = !cur.auto_update_enabled;
                      try {
                        await updateAPI.updateAutoConfig({ auto_update_enabled: val } as any);
                        setAutoUpdateCfg({ ...cur, auto_update_enabled: val });
                        showSrvMsg(val ? t("settings.auto_update_enabled") : t("settings.auto_update_disabled"));
                      } catch (err: any) { showSrvMsg(err.message, true); }
                    }}
                    class={`relative w-10 h-5 rounded-full transition-colors ${autoUpdateCfg()?.auto_update_enabled ? "bg-blue-600" : "bg-slate-600"}`}
                  >
                    <span class={`absolute top-0.5 w-4 h-4 bg-white rounded-full transition-transform ${autoUpdateCfg()?.auto_update_enabled ? "left-5" : "left-0.5"}`} />
                  </button>
                </div>

                {/* Schedule options */}
                <Show when={autoUpdateCfg()?.auto_update_enabled}>
                  <div class="flex flex-wrap gap-3">
                    <div class="flex items-center gap-1.5">
                      <label class="text-[10px] text-slate-400">{t("settings.update_frequency")}</label>
                      <select
                        value={autoUpdateCfg()?.auto_update_day ?? 0}
                        onChange={async (e) => {
                          const day = parseInt(e.currentTarget.value);
                          const cur = autoUpdateCfg();
                          if (!cur) return;
                          try {
                            await updateAPI.updateAutoConfig({ auto_update_day: day } as any);
                            setAutoUpdateCfg({ ...cur, auto_update_day: day });
                          } catch (err: any) { showSrvMsg(err.message, true); }
                        }}
                        class="bg-slate-900 text-[var(--color-text)] text-xs rounded-lg px-2 py-1.5 border border-slate-600"
                      >
                        <option value={0}>{t("settings.daily")}</option>
                        <option value={1}>{t("settings.weekly_mon")}</option>
                        <option value={2}>{t("settings.weekly_tue")}</option>
                        <option value={3}>{t("settings.weekly_wed")}</option>
                        <option value={4}>{t("settings.weekly_thu")}</option>
                        <option value={5}>{t("settings.weekly_fri")}</option>
                        <option value={6}>{t("settings.weekly_sat")}</option>
                        <option value={7}>{t("settings.weekly_sun")}</option>
                      </select>
                    </div>
                    <div class="flex items-center gap-1.5">
                      <label class="text-[10px] text-slate-400">{t("settings.update_hour")}</label>
                      <select
                        value={autoUpdateCfg()?.auto_update_hour ?? 3}
                        onChange={async (e) => {
                          const hour = parseInt(e.currentTarget.value);
                          const cur = autoUpdateCfg();
                          if (!cur) return;
                          try {
                            await updateAPI.updateAutoConfig({ auto_update_hour: hour } as any);
                            setAutoUpdateCfg({ ...cur, auto_update_hour: hour });
                          } catch (err: any) { showSrvMsg(err.message, true); }
                        }}
                        class="bg-slate-900 text-[var(--color-text)] text-xs rounded-lg px-2 py-1.5 border border-slate-600"
                      >
                        {Array.from({ length: 24 }, (_, i) => (
                          <option value={i}>{i.toString().padStart(2, "0")}:00</option>
                        ))}
                      </select>
                    </div>
                  </div>

                  <div class="p-2 bg-blue-500/5 rounded-lg border border-blue-500/10">
                    <p class="text-[10px] text-slate-400">
                      {(() => {
                        const cfg = autoUpdateCfg()!;
                        const h = cfg.auto_update_hour.toString().padStart(2, "0");
                        const tz = srvConfig()?.timezone || "Asia/Jakarta";
                        const days = ["", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
                        const freq = cfg.auto_update_day === 0
                          ? `Daily at ${h}:00 ${tz}`
                          : `Every ${days[cfg.auto_update_day]} at ${h}:00 ${tz}`;
                        return freq;
                      })()}
                    </p>
                    <p class="text-[10px] text-slate-500 mt-0.5">
                      Will not run if RPZ sync is in progress
                    </p>
                  </div>
                </Show>
              </div>
            </SectionCard>

            <SectionCard title={t("settings.services")} description={t("settings.services_desc")}>
              <div class="flex gap-2 mb-3">
                <button onClick={loadServices}
                  class="px-2.5 py-1.5 text-[10px] bg-slate-700 hover:bg-slate-600 text-slate-300 rounded-lg transition-colors">
                  Refresh
                </button>
                <button onClick={handleRestartAll} disabled={restartAllRunning()}
                  class="px-2.5 py-1.5 text-[10px] bg-amber-600/20 hover:bg-amber-600/30 text-amber-400 rounded-lg transition-colors disabled:opacity-50">
                  {restartAllRunning() ? t("settings.restarting") : t("settings.restart_all")}
                </button>
              </div>

              <Show when={restartOutput().length > 0}>
                <div class="mb-3 bg-slate-950 rounded-lg p-3 font-mono text-[11px] leading-5 max-h-40 overflow-y-auto border border-slate-700/50"
                  ref={(el) => {
                    const observer = new MutationObserver(() => { el.scrollTop = el.scrollHeight; });
                    observer.observe(el, { childList: true, subtree: true });
                  }}>
                  <For each={restartOutput()}>
                    {(line) => (
                      <div class={
                        line.includes("[OK]") ? "text-emerald-400" :
                        line.includes("[ERROR]") ? "text-red-400" :
                        "text-slate-400"
                      }>{line}</div>
                    )}
                  </For>
                  <Show when={restartAllRunning()}>
                    <div class="text-amber-400 animate-pulse mt-1">Restarting...</div>
                  </Show>
                </div>
              </Show>

              <div class="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
                <For each={services()}>
                  {(svc) => (
                    <div class="flex items-center justify-between bg-slate-900/50 rounded-lg px-3 py-2.5 group border border-slate-700/30">
                      <div class="flex items-center gap-2">
                        <span class={`w-2 h-2 rounded-full flex-shrink-0 ${
                          svc.status === "running" ? "bg-emerald-500" :
                          svc.status === "exited" ? "bg-red-500" :
                          svc.status === "restarting" ? "bg-amber-500" : "bg-slate-500"
                        }`} />
                        <span class="text-xs text-[var(--color-text)]">{svc.name}</span>
                        <Show when={svc.health}>
                          <span class={`text-[10px] ${svc.health === "healthy" ? "text-emerald-500" : "text-amber-500"}`}>
                            ({svc.health})
                          </span>
                        </Show>
                      </div>
                      <button
                        onClick={() => handleRestartService(svc.name)}
                        disabled={restartingService() !== "" || restartAllRunning()}
                        class="px-2 py-0.5 text-[10px] bg-slate-700 hover:bg-slate-600 text-slate-400 rounded opacity-0 group-hover:opacity-100 transition-all disabled:opacity-50"
                      >
                        {restartingService() === svc.name ? "..." : "Restart"}
                      </button>
                    </div>
                  )}
                </For>
              </div>

              <Show when={services().length === 0}>
                <p class="text-xs text-slate-500 text-center py-3">Loading services...</p>
              </Show>
            </SectionCard>
          </Show>

          {/* ===================== CLUSTER ===================== */}
          <Show when={activeTab() === "cluster"}>
            <SectionCard title={t("settings.node_role")} description={t("settings.node_role_desc")}>
              {clusterMsg() && <div class="mb-4"><Alert msg={clusterMsg()} error={clusterError()} /></div>}
              <div class="grid grid-cols-3 gap-2">
                {(["standalone", "controller", "agent"] as const).map((r) => (
                  <button
                    onClick={() => handleRoleChange(r)}
                    class={`px-3 py-3 rounded-lg text-xs font-medium transition-all border text-center ${
                      clusterConfig()?.node_role === r
                        ? r === "controller" ? "bg-blue-600/20 text-blue-400 border-blue-500/30"
                        : r === "agent" ? "bg-purple-600/20 text-purple-400 border-purple-500/30"
                        : "bg-slate-600/20 text-slate-300 border-slate-500/30"
                        : "bg-slate-900/30 text-slate-500 border-slate-700 hover:border-slate-600 hover:text-slate-400"
                    }`}
                  >
                    <div class="font-semibold">{r === "standalone" ? "Standalone" : r === "controller" ? "Controller" : "Agent"}</div>
                    <div class="text-[10px] mt-1 opacity-70">
                      {r === "standalone" ? "Single server" : r === "controller" ? "Central hub" : "Monitored node"}
                    </div>
                  </button>
                ))}
              </div>
            </SectionCard>

            {/* Controller: registered agents */}
            <Show when={clusterConfig()?.node_role === "controller"}>
              <SectionCard title="Registered Agents" description={`${clusterNodes().length} node(s) connected`}>
                <Show when={clusterNodes().length > 0}>
                  <div class="space-y-1.5 mb-4">
                    <For each={clusterNodes()}>
                      {(node) => (
                        <div class="flex items-center justify-between bg-slate-900/50 rounded-lg px-3.5 py-2.5 group border border-slate-700/30">
                          <div class="flex items-center gap-2.5 min-w-0">
                            <span class={`w-2 h-2 rounded-full flex-shrink-0 ${
                              node.status === "online" ? "bg-emerald-500" :
                              node.status === "offline" ? "bg-red-500" : "bg-slate-500"
                            }`} />
                            <div class="min-w-0">
                              <p class="text-xs text-[var(--color-text)] truncate">{node.name || node.domain}</p>
                              <p class="text-[10px] text-slate-600 truncate">{node.domain}{node.version ? ` — ${node.version}` : ""}</p>
                            </div>
                          </div>
                          <div class="flex items-center gap-1.5 opacity-0 group-hover:opacity-100 transition-opacity">
                            <button onClick={() => handleRemoteNodeUpdate(node.id)} disabled={updateRunning()}
                              class="px-2 py-1 text-[10px] bg-amber-500/10 text-amber-400 rounded hover:bg-amber-500/20 transition-colors">
                              Update
                            </button>
                            <button onClick={() => handleDeleteNodeAction(node.id)}
                              class="px-2 py-1 text-[10px] bg-red-500/10 text-red-400 rounded hover:bg-red-500/20 transition-colors">
                              Delete
                            </button>
                          </div>
                        </div>
                      )}
                    </For>
                  </div>
                </Show>

                <Show when={generatedToken()}>
                  <div class="p-3 bg-amber-500/10 rounded-lg border border-amber-500/20 mb-4">
                    <p class="text-[10px] text-amber-400 mb-1.5 font-medium">API Token (shown only once):</p>
                    <div class="flex items-center gap-2">
                      <code class="flex-1 bg-slate-950 px-2.5 py-1.5 rounded text-[10px] text-[var(--color-text)] font-mono break-all select-all">{generatedToken()}</code>
                      <button onClick={() => navigator.clipboard.writeText(generatedToken())}
                        class="px-2.5 py-1.5 bg-amber-600 text-white text-[10px] rounded hover:bg-amber-700 transition-colors flex-shrink-0">
                        Copy
                      </button>
                    </div>
                  </div>
                </Show>

                <form onSubmit={handleAddNode} class="flex gap-2 items-end">
                  <div class="flex-1">
                    <label class={labelClass}>Name</label>
                    <input type="text" required placeholder="DNS SG-2" class={inputClass} value={newNodeName()} onInput={(e) => setNewNodeName(e.target.value)} />
                  </div>
                  <div class="flex-1">
                    <label class={labelClass}>Domain</label>
                    <input type="text" required placeholder="dns2.example.com" class={inputClass} value={newNodeDomain()} onInput={(e) => setNewNodeDomain(e.target.value)} />
                  </div>
                  <button type="submit" class="px-3 py-2 bg-blue-600 hover:bg-blue-700 text-white text-xs rounded-lg transition-colors flex-shrink-0">
                    + Add
                  </button>
                </form>
              </SectionCard>
            </Show>

            {/* Agent: connection config */}
            <Show when={clusterConfig()?.node_role === "agent"}>
              <SectionCard title="Controller Connection" description="Connect this agent to a controller node">
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 mb-4">
                  <div>
                    <label class={labelClass}>Controller Domain</label>
                    <input type="text" placeholder="dns1.example.com" class={inputClass}
                      value={agentControllerDomain()} onInput={(e) => setAgentControllerDomain(e.target.value)} />
                  </div>
                  <div>
                    <label class={labelClass}>Cluster Token</label>
                    <input type="password" placeholder="Paste token from controller" class={inputClass}
                      value={agentToken()} onInput={(e) => setAgentToken(e.target.value)} />
                  </div>
                </div>
                <button onClick={handleSaveAgentConfig}
                  class="px-4 py-2 bg-purple-600 hover:bg-purple-700 text-white text-xs font-medium rounded-lg transition-colors">
                  Save
                </button>
              </SectionCard>
            </Show>
          </Show>

      </div>
    </Layout>
  );
}
