/**
 * Format an ISO timestamp as a short relative-day label: "Today", "Yesterday", "N days ago",
 * "Last week", or a short date like "24 Jun".
 *
 * `now` is a parameter rather than a call to `new Date()` inside so the behaviour is testable
 * without freezing the clock.
 */
export function formatRelativeDay(iso: string, now: Date = new Date()): string {
  const then = new Date(iso);
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const startOfThen = new Date(then.getFullYear(), then.getMonth(), then.getDate());
  const dayMs = 24 * 60 * 60 * 1000;
  const days = Math.round((startOfToday.getTime() - startOfThen.getTime()) / dayMs);

  if (days <= 0) return "Today";
  if (days === 1) return "Yesterday";
  if (days < 7) return `${days} days ago`;
  if (days < 14) return "Last week";
  return then.toLocaleDateString("en-GB", { day: "numeric", month: "short" });
}
