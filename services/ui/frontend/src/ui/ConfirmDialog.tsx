import { useId, type ReactNode } from "react";

import { Button } from "./Button";
import { useDismissable } from "./overlay";

export type ConfirmRequest = {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  destructive?: boolean;
  onConfirm: () => void | Promise<void>;
};

type ConfirmDialogProps = ConfirmRequest & {
  onCancel: () => void;
  busy?: boolean;
  confirmDisabled?: boolean;
  testId?: string;
  confirmTestId?: string;
  cancelTestId?: string;
};

// Replaces window.confirm everywhere. It names the affected resource, states
// the concrete consequence, traps focus, and cannot be double-submitted.
export function ConfirmDialog({
  title,
  body,
  confirmLabel,
  destructive,
  onConfirm,
  onCancel,
  busy,
  confirmDisabled,
  testId = "confirm-dialog",
  confirmTestId = "confirm-dialog-confirm",
  cancelTestId = "confirm-dialog-cancel",
}: ConfirmDialogProps) {
  const titleId = useId();
  const bodyId = useId();
  const ref = useDismissable<HTMLDivElement>({
    onDismiss: () => {
      if (!busy) {
        onCancel();
      }
    },
    trapFocus: true,
  });

  return (
    <>
      <div className="backdrop" aria-hidden="true" />
      <div
        ref={ref}
        className="dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
        tabIndex={-1}
        data-testid={testId}
      >
        <h2 className="dialog-title" id={titleId}>
          {title}
        </h2>
        <div className="dialog-body" id={bodyId}>
          {body}
        </div>
        <div className="dialog-actions">
          <Button variant="ghost" onClick={onCancel} disabled={busy} data-testid={cancelTestId}>
            Cancel
          </Button>
          <Button
            variant={destructive ? "danger" : "primary"}
            onClick={() => void onConfirm()}
            busy={busy}
            disabled={confirmDisabled}
            data-autofocus=""
            data-testid={confirmTestId}
          >
            {confirmLabel}
          </Button>
        </div>
      </div>
    </>
  );
}
