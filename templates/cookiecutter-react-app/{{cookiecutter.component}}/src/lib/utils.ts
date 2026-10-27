import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * Merge Tailwind classes so the last one wins — the standard shadcn/ui helper.
 * Plain string concatenation does not: "p-2" + "p-4" leaves both in the class list and lets
 * CSS source order decide, which is not the order you wrote them in.
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
