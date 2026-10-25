"use client";

import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";

/** Confirms the way back to the shared profile: the personal profile and its rows are deleted. */
export function ResetPersonalDialog({
  open,
  baseName,
  busy,
  onConfirm,
  onOpenChange,
}: {
  open: boolean;
  baseName: string;
  busy: boolean;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("agents");
  return (
    <AlertDialog open={open} onOpenChange={(next) => (busy ? undefined : onOpenChange(next))}>
      <AlertDialogContent className="w-[calc(100vw-2rem)] !max-w-[440px] gap-0 overflow-hidden p-0">
        <div className="px-5 pb-4 pt-5">
          <AlertDialogTitle className="font-sans text-title-sm font-semibold">
            {t(($) => $.models.reset_title, { name: baseName })}
          </AlertDialogTitle>
          <AlertDialogDescription className="mt-1 text-left text-body leading-5 text-pretty">
            {t(($) => $.models.reset_text)}
          </AlertDialogDescription>
        </div>
        <div className="border-t bg-muted/25 px-5 py-3">
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button type="button" variant="outline" className="w-full sm:w-auto" disabled={busy} onClick={() => onOpenChange(false)}>
              {t(($) => $.models.cancel)}
            </Button>
            <Button type="button" className="w-full sm:w-auto" disabled={busy} aria-busy={busy} onClick={onConfirm}>
              {t(($) => $.models.reset_confirm)}
            </Button>
          </div>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  );
}
