"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Layers, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import type { ModelProfile, ModelProfileChange, ModelProfileInput, ModelProfileKind } from "@multica/core/types";
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
  useAgentPersonalProfile,
  useCreateModelProfile,
  useDeleteModelProfile,
  usePreviewModelProfile,
  useUpdateModelProfile,
} from "@multica/core/model-profiles";
import { Badge } from "@multica/ui/components/ui/badge";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@multica/ui/components/ui/table";
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
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { CollectionPageHeader, CollectionPageHeaderAction } from "../layout/collection-page";
import { SectionTabs } from "../layout/section-tabs";
import { PAGE_GUTTER } from "../layout/page-header";
import { AppLink, useNavigation } from "../navigation";
import { useT } from "../i18n";
import { ModelRowsEditor } from "./model-rows-editor";
import { ProfileDiffBadge } from "./profile-diff";
import { ResetPersonalDialog } from "./reset-personal-dialog";

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

const KINDS: ModelProfileKind[] = ["all", "shared", "personal"];

/** The kind filter lives in the address: a link and a reload restore the same view. */
function parseKind(value: string | null): ModelProfileKind {
  return KINDS.find((kind) => kind === value) ?? "all";
}

/** "Return to shared" for one agent's personal profile, behind a confirmation. */
function ResetPersonalButton({ wsId, agent, baseName }: { wsId: string; agent: { id: string; name: string }; baseName: string }) {
  const { t } = useT("agents");
  const personal = useAgentPersonalProfile(wsId, agent.id);
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="sm" variant="ghost" onClick={() => setOpen(true)}>
        {t(($) => $.models.reset_to_shared)}
        <span className="sr-only">: {agent.name}</span>
      </Button>
      <ResetPersonalDialog
        open={open}
        baseName={baseName}
        busy={personal.reset.isPending}
        onOpenChange={setOpen}
        onConfirm={() =>
          void personal.reset.mutateAsync().then(
            () => {
              toast.success(t(($) => $.models.reset_done));
              setOpen(false);
            },
            (error: unknown) => toast.error(error instanceof Error ? error.message : String(error)),
          )
        }
      />
    </>
  );
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
  const navigation = useNavigation();
  const mobile = useIsMobile();
  const kind = parseKind(navigation.searchParams.get("kind"));
  const profiles = useQuery(modelProfileListOptions(wsId, "shared"));
  const personalProfiles = useQuery(modelProfileListOptions(wsId, "personal"));
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
  const personalList = personalProfiles.data ?? [];
  const showShared = kind !== "personal";
  const showPersonal = kind !== "shared";
  const setKind = (next: ModelProfileKind) => {
    const params = new URLSearchParams(navigation.searchParams);
    if (next === "all") params.delete("kind");
    else params.set("kind", next);
    const query = params.toString();
    navigation.replace(query ? `${navigation.pathname}?${query}` : navigation.pathname);
  };
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
  const renderRefs = (profile: ModelProfile) => (
    <>
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
        {t(($) => $.model_profiles.refs_issues)}: {renderIssues(profile)}
      </div>
      {profile.personal_profiles.length ? (
        <div className="space-y-1 pt-1" data-testid="personal-of-profile">
          <div className="text-muted-foreground">{t(($) => $.model_profiles.refs_personal)}</div>
          {profile.personal_profiles.map((personal) => (
            <div key={personal.profile_id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <AppLink href={paths.agentDetail(personal.agent.id)} className="hover:underline">
                {personal.agent.name}
              </AppLink>
              <ProfileDiffBadge matchesBase={personal.matches_base} diffRows={personal.diff_rows} />
              {canManage ? <ResetPersonalButton wsId={wsId} agent={personal.agent} baseName={profile.name} /> : null}
            </div>
          ))}
        </div>
      ) : null}
    </>
  );
  const renderIssues = (profile: ModelProfile) =>
    profile.open_issues.length
      ? profile.open_issues.map((issue, i) => (
          <span key={issue.id}>
            {i ? ", " : ""}
            <AppLink href={paths.issueDetail(issue.id)} className="hover:underline">{issue.identifier}</AppLink>
          </span>
        ))
      : "—";
  const renderActions = (profile: ModelProfile) =>
    canManage ? (
      <div className="flex shrink-0 justify-end gap-1">
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
    ) : null;
  const renderOwner = (profile: ModelProfile) =>
    profile.owner_agent ? (
      <AppLink href={paths.agentDetail(profile.owner_agent.id)} className="break-words font-medium hover:underline">
        {profile.owner_agent.name}
      </AppLink>
    ) : (
      <span className="font-medium">{profile.name}</span>
    );
  const renderPersonalActions = (profile: ModelProfile) =>
    canManage && profile.owner_agent && profile.based_on ? (
      <div className="flex shrink-0 justify-end">
        <ResetPersonalButton wsId={wsId} agent={profile.owner_agent} baseName={profile.based_on.name} />
      </div>
    ) : null;

  // Editing an existing profile needs the preview first: the save reaches
  // every agent and issue on it.
  const needsPreview = Boolean(editor?.id) && changes === null;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Layers}
        title={t(($) => $.model_profiles.title)}
        tabs={<SectionTabs section="agents" current="modelProfiles" />}
        description={t(($) => $.model_profiles.description)}
        actions={
          canManage ? (
            <CollectionPageHeaderAction
              icon={Plus}
              variant="default"
              label={t(($) => $.model_profiles.create)}
              onClick={() => open({ id: null, input: emptyInput(), fromAgent: "" })}
            />
          ) : null
        }
      />
      <div className={cn("min-h-0 flex-1 space-y-4 overflow-y-auto py-4", PAGE_GUTTER)}>
        <div role="group" aria-label={t(($) => $.model_profiles.kind_label)} className="flex w-fit max-w-full flex-wrap gap-1 rounded-lg bg-muted p-0.5">
          {KINDS.map((value) => (
            <Button
              key={value}
              size="sm"
              variant={kind === value ? "outline" : "ghost"}
              aria-pressed={kind === value}
              onClick={() => setKind(value)}
            >
              {t(($) => $.model_profiles.kind[value])}
            </Button>
          ))}
        </div>
        {profiles.isError ? (
          <p className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">
            {t(($) => $.model_profiles.load_failed)}
          </p>
        ) : null}
        {showShared && profiles.data && list.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.model_profiles.empty)}</p>
        ) : null}
        {showShared && list.length > 0 ? (
          mobile ? (
            <ul className="space-y-3" data-testid="model-profiles-list">
              {list.map((profile) => (
                <li key={profile.id} className="space-y-2 rounded-lg border p-3">
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="break-words font-medium">{profile.name}</div>
                      {profile.description ? <div className="text-caption text-muted-foreground">{profile.description}</div> : null}
                    </div>
                    {renderActions(profile)}
                  </div>
                  <p className="break-words font-mono text-caption">{compactModelRows(profile.options) || "—"}</p>
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.model_profiles.col_exhausted)}:{" "}
                    {profile.when_exhausted_profile_id ? nameOf(profile.when_exhausted_profile_id) : t(($) => $.model_profiles.exhausted_wait)}
                  </p>
                  <div className="space-y-1 text-caption">{renderRefs(profile)}</div>
                </li>
              ))}
            </ul>
          ) : (
            <div className="overflow-x-auto rounded-lg border">
              <Table className="w-full text-body" data-testid="model-profiles-table">
                <TableHeader className="text-left text-caption text-muted-foreground">
                  <TableRow className="border-b">
                    <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_name)}</TableHead>
                    <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_models)}</TableHead>
                    <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_exhausted)}</TableHead>
                    <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_refs)}</TableHead>
                    <TableHead className="px-3 py-2" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {list.map((profile) => (
                    <TableRow key={profile.id} className="border-b align-top last:border-b-0">
                      <TableCell className="px-3 py-2">
                        <div className="font-medium">{profile.name}</div>
                        {profile.description ? <div className="text-caption text-muted-foreground">{profile.description}</div> : null}
                      </TableCell>
                      <TableCell className="px-3 py-2 font-mono text-caption">{compactModelRows(profile.options) || "—"}</TableCell>
                      <TableCell className="px-3 py-2 text-caption">
                        {profile.when_exhausted_profile_id ? nameOf(profile.when_exhausted_profile_id) : t(($) => $.model_profiles.exhausted_wait)}
                      </TableCell>
                      <TableCell className="space-y-1 whitespace-normal px-3 py-2 text-caption">{renderRefs(profile)}</TableCell>
                      <TableCell className="px-3 py-2">{renderActions(profile)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )
        ) : null}
        {showPersonal ? (
          <section className="space-y-2" aria-labelledby="personal-profiles-title">
            <h2 id="personal-profiles-title" className="text-body font-medium">
              {t(($) => $.model_profiles.personal_title)}
              <span className="ml-2 text-caption font-normal text-muted-foreground">{personalList.length}</span>
            </h2>
            {personalProfiles.data && personalList.length === 0 ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.model_profiles.personal_empty)}</p>
            ) : null}
            {personalList.length > 0 ? (
              mobile ? (
                <ul className="space-y-3" data-testid="personal-profiles-list">
                  {personalList.map((profile) => (
                    <li key={profile.id} className="space-y-2 rounded-lg border p-3">
                      <div className="flex items-start justify-between gap-2">
                        <div className="min-w-0">{renderOwner(profile)}</div>
                        {renderPersonalActions(profile)}
                      </div>
                      <div className="flex flex-wrap items-center gap-2 text-caption">
                        <span className="text-muted-foreground">{profile.based_on ? profile.based_on.name : "—"}</span>
                        <ProfileDiffBadge matchesBase={profile.matches_base} diffRows={profile.diff_rows} />
                      </div>
                      <p className="break-words font-mono text-caption">{compactModelRows(profile.options) || "—"}</p>
                      <div className="text-caption">{renderIssues(profile)}</div>
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="overflow-x-auto rounded-lg border">
                  <Table className="w-full text-body" data-testid="personal-profiles-table">
                    <TableHeader className="text-left text-caption text-muted-foreground">
                      <TableRow className="border-b">
                        <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_agent)}</TableHead>
                        <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_base)}</TableHead>
                        <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_models)}</TableHead>
                        <TableHead className="px-3 py-2 font-medium">{t(($) => $.model_profiles.col_refs)}</TableHead>
                        <TableHead className="px-3 py-2" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {personalList.map((profile) => (
                        <TableRow key={profile.id} className="border-b align-top last:border-b-0">
                          <TableCell className="px-3 py-2">{renderOwner(profile)}</TableCell>
                          <TableCell className="space-y-1 px-3 py-2 text-caption">
                            <div>{profile.based_on ? profile.based_on.name : "—"}</div>
                            <ProfileDiffBadge matchesBase={profile.matches_base} diffRows={profile.diff_rows} />
                          </TableCell>
                          <TableCell className="px-3 py-2 font-mono text-caption">{compactModelRows(profile.options) || "—"}</TableCell>
                          <TableCell className="space-y-1 whitespace-normal px-3 py-2 text-caption">{renderIssues(profile)}</TableCell>
                          <TableCell className="px-3 py-2">{renderPersonalActions(profile)}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )
            ) : null}
          </section>
        ) : null}
      </div>

      <Dialog open={editor !== null} onOpenChange={(next) => !next && !busy && setEditor(null)}>
        {editor ? (
          <DialogContent className="max-h-[90vh] grid-cols-[minmax(0,1fr)] overflow-y-auto sm:max-w-2xl">
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
                  <SelectTrigger className="w-full pr-7" aria-label={t(($) => $.model_profiles.col_exhausted)}>
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
                    <SelectTrigger className="w-full pr-7" aria-label={t(($) => $.model_profiles.source)}>
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
