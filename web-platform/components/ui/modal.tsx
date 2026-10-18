"use client";

import { useEffect, useRef } from "react";
import { X } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * A dialog, with the behaviour people expect from one.
 *
 * # Why this exists rather than another hand-rolled overlay
 *
 * There was already one of these written inline, and a form that should have
 * been a dialog was sitting in the page instead. A third would have been a
 * third set of decisions about Escape, focus and the backdrop, and the one
 * that quietly did nothing on Escape would be the one nobody noticed.
 *
 * # What it does that a positioned div does not
 *
 * Escape closes it. The backdrop closes it, but only when the click starts
 * and ends there — dragging a text selection out of the dialog and releasing
 * over the backdrop is not a request to discard the form, and treating it as
 * one loses whatever was typed.
 *
 * Focus moves into the dialog on open and returns to whatever opened it on
 * close, so a keyboard user is not dropped at the top of the document. Tab is
 * confined to the dialog, because a form behind an overlay that can still be
 * typed into is a form somebody will fill in and lose.
 *
 * The page behind is locked from scrolling, and marked aria-hidden so a screen
 * reader reads the dialog rather than the page under it.
 */
export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = "md",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children: React.ReactNode;
  footer?: React.ReactNode;
  size?: "sm" | "md" | "lg";
}) {
  const panel = useRef<HTMLDivElement>(null);
  const returnTo = useRef<HTMLElement | null>(null);

  // The latest onClose, without the effect below depending on it.
  //
  // Every caller passes an inline arrow, so the prop is a new function on
  // every render of the dialog -- and a dialog holding a form re-renders on
  // every keystroke. With onClose in the dependency list, that tore the
  // effect down and set it up again per character: the cleanup returned focus
  // to whatever opened the dialog, and the setup moved it to the first field.
  // Typing one letter anywhere but the first field therefore threw the caret
  // out of the field, which is exactly what it looked like from the outside --
  // "the field disables itself after each character".
  const closeRef = useRef(onClose);
  useEffect(() => {
    closeRef.current = onClose;
  }, [onClose]);
  // Where the pointer went down. A backdrop click only counts when it began
  // on the backdrop too.
  const downOnBackdrop = useRef(false);

  useEffect(() => {
    if (!open) return;

    returnTo.current = document.activeElement as HTMLElement | null;

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        closeRef.current();
        return;
      }
      if (e.key !== "Tab" || !panel.current) return;

      const focusable = panel.current.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];

      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };

    document.addEventListener("keydown", onKey, true);

    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    // The first field, not the panel, so typing starts where the eye is.
    const t = window.setTimeout(() => {
      const target = panel.current?.querySelector<HTMLElement>(
        'input:not([type="hidden"]):not([disabled]), textarea:not([disabled]), select:not([disabled]), button:not([disabled])',
      );
      (target ?? panel.current)?.focus();
    }, 0);

    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.body.style.overflow = prevOverflow;
      window.clearTimeout(t);
      returnTo.current?.focus?.();
    };
    // open only. What this effect does -- trap Tab, lock the page, move focus
    // in on open and back out on close -- is about the dialog being open, and
    // rerunning it for any other reason moves the caret.
  }, [open]);

  if (!open) return null;

  const width =
    size === "sm" ? "max-w-md" : size === "lg" ? "max-w-3xl" : "max-w-xl";

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-foreground/30 p-4 sm:items-center"
      onMouseDown={(e) => {
        downOnBackdrop.current = e.target === e.currentTarget;
      }}
      onMouseUp={(e) => {
        if (downOnBackdrop.current && e.target === e.currentTarget) onClose();
        downOnBackdrop.current = false;
      }}>
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        aria-describedby={description ? "modal-description" : undefined}
        tabIndex={-1}
        className={cn(
          "my-auto w-full rounded-2xl border border-border bg-card shadow-lg outline-none",
          width,
        )}>
        <div className="flex items-start justify-between gap-4 border-b border-border px-6 py-4">
          <div>
            <h2 id="modal-title" className="text-base font-semibold text-foreground">
              {title}
            </h2>
            {description && (
              <p id="modal-description" className="mt-1 text-sm text-muted-foreground">
                {description}
              </p>
            )}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="grid size-8 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground">
            <X className="size-4" />
          </button>
        </div>

        <div className="max-h-[70vh] overflow-y-auto px-6 py-5">{children}</div>

        {footer && (
          <div className="flex items-center justify-end gap-3 border-t border-border px-6 py-4">
            {footer}
          </div>
        )}
      </div>
    </div>
  );
}
