import { useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { useApiKey } from '@/lib/api-key-context';
import { useToolsConfig } from '@/lib/tools-config-context';
import { fetchGraphQL } from '@/lib/graphql';

const mutation = `mutation ExtractKeyInfo($query: String!, $materials: String!, $topK: Int) {
  ExtractKeyInfo(query: $query, materials: $materials, top_k: $topK) { query created_at contexts }
}`;
type Result = { ExtractKeyInfo: { query: string; created_at: string; contexts: string[] } };

/** Render the shared extraction capability without duplicating RAG business logic. */
export function ExtractKeyInfoPage() {
  const { apiKey, isToolConsoleLocked } = useApiKey();
  const tools = useToolsConfig();
  const enabled = Boolean(apiKey) && !isToolConsoleLocked && tools.extract_key_info;
  return <ExtractionForm key={JSON.stringify([apiKey, enabled])} apiKey={apiKey || ''} enabled={enabled} />;
}

/** ExtractionForm runs one extract_key_info request and renders the returned context chunks. */
function ExtractionForm({ apiKey, enabled }: { apiKey: string; enabled: boolean }) {
  const [query, setQuery] = useState('');
  const [materials, setMaterials] = useState('');
  const [topK, setTopK] = useState('');
  const [result, setResult] = useState<Result['ExtractKeyInfo'] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);
  async function execute(event: React.FormEvent) {
    event.preventDefault();
    if (!enabled || active.current || !query.trim() || !materials.trim()) return;
    const limit = topK.trim() ? Number(topK) : undefined;
    if (limit !== undefined && (!Number.isInteger(limit) || limit < 1 || limit > 2147483647)) {
      setError('Top K must be a positive integer. Leave it blank for the server default.'); return;
    }
    const controller = new AbortController();
    active.current = controller; setRunning(true); setError(null); setResult(null);
    try {
      const response = await fetchGraphQL<Result>(apiKey, mutation, {query, materials, ...(limit === undefined ? {} : {topK:limit})}, controller.signal);
      if (!controller.signal.aborted) setResult(response.ExtractKeyInfo);
    } catch (failure) {
      if (!controller.signal.aborted) setError(failure instanceof Error ? failure.message : 'Extraction failed.');
    } finally {
      if (active.current === controller) active.current = null;
      if (!controller.signal.aborted) setRunning(false);
    }
  }
  return <section className="space-y-6">
    <div><h1 className="text-3xl font-bold">extract_key_info</h1><p className="text-muted-foreground">Retrieve relevant passages from supplied text through the existing GraphQL API.</p></div>
    <Card><CardHeader><CardTitle>Extract key information</CardTitle><CardDescription>Uses the same query, size, authentication and retrieval rules as the MCP tool.</CardDescription></CardHeader>
      <CardContent><form onSubmit={(event) => void execute(event)} className="space-y-4">
        <fieldset disabled={!enabled || running} className="space-y-4">
          <div><label htmlFor="extract-query">Query</label><Input id="extract-query" value={query} onChange={(event) => setQuery(event.target.value)} required /></div>
          <div><label htmlFor="extract-materials">Materials</label><Textarea id="extract-materials" rows={12} value={materials} onChange={(event) => setMaterials(event.target.value)} required /></div>
          <div><label htmlFor="extract-top-k">Top K (optional)</label><Input id="extract-top-k" type="number" step={1} min={1} value={topK} onChange={(event) => setTopK(event.target.value)} placeholder="Server default" /></div>
          <Button type="submit" disabled={!query.trim() || !materials.trim()}>{running ? 'Extracting…' : 'Extract'}</Button>
        </fieldset>
        {!enabled && <p role="status">Unlock the console with an API key. The GraphQL extraction service must also be configured.</p>}
        {error && <p role="alert" className="text-destructive">{error}</p>}
      </form></CardContent>
    </Card>
    {result && <Card><CardHeader><CardTitle>Relevant contexts</CardTitle></CardHeader><CardContent className="space-y-3">
      {result.contexts.length ? result.contexts.map((text, index) => <pre key={index} className="whitespace-pre-wrap break-words rounded-md border p-3 text-sm">{text}</pre>) : <p>No relevant contexts returned.</p>}
    </CardContent></Card>}
  </section>;
}
