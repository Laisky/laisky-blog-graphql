import { Globe, Loader2, Play } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { useApiKey } from '@/lib/api-key-context';
import { fetchGraphQL } from '@/lib/graphql';

import type { CallLogEntry, CallLogListResponse } from '../call-log/api';
import { fetchCallLogs } from '../call-log/api';

interface WebFetchResponse {
  WebFetch: {
    url: string;
    created_at: string;
    content: string;
  };
}

const WEB_FETCH_MUTATION = `
  mutation WebFetch($url: String!) {
    WebFetch(url: $url) {
      url
      created_at
      content
    }
  }
`;

export function WebFetchPage() {
  const { apiKey } = useApiKey();
  const [entries, setEntries] = useState<CallLogEntry[]>([]);
  const [pagination, setPagination] = useState<CallLogListResponse['pagination'] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [page, setPage] = useState(1);
  const pageSize = 10;

  // Tool execution state
  const [url, setUrl] = useState('');
  const [isExecuting, setIsExecuting] = useState(false);
  const [lastResult, setLastResult] = useState<WebFetchResponse['WebFetch'] | null>(null);
  const [execError, setExecError] = useState<string | null>(null);

  useEffect(() => {
    if (!apiKey) {
      setEntries([]);
      setPagination(null);
      return;
    }

    const controller = new AbortController();
    setIsLoading(true);
    setError(null);

    fetchCallLogs(
      apiKey,
      {
        page,
        pageSize,
        tool: 'web_fetch',
        sortBy: 'occurred_at',
        sortOrder: 'DESC',
      },
      controller.signal
    )
      .then((data) => {
        if (controller.signal.aborted) return;
        setEntries(data.data);
        setPagination(data.pagination);
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        setEntries([]);
        setPagination(null);
        setError(err instanceof Error ? err.message : 'Failed to load logs.');
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setIsLoading(false);
        }
      });

    return () => controller.abort();
  }, [apiKey, page]);

  const handleExecute = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!url.trim() || isExecuting) return;

    setIsExecuting(true);
    setExecError(null);
    setLastResult(null);

    try {
      const data = await fetchGraphQL<WebFetchResponse>(apiKey, WEB_FETCH_MUTATION, { url });
      setLastResult(data.WebFetch);
      // Refresh logs after execution
      setPage(1);
    } catch (err) {
      setExecError(err instanceof Error ? err.message : 'Execution failed');
    } finally {
      setIsExecuting(false);
    }
  };

  const dateFormatter = useMemo(
    () =>
      new Intl.DateTimeFormat(undefined, {
        dateStyle: 'medium',
        timeStyle: 'medium',
      }),
    []
  );

  const totalPages = pagination?.total_pages ?? 0;

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <Globe className="h-4 w-4" />
          <span>Tool Console</span>
        </div>
        <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">web_fetch</h1>
        <p className="max-w-2xl text-lg text-muted-foreground">
          Fetches and renders dynamic web pages using a headless browser. This tool helps AI models extract content from modern websites
          that require JavaScript.
        </p>
      </section>

      <div className="flex flex-col gap-8">
        {/* Tool Interaction */}
        <section className="space-y-6">
          <Card className="border border-border/60 bg-card">
            <CardHeader>
              <CardTitle className="text-xl">Execute web_fetch</CardTitle>
              <CardDescription>Directly test the URL fetching capability via GraphQL.</CardDescription>
            </CardHeader>
            <CardContent>
              <form onSubmit={handleExecute} className="space-y-4">
                <div className="flex gap-2">
                  <Input
                    placeholder="Enter URL (e.g. https://example.com)..."
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                    disabled={isExecuting}
                    className="flex-1"
                  />
                  <Button type="submit" disabled={isExecuting || !url.trim()}>
                    {isExecuting ? (
                      <>
                        <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                        Fetching
                      </>
                    ) : (
                      <>
                        <Play className="mr-2 h-4 w-4" />
                        Run Fetch
                      </>
                    )}
                  </Button>
                </div>

                {execError && <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">{execError}</div>}

                {lastResult && (
                  <div className="mt-6 space-y-4">
                    <h3 className="text-sm font-medium text-foreground">Fetched Content:</h3>
                    <div className="max-h-[600px] overflow-y-auto rounded-md border border-border/60 bg-muted/30 p-4">
                      <pre className="whitespace-pre-wrap font-sans text-sm leading-relaxed text-muted-foreground">
                        {lastResult.content}
                      </pre>
                    </div>
                  </div>
                )}
              </form>
            </CardContent>
          </Card>
        </section>

        {/* Usage History */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold text-foreground">Usage History</h2>
          <Card className="border border-border/60 bg-card overflow-hidden">
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="bg-muted text-muted-foreground">
                  <tr>
                    <th className="px-4 py-3 font-medium uppercase tracking-wider">Timestamp</th>
                    <th className="px-4 py-3 font-medium uppercase tracking-wider">Parameters</th>
                    <th className="px-4 py-3 font-medium uppercase tracking-wider text-right">Cost (USD)</th>
                    <th className="px-4 py-3 font-medium uppercase tracking-wider text-right">Duration</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/60">
                  {isLoading && entries.length === 0 ? (
                    <tr>
                      <td colSpan={4} className="px-4 py-8 text-center text-muted-foreground italic">
                        Loading activity…
                      </td>
                    </tr>
                  ) : error ? (
                    <tr>
                      <td colSpan={4} className="px-4 py-8 text-center text-destructive italic">
                        {error}
                      </td>
                    </tr>
                  ) : entries.length === 0 ? (
                    <tr>
                      <td colSpan={4} className="px-4 py-8 text-center text-muted-foreground italic">
                        No matching records found.
                      </td>
                    </tr>
                  ) : (
                    entries.map((entry) => (
                      <tr key={entry.id} className="transition-colors hover:bg-muted/30">
                        <td className="whitespace-nowrap px-4 py-3 font-mono text-xs">
                          <span title={new Date(entry.occurred_at).toISOString()}>{dateFormatter.format(new Date(entry.occurred_at))}</span>
                        </td>
                        <td className="px-4 py-3">
                          <div className="flex flex-wrap gap-1">
                            {Object.entries(entry.parameters || {}).map(([key, value]) => (
                              <Badge key={key} variant="secondary" className="text-[10px] py-0 px-1 opacity-70">
                                {key}: {String(value)}
                              </Badge>
                            ))}
                          </div>
                        </td>
                        <td className="px-4 py-3 text-right">
                          <Badge variant="outline" className="font-mono text-xs">
                            ${entry.cost_usd}
                          </Badge>
                        </td>
                        <td className="whitespace-nowrap px-4 py-3 text-right tabular-nums text-muted-foreground">
                          {entry.duration_ms.toLocaleString()} ms
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </Card>

          {totalPages > 1 && (
            <div className="flex items-center justify-between">
              <p className="text-sm text-muted-foreground">
                Page {page} of {totalPages}
              </p>
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={() => setPage((p) => Math.max(1, p - 1))} disabled={page <= 1}>
                  Previous
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                  disabled={page >= totalPages}
                >
                  Next
                </Button>
              </div>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
