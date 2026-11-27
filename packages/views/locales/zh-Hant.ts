import type { LocaleResources } from "@multica/core/i18n";
import { withForkStrings } from "./fork";
import forkAgents from "./zh-Hant/fork/agents.json";
import forkCommon from "./zh-Hant/fork/common.json";
import forkEditor from "./zh-Hant/fork/editor.json";
import forkIssues from "./zh-Hant/fork/issues.json";
import forkLayout from "./zh-Hant/fork/layout.json";
import forkRuntimes from "./zh-Hant/fork/runtimes.json";
import forkSettings from "./zh-Hant/fork/settings.json";
import forkUsage from "./zh-Hant/fork/usage.json";
import zhHantDocs from "./zh-Hant/docs.json";
import zhHantCommon from "./zh-Hant/common.json";
import zhHantAuth from "./zh-Hant/auth.json";
import zhHantSettings from "./zh-Hant/settings.json";
import zhHantIssues from "./zh-Hant/issues.json";
import zhHantAgents from "./zh-Hant/agents.json";
import zhHantEditor from "./zh-Hant/editor.json";
import zhHantOnboarding from "./zh-Hant/onboarding.json";
import zhHantInvite from "./zh-Hant/invite.json";
import zhHantLabels from "./zh-Hant/labels.json";
import zhHantMembers from "./zh-Hant/members.json";
import zhHantMyIssues from "./zh-Hant/my-issues.json";
import zhHantSearch from "./zh-Hant/search.json";
import zhHantInbox from "./zh-Hant/inbox.json";
import zhHantWorkspace from "./zh-Hant/workspace.json";
import zhHantProjects from "./zh-Hant/projects.json";
import zhHantAutopilots from "./zh-Hant/autopilots.json";
import zhHantSkills from "./zh-Hant/skills.json";
import zhHantChat from "./zh-Hant/chat.json";
import zhHantModals from "./zh-Hant/modals.json";
import zhHantRuntimes from "./zh-Hant/runtimes.json";
import zhHantLayout from "./zh-Hant/layout.json";
import zhHantUsage from "./zh-Hant/usage.json";
import zhHantUi from "./zh-Hant/ui.json";
import zhHantSquads from "./zh-Hant/squads.json";
import zhHantBilling from "./zh-Hant/billing.json";

const resources: LocaleResources = {
  docs: zhHantDocs,
  common: zhHantCommon,
  auth: zhHantAuth,
  settings: zhHantSettings,
  issues: zhHantIssues,
  agents: zhHantAgents,
  editor: zhHantEditor,
  onboarding: zhHantOnboarding,
  invite: zhHantInvite,
  labels: zhHantLabels,
  members: zhHantMembers,
  "my-issues": zhHantMyIssues,
  search: zhHantSearch,
  inbox: zhHantInbox,
  workspace: zhHantWorkspace,
  projects: zhHantProjects,
  autopilots: zhHantAutopilots,
  skills: zhHantSkills,
  chat: zhHantChat,
  modals: zhHantModals,
  runtimes: zhHantRuntimes,
  layout: zhHantLayout,
  usage: zhHantUsage,
  ui: zhHantUi,
  squads: zhHantSquads,
  billing: zhHantBilling,
};

export default withForkStrings(resources, { agents: forkAgents, common: forkCommon, editor: forkEditor, issues: forkIssues, layout: forkLayout, runtimes: forkRuntimes, settings: forkSettings, usage: forkUsage });
