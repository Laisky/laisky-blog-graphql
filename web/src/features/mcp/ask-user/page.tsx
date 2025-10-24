import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ChangeEvent, FormEvent } from 'react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

import { listRequests, submitAnswer, type AskUserRequest } from './api'

const STORAGE_KEY = 'ask_user_api_key'

interface StatusState {
  message: string
  tone: 'info' | 'success' | 'error'
}

interface IdentityState {
  userId?: string
  aiId?: string
  keyHint?: string
}

export function AskUserPage() {
  const [apiKey, setApiKey] = useState<string>(() => localStorage.getItem(STORAGE_KEY) ?? '')
  const [pendingRequests, setPendingRequests] = useState<AskUserRequest[]>([])
  const [historyRequests, setHistoryRequests] = useState<AskUserRequest[]>([])
  const [identity, setIdentity] = useState<IdentityState | null>(null)
  const [status, setStatus] = useState<StatusState | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [formValue, setFormValue] = useState(apiKey ? '********' : '')
  const [draftAnswers, setDraftAnswers] = useState<Record<string, string>>({})
  const [pendingSubmissions, setPendingSubmissions] = useState<Record<string, boolean>>({})

  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pollControlsRef = useRef<{ schedule: (delay: number) => void; refresh: () => void } | null>(
    null
  )

  useEffect(() => {
    if (apiKey) {
      localStorage.setItem(STORAGE_KEY, apiKey)
    } else {
      localStorage.removeItem(STORAGE_KEY)
    }
  }, [apiKey])

  useEffect(() => {
    if (!apiKey) {
      setPendingRequests([])
      setHistoryRequests([])
      setIdentity(null)
      setStatus({ message: 'Disconnected.', tone: 'info' })
      return
    }

    let disposed = false
    let inFlight: AbortController | null = null

    async function fetchData(initial: boolean) {
      if (disposed) return

      if (initial) {
        setIsLoading(true)
        setStatus({ message: 'Connected. Fetching requests…', tone: 'info' })
      }

      if (inFlight) {
        inFlight.abort()
      }
      const controller = new AbortController()
      inFlight = controller

      try {
        const data = await listRequests(apiKey, controller.signal)
        if (disposed) return

        setPendingRequests(data.pending ?? [])
        setHistoryRequests(data.history ?? [])
        setIdentity({ userId: data.user_id, aiId: data.ai_id, keyHint: data.key_hint })
        setStatus({
          message: identityMessage(data.user_id, data.ai_id, data.key_hint),
          tone: 'success',
        })
        schedule(5000)
      } catch (error) {
        if (disposed || controller.signal.aborted) return
        setStatus({
          message: error instanceof Error ? error.message : 'Failed to fetch requests.',
          tone: 'error',
        })
        schedule(8000)
      } finally {
        if (initial && !disposed) {
          setIsLoading(false)
        }
      }
    }

    function schedule(delay: number) {
      if (disposed) return
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current)
      }
      pollTimerRef.current = setTimeout(() => {
        fetchData(false)
      }, delay)
    }

    pollControlsRef.current = {
      schedule,
      refresh: () => fetchData(false),
    }

    fetchData(true)

    return () => {
      disposed = true
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current)
        pollTimerRef.current = null
      }
      if (inFlight) {
        inFlight.abort()
      }
      pollControlsRef.current = null
    }
  }, [apiKey])

  const handleAuthSubmit = useCallback(
    (event: FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      const value = formValue.trim()
      if (!value) {
        setStatus({ message: 'Please provide an API key.', tone: 'error' })
        return
      }
      if (value === '********' && apiKey) {
        setStatus({ message: 'Using stored API key.', tone: 'info' })
        pollControlsRef.current?.refresh()
        return
      }
      setApiKey(value)
      setFormValue('********')
      setStatus({ message: 'Connecting…', tone: 'info' })
    },
    [apiKey, formValue]
  )

  const handleDisconnect = useCallback(() => {
    setApiKey('')
    setFormValue('')
    setStatus({ message: 'Disconnected.', tone: 'info' })
    if (pollTimerRef.current) {
      clearTimeout(pollTimerRef.current)
      pollTimerRef.current = null
    }
  }, [])

  const handleAnswerChange = useCallback((id: string, value: string) => {
    setDraftAnswers((prev) => ({ ...prev, [id]: value }))
  }, [])

  const handleAnswerSubmit = useCallback(
    async (requestId: string) => {
      const key = apiKey.trim()
      if (!key) {
        setStatus({ message: 'Connect with your API key before answering.', tone: 'error' })
        return
      }
      const draft = (draftAnswers[requestId] ?? '').trim()
      if (!draft) {
        setStatus({ message: 'Answer cannot be empty.', tone: 'error' })
        return
      }

      setPendingSubmissions((prev) => ({ ...prev, [requestId]: true }))
      try {
        await submitAnswer(key, requestId, draft)
        setStatus({ message: 'Answer submitted successfully.', tone: 'success' })
        setDraftAnswers((prev) => ({ ...prev, [requestId]: '' }))
        pollControlsRef.current?.schedule(0)
      } catch (error) {
        setStatus({
          message: error instanceof Error ? error.message : 'Failed to submit answer.',
          tone: 'error',
        })
      } finally {
        setPendingSubmissions((prev) => {
          const next = { ...prev }
          delete next[requestId]
          return next
        })
      }
    },
    [apiKey, draftAnswers]
  )

  const maskedKeySuffix = useMemo(() => {
    if (!identity?.keyHint) return ''
    return `token •••${identity.keyHint}`
  }, [identity])

  return (
    <div className="space-y-8">
      <section className="space-y-2">
        <p className="text-xs uppercase tracking-widest text-muted-foreground">MCP tools</p>
        <h1 className="text-3xl font-semibold text-foreground">ask_user console</h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Review pending MCP questions routed to your bearer token, send human answers, and browse
          the recent history. The HTTP API remains available under <code>/mcp/tools/ask_user/api</code>.
        </p>
      </section>

      <Card className="border border-border/60 bg-card shadow-sm">
        <CardHeader className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
          <div>
            <CardTitle className="text-lg text-foreground">Authenticate</CardTitle>
            <p className="text-sm text-muted-foreground">
              Enter the bearer token shared with your AI agent. The token is stored locally and never
              sent elsewhere.
            </p>
          </div>
          {apiKey && (
            <Button variant="outline" onClick={handleDisconnect}>
              Disconnect
            </Button>
          )}
        </CardHeader>
        <CardContent>
          <form onSubmit={handleAuthSubmit} className="flex flex-col gap-3 md:flex-row">
            <Input
              value={formValue}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setFormValue(event.target.value)}
              type="password"
              placeholder="Enter your API key"
              autoComplete="off"
              className="md:max-w-md"
              required
            />
            <div className="flex gap-2">
              <Button type="submit">Connect</Button>
              {apiKey && (
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => pollControlsRef.current?.refresh()}
                >
                  Refresh
                </Button>
              )}
            </div>
          </form>
          {status && (
            <StatusBanner status={status} className="mt-4" maskedKeySuffix={maskedKeySuffix} />
          )}
        </CardContent>
      </Card>

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
                  disabled={Boolean(pendingSubmissions[request.id])}
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
  )
}

function identityMessage(userId?: string, aiId?: string, keyHint?: string) {
  const user = userId || 'unknown user'
  const ai = aiId || 'unknown agent'
  const suffix = keyHint ? `token •••${keyHint}` : 'token hidden'
  return `Linked identities: ${user} / ${ai} (${suffix})`
}

function StatusBanner({
  status,
  maskedKeySuffix,
  className,
}: {
  status: StatusState
  maskedKeySuffix: string
  className?: string
}) {
  const toneStyles = {
    info: 'border-border bg-muted text-muted-foreground',
    success:
      'border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-200 dark:border-emerald-500/40',
    error:
      'border-rose-500/40 bg-rose-500/10 text-rose-700 dark:text-rose-200 dark:border-rose-500/40',
  } as const

  return (
    <div
      className={cn(
        'flex flex-col gap-1 rounded-lg border px-4 py-3 text-sm transition-colors',
        toneStyles[status.tone],
        className
      )}
    >
      <span>{status.message}</span>
      {status.tone === 'success' && maskedKeySuffix && (
        <span className="text-xs text-inherit/80">{maskedKeySuffix}</span>
      )}
    </div>
  )
}

function EmptyState({ message, subtle = false }: { message: string; subtle?: boolean }) {
  return (
    <div
      className={cn(
        'rounded-lg border border-dashed px-4 py-6 text-sm text-muted-foreground',
        subtle ? 'bg-muted/50' : 'bg-muted'
      )}
    >
      {message}
    </div>
  )
}

function PendingRequestCard({
  request,
  draftValue,
  onDraftChange,
  onSubmit,
  disabled,
}: {
  request: AskUserRequest
  draftValue: string
  onDraftChange: (id: string, value: string) => void
  onSubmit: (id: string) => void
  disabled: boolean
}) {
  return (
    <Card className="border border-primary/30 bg-card shadow-sm">
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>ID: {request.id}</span>
          <span>Asked: {formatDate(request.created_at)}</span>
          {request.ai_identity && <span>AI: {request.ai_identity}</span>}
        </div>
        <CardTitle className="text-base font-semibold text-foreground">{request.question}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <Textarea
          value={draftValue}
          onChange={(event: ChangeEvent<HTMLTextAreaElement>) =>
            onDraftChange(request.id, event.target.value)
          }
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
  )
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
          <div className="rounded-lg border border-border bg-muted/60 p-4 text-foreground">
            {request.answer}
          </div>
        ) : (
          <p className="italic text-muted-foreground">No answer provided.</p>
        )}
        <p className="text-xs text-muted-foreground">
          Status: <span className="uppercase tracking-wide">{request.status}</span>
        </p>
      </CardContent>
    </Card>
  )
}

function formatDate(value?: string | null) {
  if (!value) return 'N/A'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
