import * as React from "react"

import { cn } from "@/lib/utils"

/**
 * Sizes, matching the Button's.
 *
 * `default` is the console: an officer at a desk working through a queue.
 * `field` is a farmer holding a phone in their crop, and is the same height
 * the mobile app enforces for a primary control. Naming them stops the next
 * screen inventing a third: before this, one file said h-11 and another h-12
 * for the same intent, and neither said which was meant.
 */
const inputSizes = {
  default: "h-8 text-sm",
  lg: "h-9 text-sm",
  // 16px, and not only for legibility. Safari on iOS zooms the page whenever a
  // focused input is under 16px, which on a farmer tapping a field means the
  // form jumps and they lose their place. The console can be 14px because it is
  // read at a desk; this cannot.
  field: "h-12 px-3.5 text-base",
} as const;

function Input({
  className,
  type,
  inputSize = "default",
  ...props
}: React.ComponentProps<"input"> & { inputSize?: keyof typeof inputSizes }) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1 transition-colors outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 dark:bg-input/30 dark:disabled:bg-input/80 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40",
        inputSizes[inputSize],
        className
      )}
      {...props}
    />
  )
}

export { Input }
