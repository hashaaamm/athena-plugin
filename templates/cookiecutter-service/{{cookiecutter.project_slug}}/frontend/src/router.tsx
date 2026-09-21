import {
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
{%- if cookiecutter.use_postgres == "yes" %}
  redirect,
{%- endif %}
  type RouterHistory,
} from "@tanstack/react-router";

import { AppShell } from "@/components/app-shell";
import { NotFoundScreen, RootErrorScreen } from "@/components/status-screens";
import { DashboardPage } from "@/routes/dashboard";
{%- if cookiecutter.use_postgres == "yes" %}
import { AccountPage } from "@/routes/account";
import { LoginPage } from "@/routes/login";
import { RegisterPage } from "@/routes/register";
import { getToken, LOGIN_PATH } from "@/lib/auth";
{%- endif %}

/**
 * The route tree, declared in code rather than generated from the filesystem.
 *
 * The file-based router is the other supported option and it is a fine one; this is the version
 * you can read top to bottom, and adding a route is adding a `createRoute` plus a line in
 * `addChildren`. Ask Athena for the frontend routing rules before switching.
 */

/** Bare root: each group below provides its own chrome, or none. */
const rootRoute = createRootRoute({ component: () => <Outlet /> });

{%- if cookiecutter.use_postgres == "yes" %}

/**
 * Pathless layout route. Everything under it renders inside the sidebar, and every page under it
 * needs a token — the shell is the guard, so there is no page inside the application a signed-out
 * visitor can reach.
 *
 * `beforeLoad` runs before the component mounts, so an unauthenticated visitor never renders a
 * flash of a page they are not allowed to see, and the queries on that page never fire.
 *
 * This is a UX boundary and not a security one. The bundle is static files served to anyone who
 * asks; what keeps data private is the backend answering 401 without a valid bearer token. Ask
 * Athena for `standard/browser-authentication` before you reason about either.
 *
 * There is no `next` parameter, and that is deliberate rather than unfinished. A `next` taken
 * from the URL and navigated to is an open redirect unless it is validated against the route
 * tree. Add it — validated — when there are enough guarded routes for a redirect-back to be
 * worth having.
 */
{%- else %}

/** Pathless layout route. Everything under it renders inside the sidebar. */
{%- endif %}
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  component: AppShell,
{%- if cookiecutter.use_postgres == "yes" %}
  beforeLoad: () => {
    if (getToken() === null) throw redirect({ to: LOGIN_PATH });
  },
{%- endif %}
});

const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  component: DashboardPage,
});
{%- if cookiecutter.use_postgres == "yes" %}

const accountRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/account",
  component: AccountPage,
});

/**
 * Sign-in and registration hang off the root, not off `appRoute`: a sidebar of links to pages
 * you cannot open is worse than no sidebar. Both bounce a caller who already holds a token,
 * because a sign-in form is not a thing a signed-in user has a use for.
 */
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: LOGIN_PATH,
  beforeLoad: () => {
    if (getToken() !== null) throw redirect({ to: "/" });
  },
  component: LoginPage,
});

const registerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/register",
  beforeLoad: () => {
    if (getToken() !== null) throw redirect({ to: "/" });
  },
  component: RegisterPage,
});
{%- endif %}

const routeTree = rootRoute.addChildren([
{%- if cookiecutter.use_postgres == "yes" %}
  loginRoute,
  registerRoute,
  appRoute.addChildren([dashboardRoute, accountRoute]),
{%- else %}
  appRoute.addChildren([dashboardRoute]),
{%- endif %}
]);

/**
 * A factory, so a test can build a router over an in-memory history instead of the browser's.
 * The exported singleton below is the one the application mounts.
 */
export function createAppRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    history,
    // An unknown URL renders a chrome-less 404; an error thrown above the shell — or before it
    // mounts — renders a chrome-less error screen. Errors inside a page are caught by the
    // ErrorBoundary in AppShell instead, so the navigation survives them.
    defaultNotFoundComponent: NotFoundScreen,
    defaultErrorComponent: RootErrorScreen,
  });
}

export const router = createAppRouter();

// Registers the instance globally so `<Link to="...">` is checked against the real route tree.
// Without this block every route string is just a string, and a typo is a runtime 404.
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
