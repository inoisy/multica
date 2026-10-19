"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AppWindow, ArrowDown, ArrowUp, ExternalLink, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useWorkspacePaths, useWorkspaceSlug } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../navigation";
import { CollectionPageHeader } from "../layout/collection-page";
import { PAGE_GUTTER } from "../layout/page-header";
import { customPagesKey, customPagesOptions, PagesError, saveCustomPages, type CustomPage } from "./custom-pages-api";

type Draft = Pick<CustomPage, "title" | "url" | "mode" | "description"> & { id?: string };

const EMPTY: Draft = { title: "", url: "", mode: "embed", description: "" };

/** Список своих страниц workspace; владелец и админы правят его здесь. */
export function CustomPagesPage() {
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
      toast.success("Страницы сохранены");
    } catch (err) {
      toast.error(err instanceof PagesError ? err.message : "Не удалось сохранить");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={AppWindow}
        title="Страницы"
        count={pages.length}
        description="Свои страницы рабочего пространства — пункты в сайдбаре. Открываются внутри Multica или в новой вкладке."
        actions={
          canEdit ? (
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" onClick={() => setDraft([...rows, { ...EMPTY }])}>
                <Plus /> Добавить
              </Button>
              <Button size="sm" disabled={!dirty || saving} onClick={save}>
                {saving ? "Сохраняю…" : "Сохранить"}
              </Button>
            </div>
          ) : null
        }
      />
      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        {query.isLoading ? (
          <p className="text-caption text-muted-foreground">Загрузка…</p>
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
            <p className="text-muted-foreground">Страниц пока нет. Добавляют их владелец и админы.</p>
          )
        ) : rows.length === 0 ? (
          <p className="text-muted-foreground">Страниц пока нет — нажмите «Добавить».</p>
        ) : (
          <div className="space-y-3">
            {rows.map((row, i) => (
              <div key={row.id ?? `new-${i}`} className="grid gap-2 rounded-lg border p-3 md:grid-cols-[1fr_1.4fr_auto]">
                <Input placeholder="Название" value={row.title} maxLength={60}
                  onChange={(e) => set(i, { title: e.target.value })} />
                <Input placeholder="/takes/ или https://…" value={row.url} maxLength={500}
                  onChange={(e) => set(i, { url: e.target.value })} />
                <div className="flex items-center gap-1">
                  <select
                    className="h-8 rounded-md border bg-background px-2 text-sm"
                    value={row.mode}
                    onChange={(e) => set(i, { mode: e.target.value as Draft["mode"] })}
                    aria-label="Как открывать"
                  >
                    <option value="embed">Внутри Multica</option>
                    <option value="tab">Новой вкладкой</option>
                  </select>
                  <Button variant="ghost" size="icon-sm" aria-label="Выше" disabled={i === 0} onClick={() => move(i, -1)}>
                    <ArrowUp />
                  </Button>
                  <Button variant="ghost" size="icon-sm" aria-label="Ниже" disabled={i === rows.length - 1} onClick={() => move(i, 1)}>
                    <ArrowDown />
                  </Button>
                  {row.url ? (
                    <Button variant="ghost" size="icon-sm" aria-label="Открыть"
                      nativeButton={false} render={<a href={row.url} target="_blank" rel="noopener noreferrer" />}>
                      <ExternalLink />
                    </Button>
                  ) : null}
                  <Button variant="ghost" size="icon-sm" aria-label="Удалить"
                    onClick={() => setDraft(rows.filter((_, j) => j !== i))}>
                    <Trash2 />
                  </Button>
                </div>
                <Input className="md:col-span-3" placeholder="Описание (необязательно)" value={row.description}
                  maxLength={200} onChange={(e) => set(i, { description: e.target.value })} />
              </div>
            ))}
            <p className="text-caption text-muted-foreground">
              Адрес — путь на этом сайте (например, <code>/takes/</code>) или https://. Внешние сайты часто не
              открываются во фрейме — для них «Новой вкладкой».
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
