import { CircleNotch } from "@phosphor-icons/react";

import { cn } from "@/lib/utils";

function Spinner({ size, className }: { size?: number; className?: string }) {
  return (
    <CircleNotch
      weight="bold"
      size={size ?? 16}
      className={cn("animate-spin", className)}
    />
  );
}

function PageSpinner({ className }: { className?: string }) {
  return (
    <div className={cn("flex min-h-[60vh] items-center justify-center", className)}>
      <Spinner size={28} className="text-brand" />
    </div>
  );
}

export { Spinner, PageSpinner };
