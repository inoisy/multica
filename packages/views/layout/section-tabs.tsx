"use client";

import { useWorkspacePaths, type WorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { AppLink } from "../navigation";

/**
 * Pages the fork adds sit under an upstream section instead of the sidebar:
 * model profiles under Agents, the epic stage
 * board under Issues. Each page of a section shows the same switcher in its
 * header, and the sidebar keeps the section's own entry active.
 */
export type Section = "issues" | "agents";

type SectionPage = "issues" | "conveyor" | "agents" | "modelProfiles";

const SECTION_PAGES: Record<Section, readonly SectionPage[]> = {
  issues: ["issues", "conveyor"],
  agents: ["agents", "modelProfiles"],
};

/**
 * The sidebar entry a fork page belongs to: `/profiles` keeps Agents
 * active and `/conveyor` Issues.
 */
export function sectionHrefsFor(p: WorkspacePaths, href: string): string[] {
  for (const pages of Object.values(SECTION_PAGES)) {
    const [owner, ...children] = pages;
    if (owner && p[owner]() === href) return [href, ...children.map((page) => p[page]())];
  }
  return [href];
}

/**
 * Switcher between the pages of a section, in place of the page title. The
 * current page keeps an h1 for assistive tech; the others are links.
 */
export function SectionTabs({ section, current, count }: { section: Section; current: SectionPage; count?: number }) {
  const { t } = useT("layout");
  const p = useWorkspacePaths();
  const label = (page: SectionPage) => t(($) => $.section_tabs[page]);
  // Long names lose to the header's actions at phone width; short ones fit.
  const text = (page: SectionPage) => {
    const short = page === "modelProfiles" ? t(($) => $.section_tabs_short[page]) : null;
    return short ? (
      <>
        <span className="sm:hidden">{short}</span>
        <span className="hidden sm:inline">{label(page)}</span>
      </>
    ) : (
      label(page)
    );
  };
  return (
    <nav aria-label={label(SECTION_PAGES[section][0]!)} className="min-w-0 shrink-0">
      <div className="inline-flex max-w-full items-center gap-0.5 overflow-x-auto rounded-lg bg-muted p-0.5">
        {SECTION_PAGES[section].map((page) => {
          const active = page === current;
          const className = cn(
            "inline-flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 text-label font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring",
            active ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
          );
          if (active) {
            return (
              <h1 key={page} aria-current="page" aria-label={label(page)} className={className}>
                {text(page)}
                {typeof count === "number" && count > 0 ? (
                  <span className="font-mono text-caption font-normal tabular-nums text-muted-foreground">{count}</span>
                ) : null}
              </h1>
            );
          }
          return (
            <AppLink key={page} href={p[page]()} className={className} aria-label={label(page)}>
              {text(page)}
            </AppLink>
          );
        })}
      </div>
    </nav>
  );
}
