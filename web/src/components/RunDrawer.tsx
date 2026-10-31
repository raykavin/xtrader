import { type ReactNode, useEffect, useRef } from "react";

interface Props {
  /** Whether it is shown. */
  open: boolean;
  /** Called as it closes: Esc, a click on the backdrop, or the close of its content. */
  onClose: () => void;
  label: string;
  children: ReactNode;
}

/**
 * A panel from the right, over the page: a modal dialog (the focus kept in it,
 * Esc and a click beside it close it), across the screen on a phone. For the
 * detail of a row, beside the list it was opened from rather than under it.
 */
export function RunDrawer({ open, onClose, label, children }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = dialog.current;
    if (!d) return;
    if (open && !d.open) {
      d.showModal();
      d.focus(); // the panel itself, not its first button: nothing looks chosen
    }
    if (!open && d.open) d.close();
  }, [open]);
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="side-drawer"
      aria-label={label}
      tabIndex={-1}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current) onClose(); // the backdrop
      }}
    >
      {open && <div className="side-drawer-body">{children}</div>}
    </dialog>
  );
}
