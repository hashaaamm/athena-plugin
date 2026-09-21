import {
  Link,
  Outlet,
{%- if cookiecutter.use_postgres == "yes" %}
  useNavigate,
{%- endif %}
} from "@tanstack/react-router";
import {
  Cube,
{%- if cookiecutter.use_postgres == "yes" %}
  SignOut,
{%- endif %}
  SquaresFour,
{%- if cookiecutter.use_postgres == "yes" %}
  UserCircle,
{%- endif %}
} from "@phosphor-icons/react";

import { ErrorBoundary } from "@/components/error-boundary";
import { InShellErrorScreen } from "@/components/status-screens";
{%- if cookiecutter.use_postgres == "yes" %}
import { useSignOut } from "@/lib/api/auth";
import { useAccessToken } from "@/lib/use-session";
{%- endif %}

// A page added to `router.tsx` is added here in the same change, or it is a route only a typed
// URL reaches.
const NAV = [{ to: "/", label: "Dashboard", Icon: SquaresFour }] as const;
{%- if cookiecutter.use_postgres == "yes" %}

// Shown only while a token is held. Not a permission check — the guard in `router.tsx` is — but
// a link to a page that would bounce you is a link that should not be drawn.
const PRIVATE_NAV = [{ to: "/account", label: "Account", Icon: UserCircle }] as const;
{%- endif %}

const NAV_BASE =
  "flex items-center gap-[11px] rounded-[9px] px-[10px] py-[9px] text-[13.5px] font-medium";

// Hoisted, for two reasons. They are stable references, so a re-render of the sidebar does not
// hand Link a new object every time; and the template this file came from is rendered by Jinja,
// where an inline object prop opens with the same doubled brace as a variable expression and is
// stripped out. Props whose value is an object literal live up here.
const EXACT = { exact: true };
const LOOSE = { exact: false };
const NAV_INACTIVE = { className: "text-nav hover:bg-nav-hover" };
const NAV_ACTIVE = { className: "bg-brand-soft text-ink" };

/**
 * The application chrome: a sidebar that never unmounts, and an outlet that does.
 *
 * The ErrorBoundary is inside the layout on purpose. A page that throws should cost the user the
 * page, not their navigation — being dumped on a full-screen error with no way back is the
 * difference between "something broke" and "the app is gone".
 */
export function AppShell() {
{%- if cookiecutter.use_postgres == "yes" %}
  const token = useAccessToken();
  const signOut = useSignOut();
  const navigate = useNavigate();
  const nav = token === null ? NAV : [...NAV, ...PRIVATE_NAV];

  async function onSignOut() {
    signOut();
    await navigate({ to: "/login" });
  }
{%- else %}
  const nav = NAV;
{%- endif %}

  function pageFallback(error: Error, reset: () => void) {
    return <InShellErrorScreen error={error} reset={reset} />;
  }

  return (
    <div className="flex min-h-dvh bg-page text-ink">
      <aside className="sticky top-0 flex h-dvh w-[236px] flex-none flex-col border-r border-hairline bg-surface">
        <div className="flex items-center gap-[10px] px-[18px] pb-[14px] pt-[18px]">
          <div className="flex size-[30px] items-center justify-center rounded-lg bg-brand">
            <Cube weight="fill" size={17} className="text-white" />
          </div>
          <span className="truncate text-base font-bold tracking-[-0.02em]">
            {{ cookiecutter.project_name }}
          </span>
        </div>

        <nav className="flex flex-col gap-[2px] px-3 py-[6px]">
          <div className="px-[10px] pb-[5px] pt-[10px] text-[10.5px] font-semibold uppercase tracking-[0.05em] text-faint">
            Workspace
          </div>
          {nav.map(({ to, label, Icon }) => (
            // TanStack Router concatenates `className` with the active/inactive className, so
            // shared classes go here and only the state-specific ones go in the props below.
            // Repeating a colour in both is how you get two of them in the class list.
            <Link
              key={to}
              to={to}
              className={NAV_BASE}
              activeOptions={to === "/" ? EXACT : LOOSE}
              inactiveProps={NAV_INACTIVE}
              activeProps={NAV_ACTIVE}
            >
              <Icon size={18} />
              {label}
            </Link>
          ))}
        </nav>

        <div className="mt-auto px-[18px] pb-4 text-[11px] leading-relaxed text-faint">
{%- if cookiecutter.use_postgres == "yes" %}
          <div className="pb-3">
            {token === null ? (
              <Link to="/login" className="text-[12px] font-medium text-brand hover:underline">
                Sign in
              </Link>
            ) : (
              <button
                type="button"
                onClick={onSignOut}
                className="flex items-center gap-2 text-[12px] font-medium text-nav hover:text-ink"
              >
                <SignOut size={14} />
                Sign out
              </button>
            )}
          </div>
{%- endif %}
          <div className="border-t border-hairline pt-3">
            API
            <span className="ml-1 font-mono text-mute">
              {import.meta.env.VITE_API_URL ?? "http://localhost:8000"}
            </span>
          </div>
        </div>
      </aside>

      <main className="h-dvh flex-1 overflow-y-auto">
        <ErrorBoundary fallback={pageFallback}>
          <Outlet />
        </ErrorBoundary>
      </main>
    </div>
  );
}
