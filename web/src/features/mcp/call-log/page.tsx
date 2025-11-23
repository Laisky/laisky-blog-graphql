import { Server } from 'lucide-react'
import type { FormEvent } from 'react'
import { useCallback, useEffect, useMemo, useState } from 'react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import { normalizeApiKey } from '../shared/auth'
import type { CallLogEntry, CallLogListResponse } from './api'
import { fetchCallLogs } from './api'

const STORAGE_KEY = 'call_log_api_key'
const TOOL_OPTIONS: Array<{ label: string; value: string }> = [
  { label: 'All tools', value: 'all' },
  { label: 'web_search', value: 'web_search' },
  { label: 'web_fetch', value: 'web_fetch' },
  { label: 'ask_user', value: 'ask_user' },
]
const SORT_FIELDS: Array<{ label: string; value: string }> = [
  { label: 'Newest first', value: 'occurred_at' },
  { label: 'Cost', value: 'cost' },
  { label: 'Duration', value: 'duration' },
]
const PAGE_SIZE_OPTIONS = [10, 20, 50]

export function CallLogPage() {
  const [apiKey, setApiKey] = useState<string>(() => loadStoredApiKey())
  const [formValue, setFormValue] = useState(apiKey)
  const [isKeyVisible, setIsKeyVisible] = useState(false)
  const [entries, setEntries] = useState<CallLogEntry[]>([])
  const [responseMeta, setResponseMeta] = useState<CallLogListResponse['meta'] | null>(null)
  const [pagination, setPagination] = useState<CallLogListResponse['pagination'] | null>(null)
  const [status, setStatus] = useState<string>('Connect with your API key to view call history.')
  const [error, setError] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(false)

  const [toolFilter, setToolFilter] = useState('all')
  const [userFilter, setUserFilter] = useState('')
  const [fromDate, setFromDate] = useState('')
  const [toDate, setToDate] = useState('')
  const [sortBy, setSortBy] = useState('occurred_at')
  const [sortOrder, setSortOrder] = useState<'ASC' | 'DESC'>('DESC')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [refreshKey, setRefreshKey] = useState(0)

  useEffect(() => {
    if (typeof window === 'undefined') return
    try {
      const normalized = normalizeApiKey(apiKey)
      if (normalized) {
        window.localStorage.setItem(STORAGE_KEY, normalized)
      } else {
        window.localStorage.removeItem(STORAGE_KEY)
      }
    } catch {
      // ignore storage errors
    }
  }, [apiKey])

  useEffect(() => {
    setFormValue(apiKey)
  }, [apiKey])

  useEffect(() => {
    if (!apiKey) {
      setEntries([])
      setPagination(null)
      setStatus('Enter an API key to load call logs.')
      return
    }

    const controller = new AbortController()
    const query = buildQuery({
      page,
      pageSize,
      sortBy,
      sortOrder,
      tool: toolFilter !== 'all' ? toolFilter : undefined,
      user: userFilter.trim() || undefined,
      from: fromDate || undefined,
      to: toDate || undefined,
    })

    setIsLoading(true)
    setError(null)

    fetchCallLogs(apiKey, query, controller.signal)
      .then((data) => {
        if (controller.signal.aborted) return
        setEntries(data.data)
        setPagination(data.pagination)
        setResponseMeta(data.meta)
        setPage(data.pagination.page)
        setStatus(`Loaded ${data.data.length} entries.`)
      })
      .catch((err) => {
        if (controller.signal.aborted) return
        setEntries([])
        setPagination(null)
        setResponseMeta(null)
        setError(err instanceof Error ? err.message : 'Failed to load call logs.')
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setIsLoading(false)
        }
      })

    return () => controller.abort()
  }, [apiKey, page, pageSize, sortBy, sortOrder, toolFilter, userFilter, fromDate, toDate, refreshKey])

  const handleAuthSubmit = useCallback(
    (event: FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      const normalized = normalizeApiKey(formValue)
      if (!normalized) {
        setStatus('Please provide an API key to connect.')
        return
      }
      if (normalized === apiKey) {
        setStatus('Already connected with this API key.')
        return
      }
      setApiKey(normalized)
      setPage(1)
      setStatus('Connecting…')
    },
    [apiKey, formValue]
  )

  const handleDisconnect = useCallback(() => {
    setApiKey('')
    setFormValue('')
    setPage(1)
    setEntries([])
    setPagination(null)
    setResponseMeta(null)
    setStatus('Disconnected.')
  }, [])

  const quotesPerUsd = responseMeta?.quotes_per_usd ?? 0
  const costFormatter = useMemo(() => new Intl.NumberFormat(undefined, { minimumFractionDigits: 4, maximumFractionDigits: 4 }), [])
  const dateFormatter = useMemo(
    () =>
      new Intl.DateTimeFormat(undefined, {
        dateStyle: 'medium',
        timeStyle: 'medium',
      }),
    []
  )

  const displayedPage = pagination?.page ?? page
  const totalPages = pagination?.total_pages ?? 0

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <Server className="h-4 w-4" />
          <span>MCP Tools</span>
        </div>
        <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
          Call Log
        </h1>
        <p className="max-w-2xl text-lg text-muted-foreground">
          Review every MCP tool invocation associated with your bearer token. Filter by tool, time range, or
          API key prefix to monitor usage and costs.
        </p>
      </section>

      <Card className="border border-border/60 bg-card shadow-sm">
        <CardHeader className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
          <div>
            <CardTitle className="text-lg text-foreground">Authenticate</CardTitle>
            <p className="text-sm text-muted-foreground">
              Enter the bearer token assigned to your agent. The API key is stored locally only.
            </p>
          </div>
          {apiKey && (
            <Button variant="outline" onClick={handleDisconnect}>
              Disconnect
            </Button>
          )}
        </CardHeader>
        <CardContent>
          <form onSubmit={handleAuthSubmit} className="flex flex-col gap-3 md:flex-row md:items-center">
            <div className="relative w-full md:max-w-md">
              <Input
                value={formValue}
                onChange={(event) => setFormValue(event.target.value)}
                type={isKeyVisible ? 'text' : 'password'}
                placeholder="Enter your API key"
                autoComplete="off"
                className="pr-28"
                required
              />
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setIsKeyVisible((prev) => !prev)}
                className="absolute right-1 top-1/2 -translate-y-1/2 px-3"
              >
                {isKeyVisible ? 'Hide' : 'Show'}
              </Button>
            </div>
            <div className="flex gap-2">
              <Button type="submit">Connect</Button>
              {apiKey && (
                <Button type="button" variant="secondary" onClick={() => setRefreshKey((prev) => prev + 1)}>
                  Refresh
                </Button>
              )}
            </div>
          </form>
          <div className="mt-3 text-sm text-muted-foreground">{status}</div>
        </CardContent>
      </Card>

      <Card className="border border-border/60 bg-card">
        <CardHeader>
          <CardTitle className="text-lg text-foreground">Filters</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
            <div className="space-y-1">
              <label className="text-xs font-medium uppercase tracking-widest text-muted-foreground">Tool</label>
              <select
                value={toolFilter}
                onChange={(event) => {
                  setToolFilter(event.target.value)
                  setPage(1)
                }}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground shadow-sm focus:border-ring focus:outline-none"
              >
                {TOOL_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium uppercase tracking-widest text-muted-foreground">User prefix</label>
              <Input
                value={userFilter}
                onChange={(event) => {
                  setUserFilter(event.target.value)
                  setPage(1)
                }}
                placeholder="First 7 characters"
              />
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium uppercase tracking-widest text-muted-foreground">Sort by</label>
              <select
                value={sortBy}
                onChange={(event) => {
                  setSortBy(event.target.value)
                  setPage(1)
                }}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground shadow-sm focus:border-ring focus:outline-none"
              >
                {SORT_FIELDS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium uppercase tracking-widest text-muted-foreground">Sort order</label>
              <select
                value={sortOrder}
                onChange={(event) => {
                  setSortOrder(event.target.value as 'ASC' | 'DESC')
                  setPage(1)
                }}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground shadow-sm focus:border-ring focus:outline-none"
              >
                <option value="DESC">Descending</option>
                <option value="ASC">Ascending</option>
              </select>
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium uppercase tracking-widest text-muted-foreground">From date</label>
              <Input
                value={fromDate}
                onChange={(event) => {
                  setFromDate(event.target.value)
                  setPage(1)
                }}
                type="date"
              />
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium uppercase tracking-widest text-muted-foreground">To date</label>
              <Input
                value={toDate}
                onChange={(event) => {
                  setToDate(event.target.value)
                  setPage(1)
                }}
                type="date"
              />
            </div>
            <div className="space-y-1">
              <label className="text-xs font-medium uppercase tracking-widest text-muted-foreground">Page size</label>
              <select
                value={pageSize}
                onChange={(event) => {
                  setPageSize(Number(event.target.value))
                  setPage(1)
                }}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground shadow-sm focus:border-ring focus:outline-none"
              >
                {PAGE_SIZE_OPTIONS.map((size) => (
                  <option key={size} value={size}>
                    {size} / page
                  </option>
                ))}
              </select>
            </div>
          </div>
          {error && <div className="text-sm text-destructive">{error}</div>}
        </CardContent>
      </Card>

      <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
        <div className="flex items-center justify-between border-b border-border/60 px-4 py-3 text-sm text-muted-foreground">
          <div>
            Page {displayedPage} of {totalPages || 1}
            {pagination ? ` • ${pagination.total_items} total entries` : ''}
          </div>
          {quotesPerUsd > 0 && (
            <div className="text-xs text-muted-foreground/80">
              {quotesPerUsd.toLocaleString()} credits = 1 USD
            </div>
          )}
        </div>
        <div className="relative overflow-x-auto">
          <table className="min-w-full divide-y divide-border/60 text-sm">
            <thead className="bg-muted/40">
              <tr className="text-left uppercase tracking-widest text-xs text-muted-foreground">
                <th className="px-4 py-3 font-medium">Occurred</th>
                <th className="px-4 py-3 font-medium">Tool</th>
                <th className="px-4 py-3 font-medium">User</th>
                <th className="px-4 py-3 font-medium">Cost</th>
                <th className="px-4 py-3 font-medium">Duration</th>
                <th className="px-4 py-3 font-medium">Status</th>
                <th className="px-4 py-3 font-medium">Parameters</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border/60 text-foreground">
              {entries.length === 0 ? (
                <tr>
                  <td colSpan={7} className="px-4 py-6 text-center text-sm text-muted-foreground">
                    {isLoading ? 'Loading call logs…' : 'No call logs available for the selected filters.'}
                  </td>
                </tr>
              ) : (
                entries.map((entry) => (
                  <tr key={entry.id} className="align-top">
                    <td className="px-4 py-3 whitespace-nowrap">{formatTimestamp(entry.occurred_at, dateFormatter)}</td>
                    <td className="px-4 py-3">
                      <span className="font-medium text-foreground">{entry.tool}</span>
                    </td>
                    <td className="px-4 py-3">
                      <code className="rounded bg-muted px-2 py-1 text-xs text-muted-foreground">{entry.user_prefix || 'unknown'}</code>
                    </td>
                    <td className="px-4 py-3 whitespace-nowrap">
                      {formatCost(entry.cost_credits, quotesPerUsd, costFormatter)}
                    </td>
                    <td className="px-4 py-3 whitespace-nowrap">{formatDuration(entry.duration_ms)}</td>
                    <td className="px-4 py-3">
                      <StatusBadge status={entry.status} error={entry.error} />
                    </td>
                    <td className="px-4 py-3">
                      <ParameterPreview parameters={entry.parameters} error={entry.error} />
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
          {isLoading && (
            <div className="absolute inset-0 flex items-center justify-center bg-background/70 text-sm text-muted-foreground">
              Loading…
            </div>
          )}
        </div>
        <div className="flex items-center justify-between border-t border-border/60 px-4 py-3 text-sm text-muted-foreground">
          <div>
            Showing {entries.length} of {pagination?.total_items ?? 0} entries
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={displayedPage <= 1 || isLoading}
              onClick={() => setPage((prev) => Math.max(1, prev - 1))}
            >
              Previous
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={Boolean(totalPages && displayedPage >= totalPages) || isLoading}
              onClick={() => setPage((prev) => prev + 1)}
            >
              Next
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

function loadStoredApiKey(): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY)
    return normalizeApiKey(stored ?? '')
  } catch {
    return ''
  }
}

type CallLogQueryInput = {
  page?: number
  pageSize?: number
  sortBy?: string
  sortOrder?: 'ASC' | 'DESC'
  tool?: string
  user?: string
  from?: string
  to?: string
}

function buildQuery(input: CallLogQueryInput) {
  return {
    page: input.page && input.page > 0 ? input.page : undefined,
    pageSize: input.pageSize && input.pageSize > 0 ? input.pageSize : undefined,
    sortBy: input.sortBy,
    sortOrder: input.sortOrder,
    tool: input.tool,
    user: input.user,
    from: input.from,
    to: input.to,
  }
}

function formatTimestamp(value: string, formatter: Intl.DateTimeFormat): string {
  if (!value) return '—'
  const time = new Date(value)
  if (Number.isNaN(time.getTime())) return value
  return formatter.format(time)
}

function formatCost(value: number, quotesPerUsd: number, formatter: Intl.NumberFormat): string {
  if (!value) {
    return '0 ($0.0000)'
  }
  const usd = quotesPerUsd > 0 ? value / quotesPerUsd : 0
  return `${value.toLocaleString()} ($${formatter.format(usd)})`
}

function formatDuration(ms: number): string {
  if (!ms) return '0 ms'
  if (ms < 1000) return `${ms} ms`
  const seconds = ms / 1000
  if (seconds < 60) return `${seconds.toFixed(2)} s`
  const minutes = seconds / 60
  return `${minutes.toFixed(2)} min`
}

function StatusBadge({ status, error }: { status: string; error?: string | null }) {
  const isSuccess = status === 'success'
  const label = isSuccess ? 'Success' : 'Error'
  const badgeClass = isSuccess ? '' : 'border-destructive/60 text-destructive'
  return (
    <div className="space-y-1">
      <Badge variant={isSuccess ? 'secondary' : 'outline'} className={badgeClass}>
        {label}
      </Badge>
      {!isSuccess && error ? (
        <p className="max-w-xs text-xs text-muted-foreground/80">{error}</p>
      ) : null}
    </div>
  )
}

function ParameterPreview({ parameters, error }: { parameters: Record<string, unknown>; error?: string | null }) {
  const text = JSON.stringify(parameters ?? {}, null, 2)
  const isEmpty = text === '{}' || !text
  return (
    <div
      className={cn(
        'max-h-48 overflow-auto rounded border border-border/60 bg-muted/40 px-3 py-2 font-mono text-xs leading-relaxed text-muted-foreground',
        isEmpty && 'italic text-muted-foreground/70'
      )}
    >
      {isEmpty ? (error ? 'Error without parameters.' : '—') : text}
    </div>
  )
}
