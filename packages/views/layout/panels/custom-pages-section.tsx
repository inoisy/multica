"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspacePaths, useWorkspaceSlug } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { customPagesOptions } from "../../custom-pages";
import { useT } from "../../i18n";
import { routeIconForPath } from "../route-icon-components";
import {
  PANEL_ROW_CLASS,
  PanelLink,
  PanelList,
  PanelSection,
  isPathWithin,
  panelRowStateClass,
} from "../panel-nav";

/** Workspace links from Settings → Pages; tab-mode pages open a new browser tab. */
export function CustomPagesSection({ pathname }: { pathname: string }) {
  const { t } = useT("layout");
  const p = useWorkspacePaths();
  const { data } = useQuery(customPagesOptions(useWorkspaceSlug()));
  if (!data?.pages.length) return null;
  const Icon = routeIconForPath(p.pages());
  return (
    <PanelSection label={t(($) => $.nav.pages)}>
      <PanelList>
        {data.pages.map((page) => {
          const href = p.customPage(page.id);
          if (page.mode === "tab") {
            return (
              <li key={page.id}>
                <a
                  href={page.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  title={page.description || undefined}
                  className={cn(PANEL_ROW_CLASS, panelRowStateClass(false), "[&_svg]:size-4 [&_svg]:shrink-0")}
                >
                  <Icon />
                  <span className="min-w-0 flex-1 truncate">{page.title}</span>
                </a>
              </li>
            );
          }
          return (
            <PanelLink
              key={page.id}
              href={href}
              icon={<Icon />}
              label={page.title}
              active={isPathWithin(pathname, href)}
            />
          );
        })}
      </PanelList>
    </PanelSection>
  );
}
