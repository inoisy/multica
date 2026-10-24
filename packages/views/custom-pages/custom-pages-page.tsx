"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AppWindow, ArrowDown, ArrowUp, ExternalLink, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useWorkspacePaths, useWorkspaceSlug } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { SimpleSelect } from "@multica/ui/components/common/simple-select";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { AppLink } from "../navigation";
import { CollectionPageHeader } from "../layout/collection-page";
import { PAGE_GUTTER } from "../layout/page-header";
import { SettingsTab } from "../settings/components/settings-layout";
import { customPagesKey, customPagesOptions, PagesError, saveCustomPages, type CustomPage } from "./custom-pages-api";

type Draft = Pick<CustomPage, "title" | "url" | "mode" | "description"> & { id?: string };

const EMPTY: Draft = { title: "", url: "", mode: "embed", description: "" };

type CustomPagesEditorState = ReturnType<typeof useCustomPagesEditor>;

/** Draft, save and reorder of the workspace's pages. */
function useCustomPagesEditor() {
  const { t } = useT("layout");
  const wsSlug = useWorkspaceSlug();
  const p = useWorkspacePaths();
  const queryClient = useQueryClient();
  const query = useQuery(customPagesOptions(wsSlug));
  const [draft, setDraft] = useState<Draft[] | null>(null);
  const [saving, setSaving] = useState(false);
  const canEdit = query.data?.can_edit ?? false;
  const pages = query.data?.pages ?? [];

  useEffect(() => {
    if (query.data && draft === null) setDraft(query.data.pages.map((x) => ({ ...x })));
  }, [query.data, draft]);

  const rows = draft ?? [];
  const dirty = JSON.stringify(rows) !== JSON.stringify(pages);
  const set = (i: number, patch: Partial<Draft>) =>
    setDraft(rows.map((row, j) => (j === i ? { ...row, ...patch } : row)));
  const move = (i: number, d: number) => {
    const next = [...rows];
    const [row] = next.splice(i, 1);
    next.splice(i + d, 0, row!);
    setDraft(next);
  };

  async function save() {
    setSaving(true);
    try {
      const data = await saveCustomPages(wsSlug, rows);
      queryClient.setQueryData(customPagesKey(wsSlug), data);
      setDraft(data.pages.map((x) => ({ ...x })));
      toast.success(t(($) => $.custom_pages.saved));
    } catch (err) {
      toast.error(err instanceof PagesError ? err.message : t(($) => $.custom_pages.save_failed));
    } finally {
      setSaving(false);
    }
  }

  return { t, p, query, canEdit, pages, rows, dirty, saving, set, move, save, setDraft };
}

function CustomPagesActions({ state }: { state: CustomPagesEditorState }) {
  const { t, canEdit, rows, dirty, saving, save, setDraft } = state;
  if (!canEdit) return null;
  return (
    <div className="flex items-center gap-2">
      <Button variant="outline" size="sm" onClick={() => setDraft([...rows, { ...EMPTY }])}>
        <Plus /> {t(($) => $.custom_pages.add)}
      </Button>
      <Button size="sm" disabled={!dirty || saving} onClick={save}>
        {saving ? t(($) => $.custom_pages.saving) : t(($) => $.custom_pages.save)}
      </Button>
    </div>
  );
}

function CustomPagesBody({ state }: { state: CustomPagesEditorState }) {
  const { t, p, query, canEdit, pages, rows, set, move, setDraft } = state;
  return (
    <>
      {query.isLoading ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.custom_pages.loading)}</p>
      ) : query.error ? (
        <p className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">
          {query.error.message}
        </p>
      ) : !canEdit ? (
        pages.length ? (
          <ul className="space-y-2">
            {pages.map((page) => (
              <li key={page.id} className="rounded-lg border px-3 py-2">
                <AppLink href={p.customPage(page.id)} className="font-medium hover:underline">
                  {page.title}
                </AppLink>
                {page.description ? <p className="text-caption text-muted-foreground">{page.description}</p> : null}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground">{t(($) => $.custom_pages.empty_readonly)}</p>
        )
      ) : rows.length === 0 ? (
        <p className="text-muted-foreground">{t(($) => $.custom_pages.empty_editable)}</p>
      ) : (
        <div className="space-y-3">
          {rows.map((row, i) => (
            <div key={row.id ?? `new-${i}`} className="grid gap-2 rounded-lg border p-3 md:grid-cols-[1fr_1.4fr_auto]">
              <Input aria-label={t(($) => $.custom_pages.name)} placeholder={t(($) => $.custom_pages.name)} value={row.title} maxLength={60}
                onChange={(e) => set(i, { title: e.target.value })} />
              <Input aria-label={t(($) => $.custom_pages.url)} placeholder={t(($) => $.custom_pages.url)} value={row.url} maxLength={500}
                onChange={(e) => set(i, { url: e.target.value })} />
              <div className="flex items-center gap-1">
                <SimpleSelect
                  className="h-8 w-44"
                  value={row.mode}
                  onValueChange={(mode) => { if (mode === "embed" || mode === "tab") set(i, { mode }); }}
                  aria-label={t(($) => $.custom_pages.open_mode)}
                  items={[{ value: "embed", label: t(($) => $.custom_pages.embed) }, { value: "tab", label: t(($) => $.custom_pages.tab) }]}
                />
                <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.custom_pages.up)} disabled={i === 0} onClick={() => move(i, -1)}>
                  <ArrowUp />
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.custom_pages.down)} disabled={i === rows.length - 1} onClick={() => move(i, 1)}>
                  <ArrowDown />
                </Button>
                {row.url ? (
                  <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.custom_pages.open)}
                    nativeButton={false} render={<a href={row.url} target="_blank" rel="noopener noreferrer" />}>
                    <ExternalLink />
                  </Button>
                ) : null}
                <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.custom_pages.delete)}
                  onClick={() => setDraft(rows.filter((_, j) => j !== i))}>
                  <Trash2 />
                </Button>
              </div>
              <Input className="md:col-span-3" aria-label={t(($) => $.custom_pages.optional_description)} placeholder={t(($) => $.custom_pages.optional_description)} value={row.description}
                maxLength={200} onChange={(e) => set(i, { description: e.target.value })} />
            </div>
          ))}
          <p className="text-caption text-muted-foreground">
            {t(($) => $.custom_pages.url_hint)}
          </p>
        </div>
      )}
    </>
  );
}

/** Workspace pages managed by their owner and admins. */
export function CustomPagesPage() {
  const state = useCustomPagesEditor();
  const { t, pages } = state;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={AppWindow}
        title={t(($) => $.custom_pages.title)}
        count={pages.length}
        description={t(($) => $.custom_pages.description)}
        actions={<CustomPagesActions state={state} />}
      />
      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        <CustomPagesBody state={state} />
      </div>
    </div>
  );
}

/** The same editor as a Settings tab: Settings → Workspace → Pages. */
export function CustomPagesSettingsTab() {
  const state = useCustomPagesEditor();
  const { t } = state;
  return (
    <SettingsTab
      title={t(($) => $.custom_pages.title)}
      description={t(($) => $.custom_pages.description)}
      scope="workspace"
    >
      <div className="space-y-4">
        {/* Two buttons beside the title would squeeze the description on narrow screens. */}
        <div className="flex justify-end">
          <CustomPagesActions state={state} />
        </div>
        <CustomPagesBody state={state} />
      </div>
    </SettingsTab>
  );
}
