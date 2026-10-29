import { redirect } from "next/navigation";

// The epics board was removed (STT-455); stage dispatch lives on the issue
// card. Old /conveyor links land on Issues instead of a 404.
export default async function ConveyorRedirect({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(`/${encodeURIComponent(workspaceSlug)}/issues`);
}
