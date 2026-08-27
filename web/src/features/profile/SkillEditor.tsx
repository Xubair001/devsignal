import { useState } from 'react';
import { Pill } from '@/components/ui/Pill';
import { Input } from '@/components/ui/Field';
import { cn } from '@/components/ui/cn';

/**
 * The skill list.
 *
 * The important behaviour here is what happens to a name the server does not
 * recognise. The profile deliberately cannot mint new skills — otherwise every
 * typo becomes a vocabulary entry that then matches no posting — so an
 * unrecognised name is reported back and shown struck through. A silently
 * dropped skill would count toward nothing while the user believed it counted,
 * which is the worst of the three possible behaviours.
 */
export function SkillEditor({
  skills,
  unresolved,
  onChange,
}: {
  skills: { name: string }[];
  unresolved: string[];
  onChange: (next: { name: string }[]) => void;
}) {
  const [draft, setDraft] = useState('');

  function add() {
    const parts = draft
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    if (parts.length === 0) return;
    const existing = new Set(skills.map((s) => s.name.toLowerCase()));
    const next = [...skills];
    for (const p of parts) {
      if (!existing.has(p.toLowerCase())) next.push({ name: p });
    }
    onChange(next);
    setDraft('');
  }

  const rejected = new Set(unresolved.map((u) => u.toLowerCase()));

  return (
    <div className="flex flex-col gap-3">
      <div className="flex gap-2">
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              add();
            }
          }}
          placeholder="Go, PostgreSQL, Kubernetes"
          aria-label="Add skills"
        />
        <button
          type="button"
          onClick={add}
          disabled={!draft.trim()}
          className={cn(
            'shrink-0 cursor-pointer rounded-[10px] border border-line bg-surface px-3.5',
            'text-meta font-medium text-ink-2 transition-all duration-[var(--dur-base)]',
            'hover:border-line-strong hover:text-ink disabled:opacity-40',
          )}
        >
          Add
        </button>
      </div>

      {skills.length === 0 ? (
        <p className="text-meta text-ink-3">
          No skills yet. Without them the required- and preferred-skill factors
          cannot be scored, which is 45 of the fit model&apos;s 100 points.
        </p>
      ) : (
        <ul className="flex flex-wrap gap-1.5">
          {skills.map((s) => {
            const bad = rejected.has(s.name.toLowerCase());
            return (
              <li key={s.name}>
                <span
                  className={cn(
                    'inline-flex items-center gap-1.5 rounded-full border py-1 pl-2.5 pr-1',
                    'text-meta font-medium transition-colors duration-[var(--dur-fast)]',
                    bad
                      ? 'border-warn/30 bg-warn-wash text-warn'
                      : 'border-line bg-raised text-ink-2',
                  )}
                >
                  <span className={bad ? 'line-through decoration-warn/60' : undefined}>
                    {s.name}
                  </span>
                  <button
                    type="button"
                    aria-label={`Remove ${s.name}`}
                    onClick={() => onChange(skills.filter((x) => x.name !== s.name))}
                    className="grid size-4 cursor-pointer place-items-center rounded-full text-ink-3 transition-colors hover:bg-line hover:text-ink"
                  >
                    <svg
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="2.6"
                      strokeLinecap="round"
                      aria-hidden
                      className="size-2.5"
                    >
                      <path d="M18 6 6 18M6 6l12 12" />
                    </svg>
                  </button>
                </span>
              </li>
            );
          })}
        </ul>
      )}

      {unresolved.length > 0 && (
        <div className="rounded-[10px] border border-warn/25 bg-warn-wash px-3 py-2.5">
          <p className="text-meta font-semibold text-warn">
            {unresolved.length} skill{unresolved.length === 1 ? '' : 's'} not recognised
          </p>
          <p className="mt-1 text-meta leading-relaxed text-ink-2">
            These were not saved, because a name we cannot place in the ontology would
            match no posting. Check the spelling, or use the common name — &ldquo;Go&rdquo;
            rather than &ldquo;Go lang&rdquo;. The ontology knows 264 skills and their
            usual variants.
          </p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {unresolved.map((u) => (
              <Pill key={u} tone="at_risk">
                {u}
              </Pill>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
