import { useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { profileApi } from '@/lib/api/profile';
import { http, ApiError } from '@/lib/api/client';
import { qk } from '@/lib/queryKeys';
import type { Resume } from '@/lib/api/types';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { useToast } from '@/components/ui/Toast';
import { relativeTime } from '@/lib/format';
import { cn } from '@/components/ui/cn';

/**
 * Resume upload and deletion.
 *
 * The parse state is shown rather than hidden behind a spinner, because a
 * document that uploaded but could not be read is a real and common outcome —
 * a scanned PDF has no text layer — and the user can only act on it if they are
 * told.
 */
export function ResumePanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const fileRef = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);

  const resumes = useQuery({
    queryKey: qk.resumes(),
    queryFn: () => profileApi.resumes(),
  });

  const upload = useMutation({
    mutationFn: (file: File) => {
      const form = new FormData();
      form.append('file', file);
      return http.upload<Resume>('/api/v1/profile/resume', form);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resumes() });
      toast('Resume uploaded');
    },
    onError: (e) =>
      toast(
        e instanceof ApiError ? `Upload failed: ${e.status}` : 'Upload failed',
        'bad',
      ),
  });

  const remove = useMutation({
    mutationFn: (id: string) => profileApi.deleteResume(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resumes() });
      toast('Resume deleted, including its extracted text');
    },
  });

  function take(files: FileList | null) {
    const f = files?.[0];
    if (f) upload.mutate(f);
  }

  return (
    <Card className="flex flex-col gap-4">
      <header>
        <h2 className="text-lead font-semibold">Resume</h2>
        <p className="mt-1 text-meta leading-relaxed text-ink-3">
          Stored in private object storage, reachable only by a short-lived signed URL.
          Deleting one removes the document and its extracted text together.
        </p>
      </header>

      <div
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          take(e.dataTransfer.files);
        }}
        className={cn(
          'rounded-[14px] border border-dashed px-4 py-7 text-center',
          'transition-colors duration-[var(--dur-base)]',
          dragging ? 'border-brand bg-brand-wash' : 'border-line-strong bg-raised/60',
        )}
      >
        <input
          ref={fileRef}
          type="file"
          accept=".pdf,.txt,.md,application/pdf,text/plain"
          className="sr-only"
          onChange={(e) => take(e.target.files)}
        />
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden
          className="mx-auto size-7 text-ink-3"
        >
          <path d="M12 16V4m0 0L8 8m4-4 4 4" />
          <path d="M4 16v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" />
        </svg>
        <p className="mt-2.5 text-body font-medium">
          Drop a PDF or text file, or{' '}
          <button
            type="button"
            onClick={() => fileRef.current?.click()}
            className="cursor-pointer text-brand underline decoration-brand/40 underline-offset-2 hover:decoration-brand"
          >
            choose a file
          </button>
        </p>
        <p className="mt-1 text-label text-ink-3">
          {upload.isPending ? 'Uploading…' : 'PDF, TXT or Markdown'}
        </p>
      </div>

      {resumes.isSuccess && resumes.data.items.length > 0 && (
        <ul className="flex flex-col gap-2">
          {resumes.data.items.map((r) => (
            <li
              key={r.id}
              className="flex items-center gap-3 rounded-[10px] border border-line bg-raised/50 px-3 py-2.5"
            >
              <div className="min-w-0 flex-1">
                <p className="truncate text-body font-medium">
                  {r.filename ?? 'untitled document'}
                </p>
                <p className="mt-0.5 text-label text-ink-3">
                  {(r.size_bytes / 1024).toFixed(0)} KB · uploaded{' '}
                  {relativeTime(r.uploaded_at)}
                  {r.text_chars != null && ` · ${r.text_chars.toLocaleString()} characters read`}
                </p>
              </div>
              <ParseState state={r.parse_state} error={r.parse_error} />
              <Button
                variant="danger"
                onClick={() => remove.mutate(r.id)}
                aria-label={`Delete ${r.filename ?? 'resume'}`}
              >
                Delete
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function ParseState({ state, error }: { state: string; error: string | null }) {
  if (state === 'parsed') return <Pill tone="met">Text extracted</Pill>;
  if (state === 'failed')
    return (
      <Pill tone="breached" title={error ?? undefined}>
        Could not read
      </Pill>
    );
  return <Pill tone="no_data">{state}</Pill>;
}
