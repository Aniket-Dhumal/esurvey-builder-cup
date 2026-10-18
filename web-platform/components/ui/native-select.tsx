import * as React from "react";
import { ChevronDown } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * A native select, styled to match Input.
 *
 * # Why native rather than the Radix Select beside it
 *
 * ui/select.tsx builds a listbox out of divs, which is the right answer when a
 * dropdown needs rich rows, search or multi-select. None of the fourteen
 * selects in this console need any of that — they choose a calamity, a status,
 * a crop, a season.
 *
 * And half of these screens are used on a phone standing in a field. A native
 * select there opens the operating system's own picker: a large wheel or a
 * full-screen list, with the platform's own scrolling and dismiss behaviour.
 * A div-based listbox is a small scrolling panel under a finger. Choosing the
 * native control is not a shortcut here, it is the better control.
 *
 * # What this fixes
 *
 * Fourteen raw selects were styled fourteen times, six different ways: some
 * rounded-md and some rounded-lg, some border-input and some border-border,
 * px-2 against px-3, text-xs against text-base, and heights of h-9, h-12 or
 * whatever the padding happened to produce.
 *
 * More than untidy: not one of them declared a focus style. Input has had
 * `focus-visible:border-ring focus-visible:ring-3` all along, so tabbing
 * through a form moved a visible ring across the text boxes and then went
 * invisible the moment it reached a dropdown. Anybody working the form by
 * keyboard lost their place at every select.
 */

const sizes = {
  default: "h-8 pl-2.5 pr-8 text-sm",
  lg: "h-9 pl-3 pr-9 text-sm",
  // A phone held in a field. Matches Button and Input's `field`.
  field: "h-12 pl-3.5 pr-10 text-base",
} as const;

export function NativeSelect({
  className,
  selectSize = "default",
  children,
  ...props
}: React.ComponentProps<"select"> & { selectSize?: keyof typeof sizes }) {
  return (
    <div className="relative w-full">
      <select
        data-slot="select"
        className={cn(
          // appearance-none so the chevron below is the only arrow; a native
          // one alongside it reads as two controls.
          "w-full appearance-none rounded-lg border border-input bg-transparent",
          "transition-colors outline-none",
          "focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50",
          "disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50",
          "aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20",
          "dark:bg-input/30 dark:disabled:bg-input/80",
          sizes[selectSize],
          className,
        )}
        {...props}>
        {children}
      </select>
      <ChevronDown
        aria-hidden
        className={cn(
          "pointer-events-none absolute top-1/2 -translate-y-1/2 text-muted-foreground",
          selectSize === "field" ? "right-3.5 size-5" : "right-2.5 size-4",
        )}
    />
    </div>
  );
}
