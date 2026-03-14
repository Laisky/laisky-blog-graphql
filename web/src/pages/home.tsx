import { Activity, ClipboardList, Database, ExternalLink, FolderOpen, Globe, Key, MessageSquare, Search, Server } from 'lucide-react';
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
    <div className="space-y-10">
      <section className="space-y-3">
        <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">MCP Workspace</h1>
        <p className="max-w-2xl text-muted-foreground">
          Manage and test your AI agent tools.{' '}
          <Button variant="link" asChild className="h-auto p-0 text-base">
            <a href="https://wiki.laisky.com/projects/gpt/pay/" target="_blank" rel="noopener noreferrer">
              <Key className="mr-1 h-3.5 w-3.5" />
              Get an API key
              <ExternalLink className="ml-1 h-3 w-3" />
            </a>
          </Button>{' '}
          to enable all tools.
        </p>
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
