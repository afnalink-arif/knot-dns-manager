import { createSignal, onMount, Show } from "solid-js";
import { useNavigate } from "@solidjs/router";
import { authAPI } from "~/lib/api";
import { setToken, setUserInfo, isLoggedIn } from "~/lib/auth";
import { t, getLang, setLang } from "~/lib/i18n";
import "~/app.css";

// Same brand logo as Layout
const BrandLogo = (props: { class?: string }) => (
  <svg viewBox="0 0 32 32" fill="none" class={props.class || "w-8 h-8"}>
    <defs>
      <linearGradient id="login-logo-grad" x1="0%" y1="0%" x2="100%" y2="100%">
        <stop offset="0%" stop-color="#60a5fa"/>
        <stop offset="100%" stop-color="#2563eb"/>
      </linearGradient>
    </defs>
    <rect width="32" height="32" rx="8" fill="url(#login-logo-grad)"/>
    <circle cx="16" cy="10" r="2.5" fill="white"/>
    <circle cx="9" cy="20" r="2" fill="white" opacity="0.8"/>
    <circle cx="23" cy="20" r="2" fill="white" opacity="0.8"/>
    <circle cx="16" cy="25" r="1.5" fill="white" opacity="0.6"/>
    <line x1="16" y1="12.5" x2="9" y2="18" stroke="white" stroke-width="1.2" opacity="0.5"/>
    <line x1="16" y1="12.5" x2="23" y2="18" stroke="white" stroke-width="1.2" opacity="0.5"/>
    <line x1="9" y1="22" x2="16" y2="23.5" stroke="white" stroke-width="1" opacity="0.35"/>
    <line x1="23" y1="22" x2="16" y2="23.5" stroke="white" stroke-width="1" opacity="0.35"/>
  </svg>
);

export default function LoginPage() {
  const navigate = useNavigate();
  const [username, setUsername] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [error, setError] = createSignal("");
  const [loading, setLoading] = createSignal(false);
  const [setupMode, setSetupMode] = createSignal(false);

  onMount(async () => {
    if (isLoggedIn()) {
      navigate("/", { replace: true });
      return;
    }
    try {
      const check = await authAPI.check();
      setSetupMode(check.setup_needed);
    } catch {}
  });

  const handleSubmit = async (e: Event) => {
    e.preventDefault();
    setError("");
    setLoading(true);

    try {
      if (setupMode()) {
        await authAPI.register(username(), password());
      }
      const data = await authAPI.login(username(), password());
      setToken(data.token);
      setUserInfo(data.user);
      navigate("/", { replace: true });
    } catch (err: any) {
      setError(err.message || "Authentication failed");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div class="min-h-screen flex bg-[#030712]">
      {/* Left panel — branding */}
      <div class="hidden lg:flex lg:w-1/2 relative overflow-hidden items-center justify-center">
        {/* Background pattern */}
        <div class="absolute inset-0 opacity-[0.03]"
          style="background-image: radial-gradient(circle at 1px 1px, #3b82f6 1px, transparent 0); background-size: 32px 32px;" />
        {/* Gradient overlay */}
        <div class="absolute inset-0 bg-gradient-to-br from-blue-600/10 via-transparent to-blue-900/10" />

        <div class="relative z-10 px-16 max-w-lg">
          <div class="mb-8">
            <img src="/logo-kdm.png" alt="KResD Manager" class="h-16 object-contain" />
          </div>
          <p class="text-lg text-slate-400 leading-relaxed mb-8">
            {t("login.tagline")}
          </p>
          <div class="grid grid-cols-2 gap-4">
            {[
              { label: t("login.feat.metrics"), desc: t("login.feat.metrics_desc") },
              { label: t("login.feat.filtering"), desc: t("login.feat.filtering_desc") },
              { label: t("login.feat.logging"), desc: t("login.feat.logging_desc") },
              { label: t("login.feat.cluster"), desc: t("login.feat.cluster_desc") },
            ].map(f => (
              <div class="p-3 rounded-lg bg-white/[0.03] border border-white/[0.05]">
                <p class="text-xs font-medium text-[var(--color-text)]">{f.label}</p>
                <p class="text-[10px] text-slate-500 mt-0.5">{f.desc}</p>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Right panel — login form */}
      <div class="flex-1 flex items-center justify-center px-6">
        <div class="w-full max-w-sm">
          {/* Mobile logo */}
          <div class="lg:hidden text-center mb-8">
            <img src="/logo-kdm.png" alt="KResD Manager" class="h-12 object-contain mx-auto" />
          </div>

          <div class="mb-6">
            <div class="flex items-center justify-between">
              <h2 class="text-xl font-semibold text-[var(--color-text)]">
                {setupMode() ? t("login.create_admin") : t("login.sign_in")}
              </h2>
              <button
                onClick={() => setLang(getLang() === "en" ? "id" : "en")}
                class="px-2 py-1 rounded text-[11px] text-slate-500 hover:text-[var(--color-text)] hover:bg-white/5 transition-colors font-mono"
              >
                {getLang() === "en" ? "EN" : "ID"}
              </button>
            </div>
            <p class="text-sm text-slate-500 mt-1">
              {setupMode() ? t("login.subtitle_setup") : t("login.subtitle")}
            </p>
          </div>

          <form onSubmit={handleSubmit} class="space-y-4">
            <Show when={setupMode()}>
              <div class="p-3 bg-blue-500/10 border border-blue-500/15 rounded-lg">
                <p class="text-xs text-blue-400">
                  {t("login.setup_notice")}
                </p>
              </div>
            </Show>

            <Show when={error()}>
              <div class="p-3 bg-red-500/10 border border-red-500/15 rounded-lg">
                <p class="text-xs text-red-400">{error()}</p>
              </div>
            </Show>

            <div>
              <label class="block text-xs font-medium text-slate-400 mb-1.5">{t("login.username")}</label>
              <input
                type="text"
                required
                autofocus
                class="w-full bg-white/[0.04] border border-white/[0.08] rounded-lg px-3.5 py-2.5 text-sm text-[var(--color-text)] placeholder:text-slate-600 focus:outline-none focus:border-blue-500/50 focus:ring-1 focus:ring-blue-500/20 transition"
                placeholder="admin"
                value={username()}
                onInput={(e) => setUsername(e.target.value)}
              />
            </div>
            <div>
              <label class="block text-xs font-medium text-slate-400 mb-1.5">{t("login.password")}</label>
              <input
                type="password"
                required
                class="w-full bg-white/[0.04] border border-white/[0.08] rounded-lg px-3.5 py-2.5 text-sm text-[var(--color-text)] placeholder:text-slate-600 focus:outline-none focus:border-blue-500/50 focus:ring-1 focus:ring-blue-500/20 transition"
                placeholder={setupMode() ? t("login.password_min") : t("login.password_placeholder")}
                value={password()}
                onInput={(e) => setPassword(e.target.value)}
              />
            </div>

            <button
              type="submit"
              disabled={loading()}
              class="w-full py-2.5 bg-gradient-to-r from-blue-600 to-blue-700 hover:from-blue-500 hover:to-blue-600 disabled:from-blue-800 disabled:to-blue-900 disabled:cursor-not-allowed text-[var(--color-text)] text-sm font-medium rounded-lg transition-all shadow-lg shadow-blue-600/20"
            >
              {loading() ? t("login.please_wait") : setupMode() ? t("login.submit_setup") : t("login.submit")}
            </button>
          </form>

          <p class="text-center text-[10px] text-slate-600 mt-8 select-none">
            KResD Manager &copy; 2026 Afnalink. Licensed under MIT.
          </p>
        </div>
      </div>
    </div>
  );
}
