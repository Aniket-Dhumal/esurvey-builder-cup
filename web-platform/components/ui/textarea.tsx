import * as React from "react"

import { cn } from "@/lib/utils"

/**
 * Sizes matching Input and NativeSelect, so a form does not step between its
 * own controls. `field` is the farmer tier: a phone held outdoors.
 */
const textareaSizes = {
  default: "min-h-16 px-2.5 py-2 text-sm",
  field: "min-h-24 px-3.5 py-3 text-base",
} as const;

function Textarea({
  className,
  textareaSize = "default",
  ...props
}: React.ComponentProps<"textarea"> & {
  textareaSize?: keyof typeof textareaSizes;
}) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "flex field-sizing-content w-full rounded-lg border border-input bg-transparent transition-colors outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 dark:bg-input/30 dark:disabled:bg-input/80 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40",
        textareaSizes[textareaSize],
        className
      )}
      {...props}
    />
  )
}

export { Textarea }
