/**
 * The logotype mark: a rising trace with a verified point at the end.
 *
 * The two halves are the product — a signal read from noise, and a single
 * confirmed observation. Not a generic chart glyph: the dot is the whole claim.
 */
export function Mark({ size = 26 }: { size?: number }) {
  const inner = Math.round(size * 0.58);
  return (
    <span
      aria-hidden
      className="grid shrink-0 place-items-center rounded-[calc(var(--radius-md)-1px)] border border-brand-edge bg-brand-wash"
      style={{ width: size, height: size, borderRadius: size * 0.32 }}
    >
      <svg viewBox="0 0 24 24" fill="none" style={{ width: inner, height: inner }} className="text-brand">
        <path
          d="M3.5 18 9 10.5l3.5 3.5L19 5"
          stroke="currentColor"
          strokeWidth="2.4"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
        <circle cx="19.6" cy="4.8" r="2.4" fill="currentColor" />
      </svg>
    </span>
  );
}
