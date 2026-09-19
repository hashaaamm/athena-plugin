import type { Icon } from "@phosphor-icons/react";

import { cn } from "@/lib/utils";

const TONE = {
  success: "text-success",
  warning: "text-warning",
  muted: "text-mute",
} as const;

export function StatCard({
  label,
  value,
  Icon,
  caption,
  tone = "muted",
  loading = false,
}: {
  label: string;
  value: number | string;
  Icon: Icon;
  caption?: string;
  tone?: keyof typeof TONE;
  loading?: boolean;
}) {
  return (
    <div className="rounded-2xl border border-hairline bg-surface p-[18px]">
      <div className="flex items-start justify-between">
        <span className="text-[12.5px] text-subtext">{label}</span>
        <Icon size={18} className="text-faint" />
      </div>
      {loading ? (
        <div className="mt-2 h-[27px] w-12 animate-pulse rounded bg-subtle" />
      ) : (
        <div className="mt-2 text-[27px] font-bold leading-none text-ink">{value}</div>
      )}
      {/* Always rendered, even when empty: a caption that appears on some cards and not others
          changes their height and makes the row jump as data arrives. */}
      <div className={cn("mt-[7px] text-[12px]", loading ? "text-faint" : TONE[tone])}>
        {loading ? "—" : (caption ?? " ")}
      </div>
    </div>
  );
}
