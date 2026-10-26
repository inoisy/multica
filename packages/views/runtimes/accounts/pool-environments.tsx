"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import type { Pool } from "@multica/core/types";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

/** Where an environment of the current workspace opens; null for one that has no runtime here (another workspace, not registered yet). */
export function useEnvironmentHref() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  return (profileId: string): string | null => {
    const runtime = runtimes.find((candidate) => candidate.profile_id === profileId);
    return runtime ? paths.runtimeSettings(runtime.id, runtime.id) : null;
  };
}

/**
 * Every environment that spends the pool, as links where this workspace can
 * open them. A pool granted to several workspaces or machines names all of
 * them; none is picked on the reader's behalf.
 */
export function PoolEnvironmentLinks({ pool }: { pool: Pool }) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const hrefOf = useEnvironmentHref();
  const workspaces = useQuery(workspaceListOptions()).data ?? [];
  if (!pool.environments.length) {
    return <span className="text-muted-foreground">{t(($) => $.accounts.pool_no_environments)}</span>;
  }
  return (
    <ul className="flex flex-wrap gap-x-3 gap-y-1" aria-label={t(($) => $.accounts.pool_environments)}>
      {pool.environments.map((env) => {
        const href = env.workspace_id === wsId ? hrefOf(env.runtime_profile_id) : null;
        const workspace = env.workspace_id === wsId ? null : workspaces.find((ws) => ws.id === env.workspace_id);
        return (
          <li key={env.runtime_profile_id} className="min-w-0 break-words">
            {href ? (
              <AppLink href={href} className="font-medium underline-offset-2 hover:underline">
                {t(($) => $.accounts.open_environment, { name: env.display_name })}
              </AppLink>
            ) : (
              <span>{env.display_name}</span>
            )}
            {workspace ? <span className="text-muted-foreground"> · {workspace.name}</span> : null}
          </li>
        );
      })}
    </ul>
  );
}
