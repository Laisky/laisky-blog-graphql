import {
  Activity,
  ClipboardList,
  Code2,
  Database,
  ExternalLink,
  FileText,
  FolderOpen,
  Globe,
  Key,
  MessageSquare,
  Search,
  Server,
  ShieldCheck,
  Terminal,
} from 'lucide-react';
import { Link } from 'react-router-dom';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { useToolsConfig } from '@/lib/tools-config-context';
import { cn } from '@/lib/utils';

/**
 * HomePage renders the MCP workspace landing page.
 * It takes no parameters and returns the homepage interface for available tools.
 */
export function HomePage() {
  const toolsConfig = useToolsConfig();
  const endpoint = typeof window === 'undefined' ? 'https://mcp.laisky.com' : window.location.origin;

  const toolCards = [
    {
      key: 'inspector',
      enabled: true,
      element: (
        <ToolCard
          title="Inspector"
          description="Debug and test MCP tools directly. Inspect JSON-RPC traffic and tool definitions."
          icon={<Activity className="h-5 w-5" />}
          priceLabel="Free"
          href="/debug"
          external
        />
      ),
    },
    {
      key: 'web_search',
      enabled: toolsConfig.web_search,
      element: (
        <ToolCard
          title="web_search"
          description="Google Programmable Search queries to retrieve relevant web results."
          icon={<Search className="h-5 w-5" />}
          priceLabel="$0.005/call"
          enabled={toolsConfig.web_search}
          href="/tools/web_search"
        />
      ),
    },
    {
      key: 'web_fetch',
      enabled: toolsConfig.web_fetch,
      element: (
        <ToolCard
          title="web_fetch"
          description="Fetch and render dynamic web pages using a headless browser."
          icon={<Globe className="h-5 w-5" />}
          priceLabel="$0.0001/call"
          enabled={toolsConfig.web_fetch}
          href="/tools/web_fetch"
        />
      ),
    },
    {
      key: 'ask_user',
      enabled: toolsConfig.ask_user,
      element: (
        <ToolCard
          title="ask_user"
          description="Suspend execution to request input from a human operator."
          icon={<MessageSquare className="h-5 w-5" />}
          priceLabel="Free"
          enabled={toolsConfig.ask_user}
          href="/tools/ask_user"
        />
      ),
    },
    {
      key: 'get_user_request',
      enabled: toolsConfig.get_user_request,
      element: (
        <ToolCard
          title="get_user_request"
          description="Deliver the latest human-authored directive queued for the AI agent."
          icon={<ClipboardList className="h-5 w-5" />}
          priceLabel="Free"
          enabled={toolsConfig.get_user_request}
          href="/tools/get_user_requests"
        />
      ),
    },
    {
      key: 'extract_key_info',
      enabled: toolsConfig.extract_key_info,
      element: (
        <ToolCard
          title="extract_key_info"
          description="RAG capability: chunk text and retrieve context using vector embeddings."
          icon={<Database className="h-5 w-5" />}
          priceLabel="Free"
          enabled={toolsConfig.extract_key_info}
        />
      ),
    },
    {
      key: 'file_io',
      enabled: toolsConfig.file_io,
      element: (
        <ToolCard
          title="file_io"
          description="Project-scoped file workspace for reading, writing, listing, and searching."
          icon={<FolderOpen className="h-5 w-5" />}
          priceLabel="Free"
          enabled={toolsConfig.file_io}
          href="/tools/file_io"
        />
      ),
    },
    {
      key: 'memory',
      enabled: toolsConfig.memory,
      element: (
        <ToolCard
          title="memory"
          description="Server-side turn memory: before_turn, after_turn, maintenance, and directory listing."
          icon={<Database className="h-5 w-5" />}
          priceLabel="Free"
          enabled={toolsConfig.memory}
          href="/tools/memory"
        />
      ),
    },
    {
      key: 'call_logs',
      enabled: true,
      element: (
        <ToolCard
          title="Call Logs"
          description="Audit trail of all tool invocations with costs, duration, and error tracking."
          icon={<Server className="h-5 w-5" />}
          priceLabel="Free"
          href="/tools/call_log"
        />
      ),
    },
  ];

  return (
    <div className="space-y-12">
      <section className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_26rem] lg:items-start">
        <div className="space-y-5">
          <Badge variant="outline" className="w-fit border-primary/30 bg-primary/5 text-primary">
            Streamable HTTP MCP server
          </Badge>
          <div className="space-y-3">
            <h1 className="max-w-3xl text-3xl font-bold tracking-tight text-foreground sm:text-5xl">Remote MCP tools for AI agents</h1>
            <p className="max-w-3xl text-base leading-7 text-muted-foreground sm:text-lg">
              Laisky MCP exposes web search, rendered fetch, FileIO, memory, RAG extraction, human request queues, and call logs through one
              authenticated endpoint.
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button asChild>
              <a href="/debug" target="_blank" rel="noopener noreferrer">
                <Activity className="mr-2 h-4 w-4" />
                Open Inspector
              </a>
            </Button>
            <Button variant="outline" asChild>
              <a href="/llms.txt">
                <FileText className="mr-2 h-4 w-4" />
                Agent Guide
              </a>
            </Button>
            <Button variant="ghost" asChild>
              <a href="https://wiki.laisky.com/projects/gpt/pay/" target="_blank" rel="noopener noreferrer">
                <Key className="mr-2 h-4 w-4" />
                Get API Key
                <ExternalLink className="ml-2 h-3.5 w-3.5" />
              </a>
            </Button>
          </div>
        </div>

        <div className="rounded-md border border-border bg-card p-4 shadow-sm">
          <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
            <Terminal className="h-4 w-4 text-primary" />
            Agent connection
          </div>
          <dl className="space-y-3 text-sm">
            <div>
              <dt className="text-xs uppercase tracking-widest text-muted-foreground">Endpoint</dt>
              <dd className="mt-1 break-all font-mono text-foreground">{endpoint}</dd>
            </div>
            <div>
              <dt className="text-xs uppercase tracking-widest text-muted-foreground">Transport</dt>
              <dd className="mt-1 text-foreground">MCP Streamable HTTP</dd>
            </div>
            <div>
              <dt className="text-xs uppercase tracking-widest text-muted-foreground">Auth</dt>
              <dd className="mt-1 flex items-center gap-2 text-foreground">
                <ShieldCheck className="h-4 w-4 text-emerald-600" />
                Bearer token header
              </dd>
            </div>
          </dl>
          <pre className="mt-4 overflow-x-auto rounded-md bg-muted p-3 text-xs leading-5 text-foreground">
            <code>{`Authorization: Bearer <api key>
Accept: application/json, text/event-stream`}</code>
          </pre>
        </div>
      </section>

      <section className="grid gap-4 md:grid-cols-3">
        <ResourceLink href="/.well-known/mcp" title="MCP discovery" description="Transport URL, auth guide, server card, and OpenAPI links." />
        <ResourceLink href="/openapi.json" title="OpenAPI" description="Machine-readable HTTP and GraphQL entry points." />
        <ResourceLink href="/auth.md" title="auth.md" description="Bearer token workflow and agent credential handling." />
        <ResourceLink href="/.well-known/mcp/server-card.json" title="Server card" description="Branded tool preview for registries and agents." />
        <ResourceLink href="/index.md" title="Markdown homepage" description="Canonical low-noise root page for crawlers." />
        <ResourceLink href="/.well-known/agent-skills/index.json" title="Agent skills" description="Capability index for agent skill discovery." />
      </section>

      {toolCards.length > 0 && (
        <section className="space-y-5">
          <h2 className="text-lg font-semibold tracking-tight text-foreground">Available Tools</h2>
          <div className="grid gap-4 md:grid-cols-2">
            {toolCards.map((card) => (
              <div key={card.key}>{card.element}</div>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function ResourceLink({ href, title, description }: { href: string; title: string; description: string }) {
  return (
    <a href={href} className="group rounded-md border border-border bg-card p-4 transition-colors hover:border-primary/40 hover:bg-muted/30">
      <div className="flex items-start gap-3">
        <Code2 className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-foreground">{title}</h2>
          <p className="mt-1 text-sm leading-6 text-muted-foreground">{description}</p>
          <span className="mt-2 block truncate font-mono text-xs text-primary group-hover:underline">{href}</span>
        </div>
      </div>
    </a>
  );
}

function ToolCard({
  title,
  description,
  icon,
  priceLabel,
  enabled = true,
  href,
  external,
}: {
  title: string;
  description: string;
  icon: React.ReactNode;
  priceLabel: string;
  enabled?: boolean;
  href?: string;
  external?: boolean;
}) {
  const content = (
    <Card
      className={cn(
        'group h-full transition-colors',
        enabled ? 'hover:border-primary/40 hover:shadow-sm' : 'cursor-not-allowed opacity-50',
        href && enabled && 'cursor-pointer'
      )}
    >
      <CardHeader className="flex flex-row items-start justify-between gap-3 space-y-0 pb-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <div
            className={cn(
              'flex h-8 w-8 shrink-0 items-center justify-center rounded-md',
              enabled ? 'bg-primary/10 text-primary' : 'bg-muted text-muted-foreground'
            )}
          >
            {icon}
          </div>
          <CardTitle className={cn('font-mono text-base font-medium', !enabled && 'text-muted-foreground')}>{title}</CardTitle>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Badge
            variant="outline"
            className={cn(
              'text-xs',
              enabled
                ? priceLabel === 'Free'
                  ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800/60 dark:bg-emerald-950/40 dark:text-emerald-300'
                  : 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800/60 dark:bg-amber-950/40 dark:text-amber-300'
                : ''
            )}
          >
            {priceLabel}
          </Badge>
          {!enabled && (
            <Badge variant="outline" className="text-xs text-muted-foreground">
              Off
            </Badge>
          )}
        </div>
      </CardHeader>
      <CardContent>
        <CardDescription className={cn('text-sm leading-relaxed', !enabled && 'text-muted-foreground/70')}>{description}</CardDescription>
      </CardContent>
    </Card>
  );

  if (href && enabled) {
    if (external) {
      return (
        <a href={href} target="_blank" rel="noopener noreferrer" className="block h-full no-underline">
          {content}
        </a>
      );
    }
    return (
      <Link to={href} className="block h-full no-underline">
        {content}
      </Link>
    );
  }

  return content;
}
