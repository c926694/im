import { useEffect, useId, useRef } from "react";
import type { ReactNode } from "react";
import { Icon } from "./visuals";

export function Dialog({ title, busy, onClose, children, className }: {
  title: string;
  busy: boolean;
  onClose: () => void;
  children: ReactNode;
  className?: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const label = useId();
  useEffect(() => {
    const element = dialog.current!;
    element.showModal();
    return () => element.close();
  }, []);

  return <dialog ref={dialog} className={`group-dialog ${className ?? ""}`.trim()} aria-labelledby={label} onCancel={(event) => {
    event.preventDefault();
    if (!busy) onClose();
  }}>
    <header className="group-dialog-header">
      <h2 id={label}>{title}</h2>
      <button type="button" className="icon-button" aria-label="关闭" disabled={busy} onClick={onClose}><Icon name="close" /></button>
    </header>
    <div className="group-dialog-body">{children}</div>
  </dialog>;
}
