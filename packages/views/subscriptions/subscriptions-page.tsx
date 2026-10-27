"use client";

import { useEffect } from "react";
import { useWorkspacePaths } from "@multica/core/paths";
import { useNavigation } from "../navigation";
import { AccountsOverview } from "../runtimes/accounts/accounts-overview";

/** Old address of the page: Analytics keeps the subscriptions tab. */
export function SubscriptionsPage() {
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const target = `${paths.usage()}?tab=subscriptions`;
  const { replace } = navigation;
  useEffect(() => replace(target), [replace, target]);
  return null;
}

/** Analytics → Subscriptions: quota, runs and discrepancies, view only. */
export function SubscriptionsContent() {
  return <AccountsOverview mode="view" />;
}
