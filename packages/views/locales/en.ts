import type { LocaleResources } from "@multica/core/i18n";
import { withForkStrings } from "./fork";
import forkAgents from "./en/fork/agents.json";
import forkCommon from "./en/fork/common.json";
import forkEditor from "./en/fork/editor.json";
import forkIssues from "./en/fork/issues.json";
import forkLayout from "./en/fork/layout.json";
import forkRuntimes from "./en/fork/runtimes.json";
import forkSettings from "./en/fork/settings.json";
import forkUsage from "./en/fork/usage.json";
import enDocs from "./en/docs.json";
import enCommon from "./en/common.json";
import enAuth from "./en/auth.json";
import enSettings from "./en/settings.json";
import enIssues from "./en/issues.json";
import enAgents from "./en/agents.json";
import enEditor from "./en/editor.json";
import enOnboarding from "./en/onboarding.json";
import enInvite from "./en/invite.json";
import enLabels from "./en/labels.json";
import enMembers from "./en/members.json";
import enMyIssues from "./en/my-issues.json";
import enSearch from "./en/search.json";
import enInbox from "./en/inbox.json";
import enWorkspace from "./en/workspace.json";
import enProjects from "./en/projects.json";
import enAutopilots from "./en/autopilots.json";
import enSkills from "./en/skills.json";
import enChat from "./en/chat.json";
import enModals from "./en/modals.json";
import enRuntimes from "./en/runtimes.json";
import enLayout from "./en/layout.json";
import enUsage from "./en/usage.json";
import enUi from "./en/ui.json";
import enSquads from "./en/squads.json";
import enBilling from "./en/billing.json";

const resources: LocaleResources = {
  docs: enDocs,
  common: enCommon,
  auth: enAuth,
  settings: enSettings,
  issues: enIssues,
  agents: enAgents,
  editor: enEditor,
  onboarding: enOnboarding,
  invite: enInvite,
  labels: enLabels,
  members: enMembers,
  "my-issues": enMyIssues,
  search: enSearch,
  inbox: enInbox,
  workspace: enWorkspace,
  projects: enProjects,
  autopilots: enAutopilots,
  skills: enSkills,
  chat: enChat,
  modals: enModals,
  runtimes: enRuntimes,
  layout: enLayout,
  usage: enUsage,
  ui: enUi,
  squads: enSquads,
  billing: enBilling,
};

export default withForkStrings(resources, { agents: forkAgents, common: forkCommon, editor: forkEditor, issues: forkIssues, layout: forkLayout, runtimes: forkRuntimes, settings: forkSettings, usage: forkUsage });
