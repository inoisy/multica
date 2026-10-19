"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Layers, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import type { ModelProfile, ModelProfileChange, ModelProfileInput } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes";
import {
  compactModelRows,
  modelProfileListOptions,
  modelRowLabel,
  toModelOptionInputs,
  useCreateModelProfile,
  useDeleteModelProfile,
  usePreviewModelProfile,
  useUpdateModelProfile,
} from "@multica/core/model-profiles";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { cn } from "@multica/ui/lib/utils";
import { CollectionPageHeader } from "../layout/collection-page";
import { PAGE_GUTTER } from "../layout/page-header";
import { AppLink } from "../navigation";
import { useT } from "../i18n";
import { ModelRowsEditor } from "./model-rows-editor";

const WAIT = "wait";
const EMPTY = "empty";

interface EditorState {
  id: string | null;
  input: ModelProfileInput;
  fromAgent: string;
}

function emptyInput(): ModelProfileInput {
  return { name: "", description: "", when_exhausted_profile_id: null, options: [] };
}

function inputOf(profile: ModelProfile): ModelProfileInput {
  return {
    name: profile.name,
    description: profile.description,
    when_exhausted_profile_id: profile.when_exhausted_profile_id,
    options: toModelOptionInputs(profile.options),
  };
}

/**
 * Profiles of the workspace: ordered model lists that agents and issues
 * reference. An edit reaches every referencing agent and issue from its next
 * run, so saving an existing profile first shows whose model would change.
 */
export function ModelProfilesPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { role, userId } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";
  const profiles = useQuery(modelProfileListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const create = useCreateModelProfile(wsId);
  const update = useUpdateModelProfile(wsId);
  const remove = useDeleteModelProfile(wsId);
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [changes, setChanges] = useState<ModelProfileChange[] | null>(null);
  const previewMutation = usePreviewModelProfile();
  const previewing = previewMutation.isPending;

  const list = profiles.data ?? [];
  const nameOf = (id: string | null) => list.find((p) => p.id === id)?.name ?? "";
  const busy = create.isPending || update.isPending || previewing;

  const open = (state: EditorState) => {
    setChanges(null);
    setEditor(state);
  };
  const patch = (change: Partial<ModelProfileInput>) => {
    if (!editor) return;
    setChanges(null);
    setEditor({ ...editor, input: { ...editor.input, ...change } });
  };
  const fail = (error: unknown) => toast.error(error instanceof Error ? error.message : String(error));

  const preview = async () => {
    if (!editor?.id) return;
    try {
      setChanges(await previewMutation.mutateAsync({ id: editor.id, input: editor.input }));
    } catch (error) {
      fail(error);
    }
  };
  const save = async () => {
    if (!editor) return;
    try {
      if (editor.id) {
        await update.mutateAsync({ id: editor.id, input: editor.input });
      } else {
        const { options, ...rest } = editor.input;
        await create.mutateAsync(editor.fromAgent ? { ...rest, from_agent_id: editor.fromAgent } : { ...rest, options });
      }
      toast.success(t(($) => $.model_profiles.saved));
      setEditor(null);
    } catch (error) {
      fail(error);
    }
  };
  const del = async (profile: ModelProfile) => {
    try {
      await remove.mutateAsync(profile.id);
      toast.success(t(($) => $.model_profiles.deleted, { name: profile.name }));
    } catch (error) {
      fail(error);
    }
  };

  const exhaustedItems = [
    { value: WAIT, label: t(($) => $.model_profiles.exhausted_wait) },
    ...list.filter((p) => p.id !== editor?.id).map((p) => ({ value: p.id, label: p.name })),
  ];
  const agentItems = [
    { value: EMPTY, label: t(($) => $.model_profiles.from_scratch) },
    ...agents.map((a) => ({ value: a.id, label: a.name })),
  ];
  // Editing an existing profile needs the preview first: the save reaches
  // every agent and issue on it.
  const needsPreview = Boolean(editor?.id) && changes === null;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Layers}
        title={t(($) => $.model_profiles.title)}
        description={t(($) => $.model_profiles.description)}
        actions={
          canManage ? (
            <Button size="sm" onClick={() => open({ id: null, input: emptyInput(), fromAgent: "" })}>
              <Plus />
              {t(($) => $.model_profiles.create)}
            </Button>
          ) : null
        }
      />
      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        {profiles.isError ? (
          <p className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">
            {t(($) => $.model_profiles.load_failed)}
          </p>
        ) : null}
        {profiles.data && list.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.model_profiles.empty)}</p>
        ) : null}
        {list.length > 0 ? (
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full text-body" data-testid="model-profiles-table">
              <thead className="text-left text-caption text-muted-foreground">
                <tr className="border-b">
                  <th className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_name)}</th>
                  <th className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_models)}</th>
                  <th className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_exhausted)}</th>
                  <th className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_refs)}</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {list.map((profile) => (
                  <tr key={profile.id} className="border-b align-top last:border-b-0">
                    <td className="px-3 py-2">
                      <div className="font-medium">{profile.name}</div>
                      {profile.description ? (
                        <div className="text-caption text-muted-foreground">{profile.description}</div>
                      ) : null}
                    </td>
                    <td className="px-3 py-2 font-mono text-caption">{compactModelRows(profile.options) || "—"}</td>
                    <td className="px-3 py-2 text-caption">
                      {profile.when_exhausted_profile_id ? nameOf(profile.when_exhausted_profile_id) : t(($) => $.model_profiles.exhausted_wait)}
                    </td>
                    <td className="space-y-1 px-3 py-2 text-caption">
                      <div>
                        {t(($) => $.model_profiles.refs_agents)}:{" "}
                        {profile.agents.length
                          ? profile.agents.map((a, i) => (
                              <span key={a.id}>
                                {i ? ", " : ""}
                                <AppLink href={paths.agentDetail(a.id)} className="hover:underline">{a.name}</AppLink>
                              </span>
                            ))
                          : "—"}
                      </div>
                      <div>
                        {t(($) => $.model_profiles.refs_issues)}:{" "}
                        {profile.open_issues.length
                          ? profile.open_issues.map((issue, i) => (
                              <span key={issue.id}>
                                {i ? ", " : ""}
                                <AppLink href={paths.issueDetail(issue.id)} className="hover:underline">{issue.identifier}</AppLink>
                              </span>
                            ))
                          : "—"}
                      </div>
                      {profile.own_list_agents.length ? (
                        <div className="text-muted-foreground">
                          {t(($) => $.model_profiles.refs_own)}: {profile.own_list_agents.map((a) => a.name).join(", ")}
                        </div>
                      ) : null}
                    </td>
                    <td className="px-3 py-2">
                      {canManage ? (
                        <div className="flex justify-end gap-1">
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            aria-label={t(($) => $.model_profiles.edit, { name: profile.name })}
                            onClick={() => open({ id: profile.id, input: inputOf(profile), fromAgent: "" })}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            aria-label={t(($) => $.model_profiles.delete, { name: profile.name })}
                            disabled={remove.isPending}
                            onClick={() => void del(profile)}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
      </div>

      <Dialog open={editor !== null} onOpenChange={(next) => !next && !busy && setEditor(null)}>
        {editor ? (
          <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>
                {editor.id ? t(($) => $.model_profiles.edit_title) : t(($) => $.model_profiles.create_title)}
              </DialogTitle>
              <DialogDescription>
                {editor.id ? t(($) => $.model_profiles.edit_description) : t(($) => $.model_profiles.create_description)}
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-3">
              <label className="block space-y-1 text-caption">
                <span>{t(($) => $.model_profiles.name)}</span>
                <Input value={editor.input.name} maxLength={100} disabled={busy} onChange={(e) => patch({ name: e.target.value })} />
              </label>
              <label className="block space-y-1 text-caption">
                <span>{t(($) => $.model_profiles.description_label)}</span>
                <Input value={editor.input.description} maxLength={500} disabled={busy} onChange={(e) => patch({ description: e.target.value })} />
              </label>
              <div className="space-y-1 text-caption">
                <span>{t(($) => $.model_profiles.col_exhausted)}</span>
                <Select
                  items={exhaustedItems}
                  value={editor.input.when_exhausted_profile_id ?? WAIT}
                  disabled={busy}
                  onValueChange={(value) => value && patch({ when_exhausted_profile_id: value === WAIT ? null : value })}
                >
                  <SelectTrigger className="w-full" aria-label={t(($) => $.model_profiles.col_exhausted)}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {exhaustedItems.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              {!editor.id ? (
                <div className="space-y-1 text-caption">
                  <span>{t(($) => $.model_profiles.source)}</span>
                  <Select
                    items={agentItems}
                    value={editor.fromAgent || EMPTY}
                    disabled={busy}
                    onValueChange={(value) => value && setEditor({ ...editor, fromAgent: value === EMPTY ? "" : value })}
                  >
                    <SelectTrigger className="w-full" aria-label={t(($) => $.model_profiles.source)}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {agentItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              ) : null}
              {editor.fromAgent ? null : (
                <ModelRowsEditor
                  rows={editor.input.options}
                  onChange={(options) => patch({ options })}
                  runtimes={runtimes}
                  members={members}
                  currentUserId={userId}
                  disabled={busy}
                />
              )}
              {changes !== null ? (
                <div className="rounded-md border px-3 py-2 text-caption" data-testid="model-profile-preview">
                  {changes.length === 0 ? (
                    <p>{t(($) => $.model_profiles.preview_none)}</p>
                  ) : (
                    <>
                      <p className="mb-1 font-medium">{t(($) => $.model_profiles.preview_title, { count: changes.length })}</p>
                      <ul className="space-y-0.5">
                        {changes.map((c) => (
                          <li key={`${c.kind}-${c.ref.id}`} className="flex flex-wrap gap-x-2">
                            <Badge variant="outline">{c.ref.identifier || c.ref.name}</Badge>
                            <span className="text-muted-foreground">
                              {c.before ? modelRowLabel({ runtime_name: runtimes.find((r) => r.id === c.before?.runtime_id)?.name ?? "", model: c.before.model, thinking_level: c.before.thinking_level }) : "—"}
                              {" → "}
                              {c.after ? modelRowLabel({ runtime_name: runtimes.find((r) => r.id === c.after?.runtime_id)?.name ?? "", model: c.after.model, thinking_level: c.after.thinking_level }) : t(($) => $.model_profiles.preview_waits)}
                            </span>
                          </li>
                        ))}
                      </ul>
                    </>
                  )}
                </div>
              ) : null}
            </div>
            <DialogFooter>
              <Button variant="outline" disabled={busy} onClick={() => setEditor(null)}>
                {t(($) => $.models.cancel)}
              </Button>
              {needsPreview ? (
                <Button disabled={busy || editor.input.name.trim() === ""} onClick={() => void preview()}>
                  {t(($) => $.model_profiles.preview)}
                </Button>
              ) : (
                <Button disabled={busy || editor.input.name.trim() === ""} onClick={() => void save()}>
                  {t(($) => $.models.save)}
                </Button>
              )}
            </DialogFooter>
          </DialogContent>
        ) : null}
      </Dialog>
    </div>
  );
}
