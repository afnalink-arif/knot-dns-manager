import type { Component, JSX } from "solid-js";
import Sparkline from "./charts/Sparkline";

interface KPICardProps {
  title: string;
  value: string;
  subtitle?: string;
  sparklineData?: number[];
  color?: string;
  icon?: JSX.Element;
}

const KPICard: Component<KPICardProps> = (props) => {
  return (
    <div class="glass glass-sheen rounded-xl p-5 hover:border-[var(--color-brand-500)]/30 transition-colors">
      <div class="flex items-start justify-between">
        <div>
          <p class="text-xs text-[var(--color-text-muted)] mb-1">{props.title}</p>
          <p class="text-2xl font-bold text-[var(--color-text)]">{props.value}</p>
          {props.subtitle && (
            <p class="text-[11px] text-[var(--color-text-faint)] mt-1">{props.subtitle}</p>
          )}
        </div>
        <div class="flex flex-col items-end gap-2">
          {props.icon && <div class="text-[var(--color-text-faint)]">{props.icon}</div>}
          {props.sparklineData && (
            <Sparkline data={props.sparklineData} color={props.color || "#3b82f6"} />
          )}
        </div>
      </div>
    </div>
  );
};

export default KPICard;
