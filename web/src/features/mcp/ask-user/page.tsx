import type { ChangeEvent } from 'react';
import { useCallback, useEffect, useRef, useState } from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Textarea } from '@/components/ui/textarea';
import { normalizeApiKey, useApiKey } from '@/lib/api-key-context';
import { cn } from '@/lib/utils';

import { createVisibilityPoller } from '../shared/visibility-poller';
import { listRequests, submitAnswer, type AskUserRequest } from './api';

/** POLL_INTERVAL_MS is the background refresh cadence for pending questions. */
const POLL_INTERVAL_MS = 5000;
/** POLL_RETRY_MS is the backoff after a failed refresh. */
const POLL_RETRY_MS = 8000;

/** AskUserPage lists pending ask_user questions, collects answers and shows history. */
export function AskUserPage() {
  const { apiKey, isToolConsoleLocked } = useApiKey();
  const [pendingRequests, setPendingRequests] = useState<AskUserRequest[]>([]);
  const [historyRequests, setHistoryRequests] = useState<AskUserRequest[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [draftAnswers, setDraftAnswers] = useState<Record<string, string>>({});
  const [pendingSubmissions, setPendingSubmissions] = useState<Record<string, boolean>>({});

  const pollControlsRef = useRef<{
    refresh: () => void;
  } | null>(null);

  useEffect(() => {
    if (!apiKey || isToolConsoleLocked) {
      setPendingRequests([]);
      setHistoryRequests([]);
      setIsLoading(false);
      return;
    }

    let disposed = false;

    // The poller chains each refresh after the previous one settles, skips ticks
    // while the tab is hidden, and refreshes once when the tab becomes visible.
    const poller = createVisibilityPoller(async ({ signal, reason, priority }) => {
      const initial = reason === 'initial';
      if (initial) {
        setIsLoading(true);
      }

      try {
        const data = await listRequests(apiKey, signal, priority);
        if (disposed || signal.aborted) return null;

        setPendingRequests(data.pending ?? []);
        setHistoryRequests(data.history ?? []);
        return POLL_INTERVAL_MS;
      } catch {
        if (disposed || signal.aborted) return null;
        return POLL_RETRY_MS;
      } finally {
        if (initial && !disposed) {
          setIsLoading(false);
        }
      }
    });

    pollControlsRef.current = poller;
    poller.start();

    return () => {
      disposed = true;
      poller.dispose();
      pollControlsRef.current = null;
    };
  }, [apiKey, isToolConsoleLocked]);

  const handleAnswerChange = useCallback((id: string, value: string) => {
    setDraftAnswers((prev) => ({ ...prev, [id]: value }));
  }, []);

  const handleAnswerSubmit = useCallback(
    async (requestId: string) => {
      if (isToolConsoleLocked) {
        return;
      }

      const key = normalizeApiKey(apiKey);
      if (!key) {
        return;
      }
      const draft = (draftAnswers[requestId] ?? '').trim();
      if (!draft) {
        return;
      }

      setPendingSubmissions((prev) => ({ ...prev, [requestId]: true }));
      try {
        await submitAnswer(key, requestId, draft);
        setDraftAnswers((prev) => ({ ...prev, [requestId]: '' }));
        pollControlsRef.current?.refresh();
      } catch (error) {
        console.error('Failed to submit answer:', error);
      } finally {
        setPendingSubmissions((prev) => {
          const next = { ...prev };
          delete next[requestId];
          return next;
        });
      }
    },
    [apiKey, draftAnswers, isToolConsoleLocked]
  );

  return (
    <div className="space-y-8">
      <section className="space-y-2">
        <h1 className="text-3xl font-bold tracking-tight text-foreground">ask_user</h1>
        <p className="max-w-2xl text-muted-foreground">Review pending questions, send answers, and browse history.</p>
      </section>

      <section className="grid gap-6 lg:grid-cols-2">
        <div className="space-y-4">
          <header className="flex items-center justify-between">
            <h2 className="text-xl font-semibold text-foreground">Pending requests</h2>
            <Badge variant="secondary">{pendingRequests.length}</Badge>
          </header>
          <div className="space-y-4">
            {isLoading && !pendingRequests.length ? (
              <EmptyState message="Loading pending requests…" />
            ) : pendingRequests.length === 0 ? (
              <EmptyState message="No pending questions right now." />
            ) : (
              pendingRequests.map((request) => (
                <PendingRequestCard
                  key={request.id}
                  request={request}
                  draftValue={draftAnswers[request.id] ?? ''}
                  onDraftChange={handleAnswerChange}
                  onSubmit={handleAnswerSubmit}
                  disabled={isToolConsoleLocked || Boolean(pendingSubmissions[request.id])}
                />
              ))
            )}
          </div>
        </div>
        <div className="space-y-4">
          <header className="flex items-center justify-between">
            <h2 className="text-xl font-semibold text-foreground">History</h2>
            <Badge variant="outline">{historyRequests.length}</Badge>
          </header>
          <div className="space-y-4">
            {historyRequests.length === 0 ? (
              <EmptyState message="No prior activity." subtle />
            ) : (
              historyRequests.map((request) => <HistoryCard key={request.id} request={request} />)
            )}
          </div>
        </div>
      </section>
    </div>
  );
}

function EmptyState({ message, subtle = false }: { message: string; subtle?: boolean }) {
  return (
    <div className={cn('rounded-lg border border-dashed px-4 py-6 text-sm text-muted-foreground', subtle ? 'bg-muted/50' : 'bg-muted')}>
      {message}
    </div>
  );
}

function PendingRequestCard({
  request,
  draftValue,
  onDraftChange,
  onSubmit,
  disabled,
}: {
  request: AskUserRequest;
  draftValue: string;
  onDraftChange: (id: string, value: string) => void;
  onSubmit: (id: string) => void;
  disabled: boolean;
}) {
  const questionId = `ask-user-question-${request.id}`;
  return (
    <Card className="border border-primary/30 bg-card shadow-sm">
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>ID: {request.id}</span>
          <span>Asked: {formatDate(request.created_at)}</span>
          {request.ai_identity && <span>AI: {request.ai_identity}</span>}
        </div>
        <CardTitle id={questionId} className="text-base font-semibold text-foreground">
          {request.question}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <Textarea
          aria-labelledby={questionId}
          value={draftValue}
          onChange={(event: ChangeEvent<HTMLTextAreaElement>) => onDraftChange(request.id, event.target.value)}
          placeholder="Provide your answer…"
          disabled={disabled}
        />
        <div className="flex justify-end">
          <Button onClick={() => onSubmit(request.id)} disabled={disabled}>
            {disabled ? 'Sending…' : 'Send answer'}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function HistoryCard({ request }: { request: AskUserRequest }) {
  return (
    <Card className="border border-border/60 bg-card">
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>ID: {request.id}</span>
          <span>Asked: {formatDate(request.created_at)}</span>
          {request.answered_at && <span>Answered: {formatDate(request.answered_at)}</span>}
          {request.ai_identity && <span>AI: {request.ai_identity}</span>}
        </div>
        <CardTitle className="text-base font-semibold text-foreground">{request.question}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm text-muted-foreground">
        {request.answer ? (
          <div className="rounded-lg border border-border bg-muted/60 p-4 text-foreground">{request.answer}</div>
        ) : (
          <p className="italic text-muted-foreground">No answer provided.</p>
        )}
        <p className="text-xs text-muted-foreground">
          Status: <span className="uppercase tracking-wide">{request.status}</span>
        </p>
      </CardContent>
    </Card>
  );
}

function formatDate(value?: string | null) {
  if (!value) return 'N/A';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}
