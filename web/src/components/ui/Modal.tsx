import { useEffect, useRef, type ReactNode } from 'react';
import { cn } from './cn';

/**
 * A modal dialog.
 *
 * Focus is moved in on open and the Escape key closes it, because a dialog you
 * cannot leave by keyboard is a trap rather than a dialog. `aria-modal` plus a
 * labelled heading is what makes a screen reader treat it as one.
 *
 * The backdrop is a real button so it is reachable and announced, rather than a
 * clickable div that only a mouse can use.
 */
export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
}) {
  const panel = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    // Move focus in, so the first Tab lands inside rather than back in the page.
    panel.current?.querySelector<HTMLElement>(
      'input, select, textarea, button, [tabindex]:not([tabindex="-1"])',
    )?.focus();
    // The page behind must not scroll under the dialog.
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = prev;
    };
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-80 grid place-items-center p-4">
      <button
        aria-label="Close"
        onClick={onClose}
        className="absolute inset-0 cursor-default bg-black/45 backdrop-blur-[2px]"
      />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        className={cn(
          'relative flex max-h-[calc(100dvh-2rem)] w-full max-w-[440px] flex-col',
          'overflow-hidden rounded-[var(--radius-xl)] border border-line bg-surface',
          'shadow-[var(--shadow-float)] rise',
        )}
      >
        <header className="border-b border-line px-5 py-4">
          <h2 id="modal-title" className="text-lead font-semibold">
            {title}
          </h2>
          {description && (
            <p className="mt-1.5 text-meta leading-relaxed text-ink-3">{description}</p>
          )}
        </header>

        {children && <div className="flex-1 overflow-y-auto px-5 py-4">{children}</div>}

        {footer && (
          <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-line px-5 py-3.5">
            {footer}
          </footer>
        )}
      </div>
    </div>
  );
}
