import { createSignal } from "solid-js";

export type Theme = "dark" | "light" | "system";

const STORAGE_KEY = "theme";

function readStored(): Theme {
  if (typeof localStorage === "undefined") return "system";
  const v = localStorage.getItem(STORAGE_KEY);
  return v === "dark" || v === "light" || v === "system" ? v : "system";
}

const [theme, setThemeRaw] = createSignal<Theme>(readStored());

/** The theme actually painted right now — "system" resolved against the OS. */
export function resolvedTheme(): "dark" | "light" {
  const t = theme();
  if (t !== "system") return t;
  if (typeof window === "undefined" || !window.matchMedia) return "dark";
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

/**
 * Stamp the theme onto <html>. Every color token in app.css keys off
 * [data-theme], so this single attribute repaints the whole app.
 */
export function applyTheme() {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  root.setAttribute("data-theme", resolvedTheme());
  root.style.colorScheme = resolvedTheme();
}

export function getTheme() {
  return theme();
}

export function setTheme(t: Theme) {
  setThemeRaw(t);
  if (typeof localStorage !== "undefined") localStorage.setItem(STORAGE_KEY, t);
  applyTheme();
}

/** Cycle dark → light → system, the order the toggle button steps through. */
export function cycleTheme() {
  const order: Theme[] = ["dark", "light", "system"];
  setTheme(order[(order.indexOf(theme()) + 1) % order.length]);
}

/** Follow the OS while the user is on "system". */
export function initTheme() {
  applyTheme();
  if (typeof window === "undefined" || !window.matchMedia) return;
  window
    .matchMedia("(prefers-color-scheme: light)")
    .addEventListener("change", () => {
      if (theme() === "system") applyTheme();
    });
}
