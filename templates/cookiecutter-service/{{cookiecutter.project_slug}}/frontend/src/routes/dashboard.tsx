import {
  CheckCircle,
  Cube,
{%- if cookiecutter.use_postgres == "yes" %}
  Database,
{%- endif %}
  WarningCircle,
} from "@phosphor-icons/react";

import { StatCard } from "@/components/stat-card";
import { Spinner } from "@/components/ui/spinner";
import { useReadiness } from "@/lib/api/health";

/**
 * The first screen. It exists to answer one question on arrival — is the backend there — and to
 * be deleted. Replace it with whatever this product's home page actually is.
 */
export function DashboardPage() {
  const readiness = useReadiness();

  const healthy = readiness.data?.status === "ok";

  return (
    <div className="mx-auto max-w-[1080px] px-10 pb-[60px] pt-8">
      <h1 className="text-[26px] font-bold tracking-[-0.03em] text-ink">Dashboard</h1>
      <p className="mt-1 text-[13.5px] text-subtext">
        A generated starting point. Everything on this page is an example of a pattern, not a
        feature worth keeping.
      </p>

      <div className="mt-7 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <StatCard
          label="Backend"
          value={readiness.isError ? "Unreachable" : healthy ? "Ready" : "Degraded"}
          Icon={healthy ? CheckCircle : WarningCircle}
          caption={readiness.isError ? "No answer from the API" : "GET /health/ready"}
          tone={healthy ? "success" : "warning"}
          loading={readiness.isPending}
        />
        <StatCard
          label="Version"
          value={readiness.data?.version ?? "—"}
          Icon={Cube}
          caption="The build answering right now"
          loading={readiness.isPending}
        />
{%- if cookiecutter.use_postgres == "yes" %}
        <StatCard
          label="Database"
          value={readiness.data?.database === "ok" ? "Connected" : "Unavailable"}
          Icon={Database}
          caption="Checked on every readiness probe"
          tone={readiness.data?.database === "ok" ? "success" : "warning"}
          loading={readiness.isPending}
        />
{%- endif %}
      </div>

      <div className="mt-6 rounded-2xl border border-hairline bg-surface p-[26px]">
        <h2 className="text-[15px] font-semibold text-ink">What is already wired up</h2>
        <ul className="mt-3 space-y-2 text-[13.5px] leading-relaxed text-body">
          <li>
            A typed API client generated from the backend&rsquo;s own OpenAPI document. Run{" "}
            <code className="rounded bg-subtle px-1 py-[1px] font-mono text-[12.5px]">
              just gen-api
            </code>{" "}
            after every API change; a removed field becomes a compile error, not a blank cell.
          </li>
          <li>
            TanStack Query for every read and write, with the cache keys in one object per
            resource.
          </li>
          <li>
            TanStack Router with the route tree in <code>src/router.tsx</code>, a 404 screen and
            an error screen that keeps this sidebar mounted.
          </li>
          <li>Tailwind v4 and shadcn/ui, with the palette in <code>src/index.css</code>.</li>
          <li>Vitest and Testing Library, with the suite in <code>just test</code>.</li>
        </ul>
        {readiness.isFetching && (
          <div className="mt-4 flex items-center gap-2 text-[12.5px] text-mute">
            <Spinner size={13} />
            Checking the backend…
          </div>
        )}
      </div>
    </div>
  );
}
