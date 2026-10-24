"use client";

import { use } from "react";
import { CustomPageView } from "@multica/views/custom-pages";

export default function CustomPageRoute({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return <CustomPageView pageId={id} />;
}
