import { Activity, ShieldAlert, ShieldCheck } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useLocation } from 'react-router-dom';

import { useApiKey } from '@/lib/api-key-context';

const inspectorScriptModules = import.meta.glob<string>(
  '../../../../node_modules/@modelcontextprotocol/inspector-client/dist/assets/index-*.js',
  { eager: true, import: 'default', query: '?url' }
);
const inspectorStyleModules = import.meta.glob<string>(
  '../../../../node_modules/@modelcontextprotocol/inspector-client/dist/assets/index-*.css',
  { eager: true, import: 'default', query: '?url' }
);
const DEFAULT_ENDPOINT_PATH = (import.meta.env.VITE_MCP_ENDPOINT_PATH as string | undefined) || '/mcp';

type CustomHeader = {
  enabled: boolean;
  name: string;
  value: string;
};

export function InspectorPage() {
  const { apiKey: contextKey } = useApiKey();
  const location = useLocation();
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [endpointDisplay, setEndpointDisplay] = useState('');

  const params = useMemo(() => new URLSearchParams(location.search), [location.search]);
  const authorization = params.get('token') || params.get('authorization') || contextKey || '';
  const inspectorScriptUrl = useMemo(() => pickFirstAssetUrl(inspectorScriptModules), []);
  const inspectorStyleUrl = useMemo(() => pickFirstAssetUrl(inspectorStyleModules), []);
  const inspectorDocument = useMemo(() => {
    if (!inspectorScriptUrl || !inspectorStyleUrl) {
      return '';
    }

    return buildInspectorDocument(inspectorScriptUrl, inspectorStyleUrl);
  }, [inspectorScriptUrl, inspectorStyleUrl]);

  useEffect(() => {
    const endpointParam = params.get('endpoint');
    const endpointUrl = endpointParam ? endpointParam : new URL(DEFAULT_ENDPOINT_PATH, window.location.origin).toString();
    setEndpointDisplay(endpointUrl);

    setError(null);
    setIsLoading(true);

    if (!inspectorScriptUrl || !inspectorStyleUrl) {
      setError('Inspector assets not found. Ensure @modelcontextprotocol/inspector-client is installed.');
      setIsLoading(false);
      return;
    }

    try {
      applyInspectorDefaults(params, endpointUrl, authorization);
    } catch (err) {
      console.error('Failed to prepare MCP Inspector defaults:', err);
      setError('Failed to configure MCP Inspector defaults. Check browser storage permissions for this origin.');
      setIsLoading(false);
    }
  }, [params, authorization, inspectorScriptUrl, inspectorStyleUrl]);

  return (
    <div className="flex h-full min-h-[calc(100vh-8rem)] min-w-0 flex-col bg-background text-foreground">
      <header className="border-b border-border bg-card/80 px-6 py-3 text-sm text-muted-foreground">
        <div className="flex items-center gap-2">
          <Activity className="h-5 w-5 text-primary" />
          <strong className="block text-base text-foreground">MCP Inspector</strong>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1">
          <div>
            Endpoint:&nbsp;
            <code className="break-all text-xs text-foreground/80">{endpointDisplay}</code>
          </div>
          <div className="flex items-center gap-1.5">
            Status:&nbsp;
            {authorization === contextKey && contextKey ? (
              <span className="flex items-center gap-1 text-xs text-green-600 dark:text-green-400">
                <ShieldCheck className="h-3.5 w-3.5" />
                Using key from settings
              </span>
            ) : authorization ? (
              <span className="flex items-center gap-1 text-xs text-amber-600">
                <ShieldAlert className="h-3.5 w-3.5" />
                Using override token
              </span>
            ) : (
              <span className="text-xs text-destructive">No API key</span>
            )}
          </div>
        </div>
        <span className="mt-1 block text-[10px] opacity-70">Override with ?endpoint=&lt;url&gt; and ?token=&lt;value&gt;.</span>
      </header>
      <div className="relative flex-1 min-h-0 overflow-hidden">
        {error ? (
          <div className="flex h-full items-center justify-center px-6 text-center text-sm text-destructive">{error}</div>
        ) : (
          <iframe
            title="MCP Inspector"
            srcDoc={inspectorDocument}
            className="absolute inset-0 block h-full w-full border-0 bg-background"
            onLoad={() => setIsLoading(false)}
          />
        )}
        {isLoading && !error ? (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-background/60 text-sm text-muted-foreground">
            Loading MCP Inspector...
          </div>
        ) : null}
      </div>
    </div>
  );
}

function pickFirstAssetUrl(modules: Record<string, string>): string | undefined {
  const assets = Object.values(modules);
  return assets.length > 0 ? assets[0] : undefined;
}

function buildInspectorDocument(scriptUrl: string, styleUrl: string): string {
  return [
    '<!doctype html>',
    '<html lang="en">',
    '  <head>',
    '    <meta charset="UTF-8" />',
    '    <meta name="viewport" content="width=device-width, initial-scale=1.0" />',
    '    <title>MCP Inspector</title>',
    '    <style>',
    '      html, body, #root {',
    '        height: 100%;',
    '        min-height: 100%;',
    '        margin: 0;',
    '      }',
    '      body {',
    '        overflow: auto;',
    '      }',
    '    </style>',
    `    <link rel="stylesheet" href="${styleUrl}">`,
    '  </head>',
    '  <body>',
    '    <div id="root" class="h-full w-full"></div>',
    `    <script type="module" src="${scriptUrl}"></script>`,
    '  </body>',
    '</html>',
  ].join('\n');
}

function applyInspectorDefaults(params: URLSearchParams, endpointUrl: string, authorization: string): void {
  if (typeof window === 'undefined') {
    return;
  }

  const defaults: Array<{
    force?: boolean;
    key: string;
    value: string;
    previous?: string[];
    queryOverride?: string;
  }> = [
    {
      force: true,
      key: 'lastTransportType',
      value: 'streamable-http',
      previous: ['stdio', ''],
      queryOverride: 'transport',
    },
    {
      force: true,
      key: 'lastSseUrl',
      value: endpointUrl,
      previous: ['http://localhost:3001/sse', ''],
      queryOverride: 'serverUrl',
    },
    {
      force: true,
      key: 'lastConnectionType',
      value: 'direct',
      previous: ['proxy', ''],
      queryOverride: 'connectionType',
    },
  ];

  for (const { key, value, previous = [], queryOverride, force = false } of defaults) {
    try {
      if (queryOverride && params.has(queryOverride)) {
        continue;
      }

      const current = window.localStorage.getItem(key);
      const shouldUpdate = force || current === null || previous.includes(current);

      if (shouldUpdate) {
        window.localStorage.setItem(key, value);
      }
    } catch (error) {
      console.warn('Failed to set MCP Inspector default preference', {
        key,
        error,
      });
    }
  }

  if (!authorization) {
    return;
  }

  const currentHeaders = readCustomHeaders(window.localStorage.getItem('lastCustomHeaders'));
  const shouldOverrideHeaders =
    params.has('token') ||
    params.has('authorization') ||
    currentHeaders.length === 0 ||
    hasOnlyDefaultAuthorizationPlaceholder(currentHeaders);

  if (!shouldOverrideHeaders) {
    return;
  }

  const nextHeaders: CustomHeader[] = [
    {
      name: 'Authorization',
      value: `Bearer ${authorization}`,
      enabled: true,
    },
  ];

  window.localStorage.setItem('lastCustomHeaders', JSON.stringify(nextHeaders));
}

function readCustomHeaders(rawValue: string | null): CustomHeader[] {
  if (!rawValue) {
    return [];
  }

  try {
    const parsed = JSON.parse(rawValue) as unknown;
    if (!Array.isArray(parsed)) {
      return [];
    }

    return parsed.filter(isCustomHeader);
  } catch {
    return [];
  }
}

function isCustomHeader(value: unknown): value is CustomHeader {
  if (typeof value !== 'object' || value === null) {
    return false;
  }

  const candidate = value as Partial<CustomHeader>;
  return typeof candidate.name === 'string' && typeof candidate.value === 'string' && typeof candidate.enabled === 'boolean';
}

function hasOnlyDefaultAuthorizationPlaceholder(headers: CustomHeader[]): boolean {
  if (headers.length !== 1) {
    return false;
  }

  const [header] = headers;
  return header.name.trim().toLowerCase() === 'authorization' && header.value.trim() === 'Bearer' && !header.enabled;
}
