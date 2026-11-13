"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { ExpressBindPage } from "@multica/views/express";

// /express/bind?token=<raw> is the bot's "link your account" destination.
// Suspense wraps useSearchParams per Next.js 15's CSR-bailout rule.
function ExpressBindPageContent() {
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  return <ExpressBindPage token={token} />;
}

export default function Page() {
  return (
    <Suspense fallback={null}>
      <ExpressBindPageContent />
    </Suspense>
  );
}
