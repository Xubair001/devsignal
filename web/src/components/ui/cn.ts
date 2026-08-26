/** Joins class names, dropping falsy entries. No dependency needed for this. */
export function cn(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(' ');
}
