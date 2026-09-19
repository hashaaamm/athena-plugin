import { createRootRoute, createRoute, createRouter, Outlet } from "@tanstack/react-router";

import { AppShell } from "@/components/app-shell";
import { NotFoundScreen, RootErrorScreen } from "@/components/status-screens";
import { DashboardPage } from "@/routes/dashboard";
{%- if cookiecutter.use_postgres == "yes" %}
import { ItemsPage } from "@/routes/items";
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

/**
 * Pathless layout route. Everything under it renders inside the sidebar, and it is where a
 * `beforeLoad` auth guard goes when this app grows one — once, for every page at the same time.
 */
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  component: AppShell,
});

const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  component: DashboardPage,
});
{%- if cookiecutter.use_postgres == "yes" %}

const itemsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/items",
  component: ItemsPage,
});
{%- endif %}

const routeTree = rootRoute.addChildren([
  appRoute.addChildren([
    dashboardRoute,
{%- if cookiecutter.use_postgres == "yes" %}
    itemsRoute,
{%- endif %}
  ]),
]);

export const router = createRouter({
  routeTree,
  // An unknown URL renders a chrome-less 404; an error thrown above the shell — or before it
  // mounts — renders a chrome-less error screen. Errors inside a page are caught by the
  // ErrorBoundary in AppShell instead, so the navigation survives them.
  defaultNotFoundComponent: NotFoundScreen,
  defaultErrorComponent: RootErrorScreen,
});

// Registers the instance globally so `<Link to="...">` is checked against the real route tree.
// Without this block every route string is just a string, and a typo is a runtime 404.
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
