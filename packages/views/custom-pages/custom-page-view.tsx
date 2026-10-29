"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AppWindow, ExternalLink, Settings2 } from "@multica/ui/icons";
import { useAuthStore } from "@multica/core/auth";
import { useCurrentWorkspace, useWorkspacePaths, useWorkspaceSlug } from "@multica/core/paths";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { AppLink, useNavigation } from "../navigation";
import { CollectionPageHeader } from "../layout/collection-page";
import { PAGE_GUTTER } from "../layout/page-header";
import { customPagesOptions } from "./custom-pages-api";

/**
 * Workspace page with a postMessage bridge. Only messages from the iframe
 * window and its expected origin may navigate or update the title.
 */
export function CustomPageView({ pageId }: { pageId: string }) {
  const { t } = useT("layout");
  const wsSlug = useWorkspaceSlug();
  const workspace = useCurrentWorkspace();
  const user = useAuthStore((s) => s.user);
  const { resolvedTheme } = useTheme();
  const { push } = useNavigation();
  const p = useWorkspacePaths();
  const query = useQuery(customPagesOptions(wsSlug));
  const page = query.data?.pages.find((x) => x.id === pageId);
  const frame = useRef<HTMLIFrameElement>(null);
  const [subtitle, setSubtitle] = useState<string | null>(null);

  const origin = useMemo(() => {
    try {
      return page ? new URL(page.url, window.location.origin).origin : null;
    } catch {
      return null;
    }
  }, [page]);

  const context = useMemo(
    () => ({
      theme: resolvedTheme === "dark" ? "dark" : "light",
      workspace: workspace ? { id: workspace.id, slug: workspace.slug, name: workspace.name } : null,
      user: user ? { id: user.id, name: user.name } : null,
      page: page ? { id: page.id, title: page.title } : null,
      locale: typeof document !== "undefined" ? document.documentElement.lang || "ru" : "ru",
    }),
    [resolvedTheme, workspace, user, page],
  );

  const post = useCallback(() => {
    if (origin) frame.current?.contentWindow?.postMessage({ source: "multica", type: "context", context }, origin);
  }, [context, origin]);

  useEffect(post, [post]);
  useEffect(() => setSubtitle(null), [pageId]);

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      if (!frame.current || e.source !== frame.current.contentWindow || e.origin !== origin) return;
      const m = e.data as { source?: string; type?: string; path?: unknown; url?: unknown; title?: unknown };
      if (!m || m.source !== "multica-page") return;
      if (m.type === "ready") post();
      else if (m.type === "navigate" && typeof m.path === "string" && /^\/(?![/\\])/.test(m.path)) push(m.path);
      else if (m.type === "open" && typeof m.url === "string" && m.url.startsWith("https://"))
        window.open(m.url, "_blank", "noopener,noreferrer");
      else if (m.type === "title") setSubtitle(String(m.title ?? "").slice(0, 120) || null);
    }
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [origin, post, push]);

  const actions = page ? (
    <div className="flex items-center gap-2">
      {query.data?.can_edit ? (
        <Button variant="ghost" size="sm" nativeButton={false} render={<AppLink href={p.pages()} />}>
          <Settings2 /> {t(($) => $.custom_pages.configure)}
        </Button>
      ) : null}
      <Button variant="outline" size="sm" nativeButton={false} render={<a href={page.url} target="_blank" rel="noopener noreferrer" />}>
        <ExternalLink /> {t(($) => $.custom_pages.tab)}
      </Button>
    </div>
  ) : null;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={AppWindow}
        title={page?.title ?? t(($) => $.custom_pages.page)}
        description={subtitle ?? (page?.description || undefined)}
        actions={actions}
      />
      {query.isLoading ? (
        <p className={cn("py-4 text-caption text-muted-foreground", PAGE_GUTTER)}>{t(($) => $.custom_pages.loading)}</p>
      ) : query.error ? (
        <p className={cn("py-4 text-caption text-destructive", PAGE_GUTTER)}>
          {t(($) => $.custom_pages.load_failed, { error: query.error.message })}
        </p>
      ) : !page ? (
        <p className={cn("py-4 text-caption text-muted-foreground", PAGE_GUTTER)}>
          {t(($) => $.custom_pages.not_found)} <AppLink href={p.pages()} className="underline">{t(($) => $.custom_pages.all)}</AppLink>
        </p>
      ) : page.mode === "tab" ? (
        <div className={cn("py-6", PAGE_GUTTER)}>
          <p className="mb-3 text-muted-foreground">{t(($) => $.custom_pages.opens_in_tab)}</p>
          <Button nativeButton={false} render={<a href={page.url} target="_blank" rel="noopener noreferrer" />}>
            <ExternalLink /> {t(($) => $.custom_pages.open_named, { title: page.title })}
          </Button>
        </div>
      ) : (
        <iframe
          ref={frame}
          key={page.id}
          src={page.url}
          title={page.title}
          onLoad={post}
          className="min-h-0 w-full flex-1 border-0 border-t bg-background"
          referrerPolicy="same-origin"
        />
      )}
    </div>
  );
}
