import type { LocaleResources } from "@multica/core/i18n";
import { withForkStrings } from "./fork";
import forkAgents from "./zh-Hans/fork/agents.json";
import forkCommon from "./zh-Hans/fork/common.json";
import forkEditor from "./zh-Hans/fork/editor.json";
import forkIssues from "./zh-Hans/fork/issues.json";
import forkLayout from "./zh-Hans/fork/layout.json";
import forkRuntimes from "./zh-Hans/fork/runtimes.json";
import forkSettings from "./zh-Hans/fork/settings.json";
import forkUsage from "./zh-Hans/fork/usage.json";
import zhHansDocs from "./zh-Hans/docs.json";
import zhHansCommon from "./zh-Hans/common.json";
import zhHansAuth from "./zh-Hans/auth.json";
import zhHansSettings from "./zh-Hans/settings.json";
import zhHansIssues from "./zh-Hans/issues.json";
import zhHansAgents from "./zh-Hans/agents.json";
import zhHansEditor from "./zh-Hans/editor.json";
import zhHansOnboarding from "./zh-Hans/onboarding.json";
import zhHansInvite from "./zh-Hans/invite.json";
import zhHansLabels from "./zh-Hans/labels.json";
import zhHansMembers from "./zh-Hans/members.json";
import zhHansMyIssues from "./zh-Hans/my-issues.json";
import zhHansSearch from "./zh-Hans/search.json";
import zhHansInbox from "./zh-Hans/inbox.json";
import zhHansWorkspace from "./zh-Hans/workspace.json";
import zhHansProjects from "./zh-Hans/projects.json";
import zhHansAutopilots from "./zh-Hans/autopilots.json";
import zhHansSkills from "./zh-Hans/skills.json";
import zhHansChat from "./zh-Hans/chat.json";
import zhHansModals from "./zh-Hans/modals.json";
import zhHansRuntimes from "./zh-Hans/runtimes.json";
import zhHansLayout from "./zh-Hans/layout.json";
import zhHansUsage from "./zh-Hans/usage.json";
import zhHansUi from "./zh-Hans/ui.json";
import zhHansSquads from "./zh-Hans/squads.json";
import zhHansBilling from "./zh-Hans/billing.json";

const resources: LocaleResources = {
  docs: zhHansDocs,
  common: zhHansCommon,
  auth: zhHansAuth,
  settings: zhHansSettings,
  issues: zhHansIssues,
  agents: zhHansAgents,
  editor: zhHansEditor,
  onboarding: zhHansOnboarding,
  invite: zhHansInvite,
  labels: zhHansLabels,
  members: zhHansMembers,
  "my-issues": zhHansMyIssues,
  search: zhHansSearch,
  inbox: zhHansInbox,
  workspace: zhHansWorkspace,
  projects: zhHansProjects,
  autopilots: zhHansAutopilots,
  skills: zhHansSkills,
  chat: zhHansChat,
  modals: zhHansModals,
  runtimes: zhHansRuntimes,
  layout: zhHansLayout,
  usage: zhHansUsage,
  ui: zhHansUi,
  squads: zhHansSquads,
  billing: zhHansBilling,
};

export default withForkStrings(resources, { agents: forkAgents, common: forkCommon, editor: forkEditor, issues: forkIssues, layout: forkLayout, runtimes: forkRuntimes, settings: forkSettings, usage: forkUsage });
