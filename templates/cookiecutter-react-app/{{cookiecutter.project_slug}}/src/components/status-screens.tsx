import { type ReactNode } from "react";
import { Link, useRouter, type ErrorComponentProps } from "@tanstack/react-router";
import { ArrowClockwise, ArrowLeft, Cube, Warning } from "@phosphor-icons/react";

// Button treatments, kept next to each other so the two screens cannot drift apart.
const PRIMARY =
  "inline-flex items-center justify-center gap-2 rounded-[10px] bg-brand px-[18px] py-[10px] text-[13.5px] font-semibold text-white hover:bg-brand-hover";
const GHOST =
  "inline-flex items-center justify-center gap-2 rounded-[10px] border border-field bg-surface px-[18px] py-[10px] text-[13.5px] font-semibold text-body hover:bg-subtle";

/**
 * The shared status surface. `full` centres a card on its own background for chrome-less
 * contexts — a top-level 404, or an error thrown before the shell mounted. `inline` centres
 * inside the shell's main area so the sidebar stays exactly where the user left it.
 */
function StatusCard({ variant, children }: { variant: "full" | "inline"; children: ReactNode }) {
  const card = (
    <div className="w-full max-w-[440px] rounded-2xl border border-hairline bg-surface p-9 text-center shadow-card [animation:riseIn_.5s_cubic-bezier(.2,.7,.2,1)_both] motion-reduce:animate-none">
      {children}
    </div>
  );
  if (variant === "inline") {
    return <div className="flex min-h-full items-center justify-center p-10">{card}</div>;
  }
  return (
    <div className="flex min-h-dvh items-center justify-center bg-page p-6">{card}</div>
  );
}

function Mark({ tone }: { tone: "brand" | "warning" }) {
  if (tone === "warning") {
    return (
      <div className="mx-auto mb-5 flex size-[42px] items-center justify-center rounded-[12px] bg-warning-soft">
        <Warning weight="fill" size={22} className="text-warning" />
      </div>
    );
  }
  return (
    <div className="mx-auto mb-5 flex size-[42px] items-center justify-center rounded-[12px] bg-brand">
      <Cube weight="fill" size={22} className="text-white" />
    </div>
  );
}

function Title({ children }: { children: ReactNode }) {
  return <h1 className="text-[19px] font-bold tracking-[-0.02em] text-ink">{children}</h1>;
}

function Description({ children }: { children: ReactNode }) {
  return (
    <p className="mx-auto mt-2 max-w-[340px] text-[13.5px] leading-[1.55] text-subtext">
      {children}
    </p>
  );
}

/**
 * Chrome-less 404, shown for any URL the route tree does not resolve. It prints the attempted
 * path: "page not found" without saying which page is a support ticket with no information in it.
 */
export function NotFoundScreen() {
  const path = typeof window === "undefined" ? "" : window.location.pathname;

  return (
    <StatusCard variant="full">
      <Mark tone="brand" />
      {path && (
        <div className="mx-auto mb-5 inline-flex max-w-full items-center gap-2 rounded-full border border-hairline bg-subtle px-3 py-[5px] font-mono text-[12px] text-mute">
          <span className="truncate">{path}</span>
          <span className="flex-none rounded-full bg-danger-soft px-[7px] py-[1px] text-[10px] font-semibold text-destructive">
            not found
          </span>
        </div>
      )}
      <Title>Page not found</Title>
      <Description>
        We couldn&rsquo;t find a page at that address. It may have moved, or the link was mistyped.
      </Description>
      <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
        <Link to="/" className={PRIMARY}>
          <ArrowLeft weight="bold" size={15} />
          Back to the dashboard
        </Link>
      </div>
    </StatusCard>
  );
}

function ErrorScreen({
  variant,
  error,
  reset,
}: {
  variant: "full" | "inline";
  error?: Error;
  reset: () => void;
}) {
  const router = useRouter();

  function tryAgain() {
    reset();
    // Clearing the boundary re-renders the same failing data unless the router reloads it too.
    router.invalidate();
  }

  return (
    <StatusCard variant={variant}>
      <Mark tone="warning" />
      <Title>Something went wrong</Title>
      <Description>
        This page ran into an unexpected error. You can try again, or head back to the dashboard.
      </Description>
      <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
        <button type="button" onClick={tryAgain} className={PRIMARY}>
          <ArrowClockwise weight="bold" size={15} />
          Try again
        </button>
        <Link to="/" className={GHOST}>
          Back to the dashboard
        </Link>
      </div>

      {/* Stack traces are for developers. `import.meta.env.DEV` is compiled out of the production
          bundle entirely, so this block does not ship — it is not merely hidden. */}
      {import.meta.env.DEV && error && (
        <details className="mt-6 text-left">
          <summary className="cursor-pointer text-[12px] font-semibold text-mute">
            Error details (dev only)
          </summary>
          <pre className="mt-2 max-h-[200px] overflow-auto rounded-lg bg-subtle p-3 font-mono text-[11px] leading-relaxed text-body">
            {error.message}
            {error.stack ? `\n\n${error.stack}` : ""}
          </pre>
        </details>
      )}
    </StatusCard>
  );
}

/** The router's catch-all: errors above the shell, or before it mounts. */
export function RootErrorScreen({ error, reset }: ErrorComponentProps) {
  // The router types what was thrown as `unknown`, correctly — `throw "nope"` is legal
  // JavaScript. Anything that is not an Error carries no message worth showing.
  return (
    <ErrorScreen
      variant="full"
      error={error instanceof Error ? error : undefined}
      reset={reset}
    />
  );
}

/** The in-shell fallback, via ErrorBoundary in AppShell — keeps the navigation usable. */
export function InShellErrorScreen({ error, reset }: { error: Error; reset: () => void }) {
  return <ErrorScreen variant="inline" error={error} reset={reset} />;
}
