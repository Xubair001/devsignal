import { useId } from 'react';

type Props = {
  series: number[];
  tone?: 'good' | 'bad' | 'null';
  /** Real text for a screen reader; the shape is decoration on top of it. */
  label: string;
  className?: string;
};

/**
 * Drawn rather than imported. An area fill, a faint baseline and an emphasised
 * endpoint are what make a 30px-tall chart readable — the same care type gets.
 */
export function Sparkline({ series, tone = 'good', label, className }: Props) {
  const gradientId = useId();
  if (series.length < 2) return null;

  const w = 100;
  const h = 30;
  const pad = 3;
  const lo = Math.min(...series);
  const hi = Math.max(...series);
  const span = hi - lo || 1;

  const pts = series.map((v, i) => {
    const x = pad + (i / (series.length - 1)) * (w - pad * 2);
    const y = h - pad - ((v - lo) / span) * (h - pad * 2);
    return [x, y] as const;
  });

  const line = pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)}`).join('');
  const first = pts[0]!;
  const last = pts[pts.length - 1]!;
  const area = `${line}L${last[0].toFixed(1)},${h}L${first[0].toFixed(1)},${h}Z`;
  const stroke = `var(--color-${tone === 'null' ? 'null' : tone})`;

  return (
    <svg
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={label}
      className={className ?? 'block h-[30px] w-full overflow-visible'}
    >
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={stroke} stopOpacity="0.22" />
          <stop offset="100%" stopColor={stroke} stopOpacity="0" />
        </linearGradient>
      </defs>
      <line
        x1="0"
        y1={h - pad}
        x2={w}
        y2={h - pad}
        stroke="var(--color-line)"
        strokeWidth="1"
        vectorEffect="non-scaling-stroke"
      />
      <path d={area} fill={`url(#${gradientId})`} />
      <path
        d={line}
        fill="none"
        stroke={stroke}
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
      <circle cx={last[0].toFixed(1)} cy={last[1].toFixed(1)} r="2.1" fill={stroke} />
    </svg>
  );
}
