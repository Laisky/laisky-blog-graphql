import { ClipboardList, PlusCircle, Trash2 } from 'lucide-react'
import type { ChangeEvent } from 'react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { ApiKeyInput } from '@/components/api-key-input'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { normalizeApiKey, useApiKey } from '@/lib/api-key-context'
import { cn } from '@/lib/utils'

import {
  createUserRequest,
  deleteAllUserRequests,
  deleteUserRequest,
  listUserRequests,
  type UserRequest,
} from './api'

interface StatusState {
  message: string
  tone: 'info' | 'success' | 'error'
}

interface IdentityState {
  userId?: string
  keyHint?: string
}

export function UserRequestsPage() {
  const { apiKey } = useApiKey()
  const [status, setStatus] = useState<StatusState | null>(null)
  const [identity, setIdentity] = useState<IdentityState | null>(null)
  const [pending, setPending] = useState<UserRequest[]>([])
  const [consumed, setConsumed] = useState<UserRequest[]>([])
  const [isLoading, setIsLoading] = useState(false)
  const [newContent, setNewContent] = useState('')
  const [taskId, setTaskId] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [isDeletingAll, setIsDeletingAll] = useState(false)
  const [pendingDeletes, setPendingDeletes] = useState<Record<string, boolean>>({})

  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pollControlsRef = useRef<{ schedule: (delay: number) => void; refresh: () => void } | null>(
    null,
  )

  useEffect(() => {
    if (!apiKey) {
      setPending([])
      setConsumed([])
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
        const data = await listUserRequests(apiKey, controller.signal)
        if (disposed) return

        setPending(data.pending ?? [])
        setConsumed(data.consumed ?? [])
        setIdentity({ userId: data.user_id, keyHint: data.key_hint })
        setStatus({
          message: identityMessage(data.user_id, data.key_hint),
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
      pollTimerRef.current = setTimeout(() => fetchData(false), delay)
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

  const handleRefresh = useCallback(() => {
    pollControlsRef.current?.refresh()
  }, [])

  const handleCreateRequest = useCallback(async () => {
    const key = normalizeApiKey(apiKey)
    if (!key) {
      setStatus({ message: 'Connect with your API key before creating requests.', tone: 'error' })
      return
    }
    const trimmed = newContent.trim()
    if (!trimmed) {
      setStatus({ message: 'Content cannot be empty.', tone: 'error' })
      return
    }

    setIsSubmitting(true)
    try {
      await createUserRequest(key, trimmed, taskId.trim() || undefined)
      setNewContent('')
      setStatus({ message: 'Request queued successfully.', tone: 'success' })
      pollControlsRef.current?.schedule(0)
    } catch (error) {
      setStatus({
        message: error instanceof Error ? error.message : 'Failed to create request.',
        tone: 'error',
      })
    } finally {
      setIsSubmitting(false)
    }
  }, [apiKey, newContent, taskId])

  const handleDeleteRequest = useCallback(
    async (requestId: string) => {
      const key = normalizeApiKey(apiKey)
      if (!key) {
        setStatus({ message: 'Connect with your API key before deleting.', tone: 'error' })
        return
      }
      setPendingDeletes((prev) => ({ ...prev, [requestId]: true }))
      try {
        await deleteUserRequest(key, requestId)
        setStatus({ message: 'Request deleted.', tone: 'success' })
        pollControlsRef.current?.schedule(0)
      } catch (error) {
        setStatus({
          message: error instanceof Error ? error.message : 'Failed to delete request.',
          tone: 'error',
        })
      } finally {
        setPendingDeletes((prev) => {
          const next = { ...prev }
          delete next[requestId]
          return next
        })
      }
    },
    [apiKey],
  )

  const handleDeleteAll = useCallback(async () => {
    const key = normalizeApiKey(apiKey)
    if (!key) {
      setStatus({ message: 'Connect with your API key before deleting.', tone: 'error' })
      return
    }
    if (typeof window !== 'undefined') {
      const confirmDelete = window.confirm('Delete all user requests for this API key?')
      if (!confirmDelete) {
        return
      }
    }
    setIsDeletingAll(true)
    try {
      const deleted = await deleteAllUserRequests(key)
      setStatus({
        message: deleted ? `Deleted ${deleted} request${deleted === 1 ? '' : 's'}.` : 'No requests to delete.',
        tone: 'success',
      })
      pollControlsRef.current?.schedule(0)
    } catch (error) {
      setStatus({
        message: error instanceof Error ? error.message : 'Failed to delete requests.',
        tone: 'error',
      })
    } finally {
      setIsDeletingAll(false)
    }
  }, [apiKey])

  const maskedKeySuffix = useMemo(() => {
    if (!identity?.keyHint) return ''
    return `token •••${identity.keyHint}`
  }, [identity])

  const totalRequests = pending.length + consumed.length

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <ClipboardList className="h-4 w-4" />
          <span>MCP Tools</span>
        </div>
        <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
          Get User Requests Console
        </h1>
        <p className="max-w-2xl text-lg text-muted-foreground">
          Queue fresh directives for your AI assistants and inspect everything they have already consumed. Use this page to manage the <code>get_user_request</code> MCP tool inputs tied to your bearer token.
        </p>
      </section>

      <Card className="border border-border/60 bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-lg text-foreground">Authenticate</CardTitle>
          <p className="text-sm text-muted-foreground">
            Enter the bearer token shared with your AI agent. The token stays in your browser storage only.
          </p>
        </CardHeader>
        <CardContent className="space-y-4">
          <ApiKeyInput
            showRefresh
            onRefresh={handleRefresh}
          />
          {status && (
            <StatusBanner status={status} maskedKeySuffix={maskedKeySuffix} />
          )}
        </CardContent>
      </Card>

      <Card className="border border-border/60 bg-card shadow-sm">
        <CardHeader className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
          <div>
            <CardTitle className="text-lg text-foreground">Create user directive</CardTitle>
            <p className="text-sm text-muted-foreground">
              Draft a new instruction for your AI agent. The latest entry is delivered first when they call <code>get_user_request</code>.
            </p>
          </div>
          <Button
            type="button"
            variant="destructive"
            onClick={handleDeleteAll}
            disabled={!totalRequests || isDeletingAll}
          >
            <Trash2 className="mr-2 h-4 w-4" />
            {isDeletingAll ? 'Deleting…' : 'Delete all'}
          </Button>
        </CardHeader>
        <CardContent className="space-y-3">
          <Textarea
            value={newContent}
            onChange={(event: ChangeEvent<HTMLTextAreaElement>) => setNewContent(event.target.value)}
            placeholder="Describe the feedback or task for your AI assistant…"
            disabled={!apiKey || isSubmitting}
          />
          <div className="flex flex-col gap-3 md:flex-row">
            <Input
              value={taskId}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setTaskId(event.target.value)}
              placeholder="Optional task identifier"
              disabled={!apiKey || isSubmitting}
              className="md:flex-1"
            />
            <Button onClick={handleCreateRequest} disabled={isSubmitting || !apiKey}>
              <PlusCircle className="mr-2 h-4 w-4" />
              {isSubmitting ? 'Queuing…' : 'Queue request'}
            </Button>
          </div>
        </CardContent>
      </Card>

      <section className="grid gap-6 lg:grid-cols-2">
        <div className="space-y-4">
          <header className="flex items-center justify-between">
            <h2 className="text-xl font-semibold text-foreground">Pending requests</h2>
            <Badge variant="secondary">{pending.length}</Badge>
          </header>
          <div className="space-y-4">
            {isLoading && !pending.length ? (
              <EmptyState message="Loading pending requests…" />
            ) : pending.length === 0 ? (
              <EmptyState message="No pending directives right now." />
            ) : (
              pending.map((request) => (
                <PendingRequestCard
                  key={request.id}
                  request={request}
                  onDelete={handleDeleteRequest}
                  deleting={Boolean(pendingDeletes[request.id])}
                />
              ))
            )}
          </div>
        </div>
        <div className="space-y-4">
          <header className="flex items-center justify-between">
            <h2 className="text-xl font-semibold text-foreground">Consumed history</h2>
            <Badge variant="outline">{consumed.length}</Badge>
          </header>
          <div className="space-y-4">
            {consumed.length === 0 ? (
              <EmptyState message="Nothing consumed yet." subtle />
            ) : (
              consumed.map((request) => <ConsumedCard key={request.id} request={request} />)
            )}
          </div>
        </div>
      </section>
    </div>
  )
}

function identityMessage(userId?: string, keyHint?: string) {
  const user = userId || 'unknown user'
  const suffix = keyHint ? `token •••${keyHint}` : 'token hidden'
  return `Linked identity: ${user} (${suffix})`
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
        className,
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
        subtle ? 'bg-muted/50' : 'bg-muted',
      )}
    >
      {message}
    </div>
  )
}

function PendingRequestCard({
  request,
  onDelete,
  deleting,
}: {
  request: UserRequest
  onDelete: (id: string) => void
  deleting: boolean
}) {
  return (
    <Card className="border border-primary/30 bg-card shadow-sm">
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>ID: {request.id}</span>
          <span>Queued: {formatDate(request.created_at)}</span>
          {request.task_id && <span>Task: {request.task_id}</span>}
        </div>
        <CardTitle className="text-base font-semibold text-foreground whitespace-pre-wrap">
          {request.content}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex justify-end">
        <Button
          variant="destructive"
          onClick={() => onDelete(request.id)}
          disabled={deleting}
        >
          <Trash2 className="mr-2 h-4 w-4" />
          {deleting ? 'Deleting…' : 'Delete'}
        </Button>
      </CardContent>
    </Card>
  )
}

function ConsumedCard({ request }: { request: UserRequest }) {
  return (
    <Card className="border border-border/60 bg-card">
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>ID: {request.id}</span>
          <span>Queued: {formatDate(request.created_at)}</span>
          {request.consumed_at && <span>Delivered: {formatDate(request.consumed_at)}</span>}
          {request.task_id && <span>Task: {request.task_id}</span>}
        </div>
        <CardTitle className="text-base font-semibold text-foreground whitespace-pre-wrap">
          {request.content}
        </CardTitle>
      </CardHeader>
    </Card>
  )
}

function formatDate(value?: string | null) {
  if (!value) return 'N/A'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
