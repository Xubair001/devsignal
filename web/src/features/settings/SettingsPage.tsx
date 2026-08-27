import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { notificationsApi } from '@/lib/api/profile';
import { qk } from '@/lib/queryKeys';
import type { NotificationSettings } from '@/lib/api/types';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { Field, Select, Toggle, Segmented } from '@/components/ui/Field';
import { ErrorState, SkeletonCards } from '@/components/ui/States';
import { PageHeader } from '@/components/ui/PageHeader';
import { useToast } from '@/components/ui/Toast';

/** The wording a user agrees to. Versioned, because consent is evidence. */
const CONSENT_WORDING = 'console-optin-v1';
const CONSENT_TEXT =
  'Send me a daily email digest of roles that clear my bar. I understand I can ' +
  'withdraw this at any time, and that withdrawing does not affect ' +
  'account emails like password resets.';

const ZONES = [
  'UTC', 'Europe/London', 'Europe/Berlin', 'Europe/Lisbon', 'Europe/Warsaw',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Sao_Paulo', 'Asia/Karachi', 'Asia/Kolkata', 'Asia/Dubai',
  'Asia/Singapore', 'Asia/Tokyo', 'Australia/Sydney', 'Pacific/Auckland',
];

export function SettingsPage() {
  const qc = useQueryClient();
  const toast = useToast();

  const settings = useQuery({
    queryKey: qk.notifications(),
    queryFn: () => notificationsApi.get(),
  });
  const history = useQuery({
    queryKey: qk.digestHistory(),
    queryFn: () => notificationsApi.history(),
  });

  const [form, setForm] = useState<NotificationSettings | null>(null);
  useEffect(() => {
    if (settings.data && form === null) setForm(settings.data);
  }, [settings.data, form]);

  const save = useMutation({
    mutationFn: (s: NotificationSettings) =>
      notificationsApi.save({
        timezone: s.timezone,
        quiet_start: s.quiet_start,
        quiet_end: s.quiet_end,
        digest_enabled: s.digest_enabled,
        max_per_week: s.max_per_week,
        min_band: s.min_band,
        send_when_empty: s.send_when_empty,
      }),
    onSuccess: (s) => {
      setForm(s);
      void qc.invalidateQueries({ queryKey: qk.notifications() });
      toast('Notification settings saved');
    },
    onError: () => toast('Could not save settings', 'bad'),
  });

  const consent = useMutation({
    mutationFn: () => notificationsApi.consent(CONSENT_WORDING),
    onSuccess: (s) => {
      setForm(s);
      void qc.invalidateQueries({ queryKey: qk.notifications() });
      toast('Consent recorded');
    },
  });

  const withdraw = useMutation({
    mutationFn: () => notificationsApi.withdraw(),
    onSuccess: (s) => {
      setForm(s);
      void qc.invalidateQueries({ queryKey: qk.notifications() });
      toast('Consent withdrawn. The digest will not be sent.');
    },
  });

  if (settings.isPending) return <SkeletonCards count={2} height="h-[240px]" />;
  if (settings.isError)
    return <ErrorState error={settings.error} onRetry={() => void settings.refetch()} />;
  if (!form) return null;

  const consented = !!form.consent_at && !form.consent_withdrawn_at;
  const set = <K extends keyof NotificationSettings>(k: K, v: NotificationSettings[K]) =>
    setForm({ ...form, [k]: v });

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Notifications"
        subtitle="The daily digest. A hard daily cap, a weekly cap, quiet hours in your own timezone, and a minimum band below which we do not interrupt you at all."
      />

      {/* Consent first, because nothing below it applies without it. */}
      <Card className={consented ? 'border-good/25' : undefined}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 max-w-[62ch]">
            <h2 className="flex items-center gap-2 text-[15px] font-semibold">
              Consent
              {consented ? (
                <Pill tone="met">Given</Pill>
              ) : form.consent_withdrawn_at ? (
                <Pill tone="no_data">Withdrawn</Pill>
              ) : (
                <Pill tone="no_data">Not given</Pill>
              )}
            </h2>
            <p className="mt-2 text-[12.5px] leading-relaxed text-ink-2">{CONSENT_TEXT}</p>
            <p className="mt-2 text-[12px] leading-relaxed text-ink-3">
              Recorded with a wording version, so what you agreed to is part of the record —
              consent you cannot evidence is consent you do not have. Withdrawing keeps the
              record and stamps it, rather than erasing it: the evidence that a withdrawal was
              honoured matters too.
            </p>
            {form.consent_wording_version && (
              <p className="mt-2 font-mono text-[11.5px] text-ink-3">
                version {form.consent_wording_version}
              </p>
            )}
          </div>
          <div className="shrink-0">
            {consented ? (
              <Button variant="danger" onClick={() => withdraw.mutate()} disabled={withdraw.isPending}>
                Withdraw consent
              </Button>
            ) : (
              <Button variant="primary" onClick={() => consent.mutate()} disabled={consent.isPending}>
                I agree — send the digest
              </Button>
            )}
          </div>
        </div>
      </Card>

      <Card className="flex flex-col gap-5">
        <h2 className="text-[15px] font-semibold">When and how often</h2>

        <Toggle
          checked={form.digest_enabled}
          onChange={(v) => set('digest_enabled', v)}
          disabled={!consented}
          label="Send me the daily digest"
          hint={consented ? undefined : 'Requires consent above.'}
        />

        <div className="grid gap-4 sm:grid-cols-3">
          <Field
            label="Timezone"
            htmlFor="tz"
            hint="Quiet hours are local to you, not to the server."
          >
            <Select id="tz" value={form.timezone} onChange={(e) => set('timezone', e.target.value)}>
              {ZONES.map((z) => (
                <option key={z} value={z}>{z}</option>
              ))}
            </Select>
          </Field>

          <Field label="Quiet from" htmlFor="qs">
            <Select
              id="qs"
              value={String(form.quiet_start)}
              onChange={(e) => set('quiet_start', Number(e.target.value))}
            >
              {hours()}
            </Select>
          </Field>

          <Field label="Quiet until" htmlFor="qe">
            <Select
              id="qe"
              value={String(form.quiet_end)}
              onChange={(e) => set('quiet_end', Number(e.target.value))}
            >
              {hours()}
            </Select>
          </Field>
        </div>

        <p className="-mt-2 text-[12px] leading-relaxed text-ink-3">
          A window that wraps midnight is the normal case and is handled: 21:00 to 08:00 means
          overnight, not &ldquo;never&rdquo;. Quiet hours <b className="font-semibold">defer</b> a
          digest rather than cancelling it, so the day stays available once the window closes.
        </p>

        <Field
          label="Maximum per week"
          htmlFor="cap"
          hint="Counts delivered digests only. A day we correctly stayed quiet does not spend your budget. One per day is structural and cannot be raised."
        >
          <Select
            id="cap"
            value={String(form.max_per_week)}
            onChange={(e) => set('max_per_week', Number(e.target.value))}
            className="max-w-[140px]"
          >
            {[0, 1, 2, 3, 4, 5, 6, 7].map((n) => (
              <option key={n} value={n}>{n} per week</option>
            ))}
          </Select>
        </Field>

        <Field
          label="Minimum band to interrupt you"
          hint='A band, never a percentage. "Not enough information" clears neither: interrupting someone on evidence we admit we do not have is worse than staying quiet.'
        >
          <Segmented
            label="Minimum band"
            value={form.min_band}
            onChange={(v) => set('min_band', v)}
            options={[
              { value: 'strong', label: 'Strong fit only' },
              { value: 'worth_a_look', label: 'Worth a look and above' },
            ]}
          />
        </Field>

        <Toggle
          checked={form.send_when_empty}
          onChange={(v) => set('send_when_empty', v)}
          label="Email me even when nothing clears the bar"
          hint="Off by default. The empty state is always recorded either way — we simply do not think a daily message saying nothing happened earns its place in your inbox."
        />

        <div className="flex justify-end">
          <Button
            variant="primary"
            className="h-9"
            onClick={() => save.mutate(form)}
            disabled={save.isPending}
          >
            {save.isPending ? 'Saving…' : 'Save settings'}
          </Button>
        </div>
      </Card>

      <Card>
        <h2 className="text-[15px] font-semibold">Recent digests</h2>
        <p className="mt-1 text-[12.5px] leading-relaxed text-ink-3">
          Every outcome is recorded with its reason, so &ldquo;why did I not get one
          yesterday&rdquo; always has an answer.
        </p>

        {history.isSuccess && history.data.sends.length === 0 && (
          <p className="mt-3 text-[12.5px] text-ink-3">
            No digest has been generated for you yet.
          </p>
        )}

        {history.isSuccess && history.data.sends.length > 0 && (
          <ul className="mt-3 flex flex-col gap-1.5">
            {history.data.sends.map((s) => (
              <li
                key={s.local_date}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-[10px] border border-line bg-raised/50 px-3 py-2"
              >
                <span className="num font-mono text-[12px] text-ink-3">{s.local_date}</span>
                <OutcomePill outcome={s.outcome} />
                {s.item_count > 0 && (
                  <span className="text-[12px] text-ink-2">
                    {s.item_count} role{s.item_count === 1 ? '' : 's'}
                  </span>
                )}
                {s.attempts > 1 && (
                  <span className="text-[11.5px] text-ink-3">
                    {s.attempts} attempts
                  </span>
                )}
                {s.reason && (
                  <span className="basis-full text-[12px] leading-relaxed text-ink-3">
                    {s.reason}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

function OutcomePill({ outcome }: { outcome: string }) {
  if (outcome === 'sent') return <Pill tone="met">Sent</Pill>;
  if (outcome === 'empty') return <Pill tone="no_data">Nothing cleared the bar</Pill>;
  if (outcome === 'failed') return <Pill tone="breached">Failed</Pill>;
  return <Pill tone="at_risk">{outcome.replace(/_/g, ' ')}</Pill>;
}

function hours() {
  return Array.from({ length: 24 }, (_, h) => (
    <option key={h} value={h}>
      {String(h).padStart(2, '0')}:00
    </option>
  ));
}
