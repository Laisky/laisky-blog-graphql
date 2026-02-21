import { Brain, Database, RefreshCw, Wrench } from 'lucide-react';
import { useState } from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { useApiKey } from '@/lib/api-key-context';

import { callMcpTool, type CallToolResponse } from '../shared/mcp-api';
import { useMemoryInputDefaults, usePersistMemoryInputs } from './use-memory-input-storage';

type StructuredToolResponse<T> = CallToolResponse & {
  structured?: T;
  structuredContent?: T;
  structured_content?: T;
};

type MemoryResponseItem = Record<string, unknown>;

type BeforeTurnPayload = {
  input_items: MemoryResponseItem[];
  recall_fact_ids: string[];
  context_token_count: number;
};

type MaintenancePayload = {
  ok: boolean;
};

type ListDirSummary = {
  path: string;
  abstract?: string;
  updated_at?: string;
  has_overview?: boolean;
};

type ListDirPayload = {
  summaries: ListDirSummary[];
};

const dateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

const inputLabelClass = 'text-xs font-medium uppercase tracking-wide text-muted-foreground';
const defaultMemoryProject = 'default';
const defaultMemorySessionID = 'default';

function generateClientTurnID(): string {
  const now = Date.now();
  const randomBytes = new Uint8Array(3);

  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    crypto.getRandomValues(randomBytes);
  } else {
    randomBytes[0] = Math.floor(Math.random() * 256);
    randomBytes[1] = Math.floor(Math.random() * 256);
    randomBytes[2] = Math.floor(Math.random() * 256);
  }

  const suffix = Array.from(randomBytes)
    .map((value) => value.toString(16).padStart(2, '0'))
    .join('');

  return `turn-${now}-${suffix}`;
}

function extractStructuredPayload<T>(result: CallToolResponse): T {
  const structuredResult = result as StructuredToolResponse<T>;
  const structured = structuredResult.structured ?? structuredResult.structuredContent ?? structuredResult.structured_content;
  if (structured) {
    return structured as T;
  }

  const contentText = result.content?.find((item) => typeof item.text === 'string')?.text;
  if (!contentText) {
    throw new Error('Tool response missing structured payload');
  }

  return JSON.parse(contentText) as T;
}

function extractToolError(result: CallToolResponse): string {
  const contentText = result.content?.find((item) => typeof item.text === 'string')?.text;
  if (!contentText) {
    return 'Tool execution failed.';
  }

  try {
    const parsed = JSON.parse(contentText) as { message?: string };
    if (parsed?.message) {
      return parsed.message;
    }
  } catch {
    return contentText;
  }

  return contentText;
}

function formatTimestamp(value: string | undefined): string {
  if (!value) {
    return '—';
  }

  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }

  return dateFormatter.format(parsed);
}

function parseJSONArray(text: string, fieldName: string): MemoryResponseItem[] {
  const trimmed = text.trim();
  if (!trimmed) {
    return [];
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Invalid JSON';
    throw new Error(`${fieldName} is not valid JSON: ${message}`);
  }

  if (!Array.isArray(parsed)) {
    throw new Error(`${fieldName} must be a JSON array.`);
  }

  return parsed as MemoryResponseItem[];
}

export function MemoryPage() {
  const { apiKey } = useApiKey();
  const persistedInputs = useMemoryInputDefaults();

  const [project, setProject] = useState((persistedInputs.project ?? '').trim() || defaultMemoryProject);
  const [sessionId, setSessionId] = useState((persistedInputs.sessionId ?? '').trim() || defaultMemorySessionID);
  const [userId, setUserId] = useState(persistedInputs.userId ?? '');
  const [turnId, setTurnId] = useState((persistedInputs.turnId ?? '').trim() || generateClientTurnID());

  const [maxInputTok, setMaxInputTok] = useState(persistedInputs.maxInputTok ?? 120000);
  const [baseInstructions, setBaseInstructions] = useState(persistedInputs.baseInstructions ?? '');
  const [currentInputText, setCurrentInputText] = useState(
    persistedInputs.currentInputText ??
      '[\n  {\n    "type": "message",\n    "role": "user",\n    "content": [\n      { "type": "input_text", "text": "Hello" }\n    ]\n  }\n]'
  );

  const [inputItemsText, setInputItemsText] = useState(persistedInputs.inputItemsText ?? '[]');
  const [outputItemsText, setOutputItemsText] = useState(
    persistedInputs.outputItemsText ??
      '[\n  {\n    "type": "message",\n    "role": "assistant",\n    "content": [\n      { "type": "output_text", "text": "Hi, how can I help?" }\n    ]\n  }\n]'
  );

  const [listPath, setListPath] = useState(persistedInputs.listPath ?? '');
  const [listDepth, setListDepth] = useState(persistedInputs.listDepth ?? 8);
  const [listLimit, setListLimit] = useState(persistedInputs.listLimit ?? 200);

  const [beforeError, setBeforeError] = useState<string | null>(null);
  const [beforePayload, setBeforePayload] = useState<BeforeTurnPayload | null>(null);
  const [isBeforeRunning, setIsBeforeRunning] = useState(false);

  const [afterError, setAfterError] = useState<string | null>(null);
  const [afterInfo, setAfterInfo] = useState<string | null>(null);
  const [isAfterRunning, setIsAfterRunning] = useState(false);

  const [maintenanceError, setMaintenanceError] = useState<string | null>(null);
  const [maintenanceInfo, setMaintenanceInfo] = useState<string | null>(null);
  const [isMaintenanceRunning, setIsMaintenanceRunning] = useState(false);

  const [queryError, setQueryError] = useState<string | null>(null);
  const [listPayload, setListPayload] = useState<ListDirPayload | null>(null);
  const [isListing, setIsListing] = useState(false);

  usePersistMemoryInputs({
    project,
    sessionId,
    userId,
    turnId,
    maxInputTok,
    baseInstructions,
    currentInputText,
    inputItemsText,
    outputItemsText,
    listPath,
    listDepth,
    listLimit,
  });

  async function callTool<T>(toolName: string, args: Record<string, unknown>) {
    if (!apiKey) {
      throw new Error('API key is required');
    }

    const result = await callMcpTool(apiKey, toolName, args);
    if (result.isError) {
      throw new Error(extractToolError(result));
    }

    return extractStructuredPayload<T>(result);
  }

  async function runBeforeTurn() {
    setBeforeError(null);
    setBeforePayload(null);
    setIsBeforeRunning(true);

    try {
      const payload = await callTool<BeforeTurnPayload>('memory_before_turn', {
        project: project.trim() || undefined,
        session_id: sessionId.trim() || undefined,
        user_id: userId || undefined,
        turn_id: turnId.trim() || undefined,
        current_input: parseJSONArray(currentInputText, 'current_input'),
        base_instructions: baseInstructions || undefined,
        max_input_tok: Number.isFinite(maxInputTok) && maxInputTok > 0 ? maxInputTok : undefined,
      });

      setBeforePayload(payload);
      setInputItemsText(JSON.stringify(payload.input_items ?? [], null, 2));
    } catch (error) {
      setBeforeError(error instanceof Error ? error.message : 'Failed to run memory_before_turn.');
    } finally {
      setIsBeforeRunning(false);
    }
  }

  async function runAfterTurn() {
    setAfterError(null);
    setAfterInfo(null);
    setIsAfterRunning(true);

    try {
      const payload = await callTool<MaintenancePayload>('memory_after_turn', {
        project: project.trim() || undefined,
        session_id: sessionId.trim() || undefined,
        user_id: userId || undefined,
        turn_id: turnId.trim() || undefined,
        input_items: parseJSONArray(inputItemsText, 'input_items'),
        output_items: parseJSONArray(outputItemsText, 'output_items'),
      });

      if (payload.ok) {
        setAfterInfo('memory_after_turn completed successfully.');
      } else {
        setAfterInfo('memory_after_turn returned without error.');
      }
    } catch (error) {
      setAfterError(error instanceof Error ? error.message : 'Failed to run memory_after_turn.');
    } finally {
      setIsAfterRunning(false);
    }
  }

  async function runMaintenance() {
    setMaintenanceError(null);
    setMaintenanceInfo(null);
    setIsMaintenanceRunning(true);

    try {
      const payload = await callTool<MaintenancePayload>('memory_run_maintenance', {
        project: project.trim() || undefined,
        session_id: sessionId.trim() || undefined,
      });

      setMaintenanceInfo(payload.ok ? 'maintenance completed successfully.' : 'maintenance finished without explicit ok=true.');
    } catch (error) {
      setMaintenanceError(error instanceof Error ? error.message : 'Failed to run maintenance.');
    } finally {
      setIsMaintenanceRunning(false);
    }
  }

  async function runListDir() {
    setQueryError(null);
    setListPayload(null);
    setIsListing(true);

    try {
      const payload = await callTool<ListDirPayload>('memory_list_dir_with_abstract', {
        project: project.trim() || undefined,
        session_id: sessionId.trim() || undefined,
        path: listPath || undefined,
        depth: Number.isFinite(listDepth) && listDepth > 0 ? listDepth : undefined,
        limit: Number.isFinite(listLimit) && listLimit > 0 ? listLimit : undefined,
      });

      setListPayload(payload);
    } catch (error) {
      setQueryError(error instanceof Error ? error.message : 'Failed to list memory directories.');
    } finally {
      setIsListing(false);
    }
  }

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <Brain className="h-4 w-4" />
          <span>Memory Console</span>
        </div>
        <p className="max-w-3xl text-sm text-muted-foreground">
          Manage server-side memory lifecycle with MCP tools: <strong>before_turn</strong>, <strong>after_turn</strong>,{' '}
          <strong>run_maintenance</strong>, and <strong>list_dir_with_abstract</strong>.
        </p>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          Detailed user manual:
          <a
            href="https://github.com/Laisky/laisky-blog-graphql/blob/master/docs/manual/mcp_memory.md"
            target="_blank"
            rel="noopener noreferrer"
            className="font-mono text-xs text-primary underline-offset-4 hover:underline"
          >
            docs/manual/mcp_memory.md
          </a>
        </div>
      </section>

      <Card className="border border-border/60 bg-card">
        <CardHeader>
          <CardTitle className="text-xl">Session Context</CardTitle>
          <CardDescription>Optional identifiers with client/server defaults.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3 md:grid-cols-2 lg:grid-cols-4">
          <div className="space-y-1">
            <label htmlFor="memory-project" className={inputLabelClass}>
              Project
            </label>
            <Input
              id="memory-project"
              placeholder="Default: default"
              value={project}
              onChange={(event) => setProject(event.target.value)}
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="memory-session-id" className={inputLabelClass}>
              Session ID
            </label>
            <Input
              id="memory-session-id"
              placeholder="Default: default"
              value={sessionId}
              onChange={(event) => setSessionId(event.target.value)}
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="memory-user-id" className={inputLabelClass}>
              User ID
            </label>
            <Input id="memory-user-id" placeholder="Optional" value={userId} onChange={(event) => setUserId(event.target.value)} />
          </div>
          <div className="space-y-1">
            <label htmlFor="memory-turn-id" className={inputLabelClass}>
              Turn ID
            </label>
            <Input
              id="memory-turn-id"
              placeholder="Auto-generated if empty"
              value={turnId}
              onChange={(event) => setTurnId(event.target.value)}
            />
          </div>
        </CardContent>
      </Card>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">before_turn (Query)</CardTitle>
            <CardDescription>Prepare model input with recalled memory context.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1">
                <label htmlFor="memory-max-input-tok" className={inputLabelClass}>
                  max_input_tok
                </label>
                <Input
                  id="memory-max-input-tok"
                  type="number"
                  min={1}
                  value={maxInputTok}
                  onChange={(event) => setMaxInputTok(Number(event.target.value))}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="memory-base-instructions" className={inputLabelClass}>
                  base_instructions
                </label>
                <Input
                  id="memory-base-instructions"
                  placeholder="Optional"
                  value={baseInstructions}
                  onChange={(event) => setBaseInstructions(event.target.value)}
                />
              </div>
            </div>

            <div className="space-y-1">
              <label htmlFor="memory-current-input" className={inputLabelClass}>
                current_input (JSON array)
              </label>
              <Textarea
                id="memory-current-input"
                className="min-h-40 font-mono text-xs"
                value={currentInputText}
                onChange={(event) => setCurrentInputText(event.target.value)}
              />
            </div>

            <div className="flex flex-wrap items-center gap-3">
              <Button onClick={() => void runBeforeTurn()} disabled={isBeforeRunning}>
                {isBeforeRunning ? 'Running…' : 'Run memory_before_turn'}
              </Button>
              {beforePayload && <Badge variant="secondary">context_token_count: {beforePayload.context_token_count ?? 0}</Badge>}
            </div>

            {beforeError && <p className="text-sm text-destructive">{beforeError}</p>}

            <div className="space-y-1">
              <label htmlFor="memory-before-input-items" className={inputLabelClass}>
                input_items (Result)
              </label>
              <Textarea
                id="memory-before-input-items"
                className="min-h-48 font-mono text-xs"
                value={inputItemsText}
                onChange={(event) => setInputItemsText(event.target.value)}
              />
            </div>

            {beforePayload && (
              <div className="rounded-lg border border-border/60 bg-muted/30 p-3">
                <div className="mb-2 flex items-center gap-2 text-xs uppercase tracking-wide text-muted-foreground">
                  <Database className="h-3.5 w-3.5" />
                  recall_fact_ids
                </div>
                <div className="flex flex-wrap gap-2">
                  {(beforePayload.recall_fact_ids ?? []).length === 0 ? (
                    <span className="text-sm text-muted-foreground">No recalled facts.</span>
                  ) : (
                    beforePayload.recall_fact_ids.map((factID) => (
                      <Badge key={factID} variant="outline" className="font-mono text-xs">
                        {factID}
                      </Badge>
                    ))
                  )}
                </div>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">after_turn (Write)</CardTitle>
            <CardDescription>Persist turn artifacts into memory.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1">
              <label htmlFor="memory-after-input-items" className={inputLabelClass}>
                input_items (JSON array)
              </label>
              <Textarea
                id="memory-after-input-items"
                className="min-h-40 font-mono text-xs"
                value={inputItemsText}
                onChange={(event) => setInputItemsText(event.target.value)}
              />
            </div>

            <div className="space-y-1">
              <label htmlFor="memory-output-items" className={inputLabelClass}>
                output_items (JSON array)
              </label>
              <Textarea
                id="memory-output-items"
                className="min-h-40 font-mono text-xs"
                value={outputItemsText}
                onChange={(event) => setOutputItemsText(event.target.value)}
              />
            </div>

            <div className="flex flex-wrap items-center gap-3">
              <Button onClick={() => void runAfterTurn()} disabled={isAfterRunning}>
                {isAfterRunning ? 'Running…' : 'Run memory_after_turn'}
              </Button>
              {afterInfo && <Badge variant="secondary">{afterInfo}</Badge>}
            </div>
            {afterError && <p className="text-sm text-destructive">{afterError}</p>}
          </CardContent>
        </Card>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">Maintenance</CardTitle>
            <CardDescription>Run compaction, retention sweep, and summary refresh.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Button onClick={() => void runMaintenance()} disabled={isMaintenanceRunning}>
              {isMaintenanceRunning ? (
                <span className="inline-flex items-center gap-2">
                  <RefreshCw className="h-4 w-4 animate-spin" />
                  Running maintenance…
                </span>
              ) : (
                <span className="inline-flex items-center gap-2">
                  <Wrench className="h-4 w-4" />
                  Run memory_run_maintenance
                </span>
              )}
            </Button>
            {maintenanceInfo && <p className="text-sm text-emerald-600 dark:text-emerald-400">{maintenanceInfo}</p>}
            {maintenanceError && <p className="text-sm text-destructive">{maintenanceError}</p>}
          </CardContent>
        </Card>

        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">list_dir_with_abstract (Query)</CardTitle>
            <CardDescription>Inspect memory directory summaries for a session.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-3 md:grid-cols-3">
              <div className="space-y-1 md:col-span-3">
                <label htmlFor="memory-list-path" className={inputLabelClass}>
                  path
                </label>
                <Input
                  id="memory-list-path"
                  placeholder="Empty means session root"
                  value={listPath}
                  onChange={(event) => setListPath(event.target.value)}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="memory-list-depth" className={inputLabelClass}>
                  depth
                </label>
                <Input
                  id="memory-list-depth"
                  type="number"
                  min={1}
                  value={listDepth}
                  onChange={(event) => setListDepth(Number(event.target.value))}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="memory-list-limit" className={inputLabelClass}>
                  limit
                </label>
                <Input
                  id="memory-list-limit"
                  type="number"
                  min={1}
                  value={listLimit}
                  onChange={(event) => setListLimit(Number(event.target.value))}
                />
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-3">
              <Button onClick={() => void runListDir()} disabled={isListing}>
                {isListing ? 'Querying…' : 'Run memory_list_dir_with_abstract'}
              </Button>
              <Badge variant="outline">{listPayload?.summaries?.length ?? 0} summaries</Badge>
            </div>

            {queryError && <p className="text-sm text-destructive">{queryError}</p>}

            <div className="max-h-80 space-y-2 overflow-y-auto rounded-lg border border-border/60 bg-muted/30 p-3">
              {(listPayload?.summaries ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">No summary records loaded.</p>
              ) : (
                listPayload!.summaries.map((summary) => (
                  <div key={summary.path} className="space-y-1 rounded-md border border-border/60 bg-background p-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-xs text-foreground">{summary.path}</span>
                      {summary.has_overview ? <Badge variant="secondary">overview</Badge> : <Badge variant="outline">no overview</Badge>}
                    </div>
                    <p className="text-xs text-muted-foreground">Updated: {formatTimestamp(summary.updated_at)}</p>
                    <p className="whitespace-pre-wrap break-words text-sm text-foreground">{summary.abstract?.trim() || '—'}</p>
                  </div>
                ))
              )}
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
