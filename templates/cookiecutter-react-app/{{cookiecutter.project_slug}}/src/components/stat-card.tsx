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
  valueTitle,
}: {
  label: string;
  value: number | string;
  Icon: Icon;
  caption?: string;
  tone?: keyof typeof TONE;
  loading?: boolean;
  /** The full text, when `value` had to be shortened to fit. Surfaced as a tooltip. */
  valueTitle?: string;
}) {
  return (
    // `min-w-0` is load-bearing: a grid item defaults to `min-width: auto`, so one long
    // unbreakable value — a commit SHA, an identifier — widens its column and drags the whole
    // row out of shape rather than being clipped. It pairs with `truncate` below, and neither
    // does anything on its own.
    <div className="min-w-0 rounded-2xl border border-hairline bg-surface p-[18px]">
      <div className="flex items-start justify-between">
        <span className="text-[12.5px] text-subtext">{label}</span>
        <Icon size={18} className="text-faint" />
      </div>
      {loading ? (
        <div className="mt-2 h-[27px] w-12 animate-pulse rounded bg-subtle" />
      ) : (
        <div
          className="mt-2 truncate text-[27px] font-bold leading-none text-ink"
          title={valueTitle}
        >
          {value}
        </div>
      )}
      {/* Always rendered, even when empty: a caption that appears on some cards and not others
          changes their height and makes the row jump as data arrives. */}
      <div
        className={cn("mt-[7px] truncate text-[12px]", loading ? "text-faint" : TONE[tone])}
        title={caption}
      >
        {loading ? "—" : (caption ?? " ")}
      </div>
    </div>
  );
}
