import { Activity, ClipboardList, Cpu, Database, ExternalLink, FolderOpen, Globe, Key, MessageSquare, Search, Server } from 'lucide-react';
import { Link } from 'react-router-dom';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { useToolsConfig } from '@/lib/tools-config-context';

export function HomePage() {
  const toolsConfig = useToolsConfig();

  // Tool cards - show all tools with enabled state for visual distinction
  const toolCards = [
    {
      key: 'inspector',
      enabled: true,
      element: (
        <ToolCard
          title="Inspector"
          description="Debug and test MCP tools directly. Inspect JSON-RPC traffic and tool definitions."
          icon={<Activity className="h-5 w-5" />}
          tags={['Debug', 'Test']}
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
          description="Performs Google Programmable Search queries to retrieve relevant web results."
          icon={<Search className="h-5 w-5" />}
          tags={['External API', 'Billing']}
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
          description="Fetches and renders dynamic web pages using a headless browser (via Redis)."
          icon={<Globe className="h-5 w-5" />}
          tags={['Headless Browser', 'Content Extraction']}
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
          description="Suspends execution to request input from a human operator via the console."
          icon={<MessageSquare className="h-5 w-5" />}
          tags={['Human-in-the-loop', 'Async']}
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
          description="Delivers the latest human-authored directive queued for the AI agent."
          icon={<ClipboardList className="h-5 w-5" />}
          tags={['Human-in-the-loop', 'Push-based']}
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
          description="RAG capability that chunks text and retrieves relevant context using vector embeddings."
          icon={<Database className="h-5 w-5" />}
          tags={['RAG', 'Vector DB', 'Embeddings']}
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
          description="Project-scoped file workspace for reading, writing, listing, and searching content."
          icon={<FolderOpen className="h-5 w-5" />}
          tags={['Storage', 'Workspace', 'Search']}
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
          description="Server-side turn memory lifecycle tools: before_turn, after_turn, maintenance, and directory abstracts."
          icon={<Database className="h-5 w-5" />}
          tags={['Context', 'Recall', 'Lifecycle']}
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
          description="Audit trail of all tool invocations, including costs, duration, and error rates."
          icon={<Server className="h-5 w-5" />}
          tags={['Audit', 'Cost Tracking']}
          href="/tools/call_log"
        />
      ),
    },
  ];

  return (
    <div className="space-y-12">
      {/* Hero Section */}
      <section className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <Cpu className="h-4 w-4" />
          <span>MCP Workspace</span>
        </div>
        <h1 className="text-4xl font-bold tracking-tight text-foreground sm:text-5xl">Model Context Protocol</h1>
        <p className="max-w-3xl text-lg text-muted-foreground">
          A unified interface for AI agents to interact with external tools and data. This workspace provides management consoles and
          documentation for the available capabilities.
        </p>

        <Card className="max-w-3xl border-primary/20 bg-primary/5">
          <CardContent className="flex flex-col gap-4 p-6 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <h3 className="flex items-center gap-2 font-semibold text-foreground">
                <Key className="h-4 w-4 text-primary" />
                API Key Required
              </h3>
              <p className="text-sm text-muted-foreground">Access to all tools in this MCP requires an API key.</p>
            </div>
            <Button asChild className="shrink-0">
              <a href="https://wiki.laisky.com/projects/gpt/pay/" target="_blank" rel="noopener noreferrer">
                Get API Key
                <ExternalLink className="ml-2 h-4 w-4" />
              </a>
            </Button>
          </CardContent>
        </Card>
      </section>

      {/* Tools Section */}
      {toolCards.length > 0 && (
        <section className="space-y-6">
          <div className="flex items-center gap-2 border-b border-border pb-2">
            <Database className="h-5 w-5 text-foreground" />
            <h2 className="text-2xl font-semibold tracking-tight">Available Tools</h2>
          </div>
          <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-2">
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
  tags,
  enabled = true,
  href,
  external,
}: {
  title: string;
  description: string;
  icon: React.ReactNode;
  tags: string[];
  enabled?: boolean;
  href?: string;
  external?: boolean;
}) {
  const content = (
    <Card
      className={`group h-full transition-all ${
        enabled
          ? 'border-sky-200/60 bg-sky-50/30 hover:border-sky-300/80 hover:shadow-sm dark:border-sky-800/40 dark:bg-sky-950/20 dark:hover:border-sky-700/60'
          : 'cursor-not-allowed border-gray-200/40 bg-gray-100/30 opacity-60 dark:border-gray-700/30 dark:bg-gray-800/20'
      } ${href ? 'cursor-pointer' : ''}`}
    >
      <CardHeader className="flex flex-row items-start justify-between space-y-0 pb-2">
        <div className="flex items-center gap-2">
          <div
            className={`flex h-8 w-8 items-center justify-center rounded-md ${
              enabled
                ? 'bg-sky-100 text-sky-700 dark:bg-sky-900/50 dark:text-sky-300'
                : 'bg-gray-200 text-gray-400 dark:bg-gray-700/50 dark:text-gray-500'
            }`}
          >
            {icon}
          </div>
          <CardTitle className={`font-mono text-lg font-medium ${enabled ? '' : 'text-muted-foreground'}`}>{title}</CardTitle>
          {!enabled && (
            <Badge variant="outline" className="ml-2 text-xs text-muted-foreground">
              Disabled
            </Badge>
          )}
        </div>
        {href && enabled && (
          <div className="text-muted-foreground/50 transition-colors group-hover:text-primary">
            {external ? <ExternalLink className="h-4 w-4" /> : <span className="text-xl">→</span>}
          </div>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        <CardDescription className={`text-sm leading-relaxed ${enabled ? '' : 'text-muted-foreground/70'}`}>{description}</CardDescription>
        <div className="flex flex-wrap gap-2">
          {tags.map((tag) => (
            <Badge
              key={tag}
              variant="secondary"
              className={`text-xs font-normal ${enabled ? '' : 'bg-gray-100 text-gray-400 dark:bg-gray-800 dark:text-gray-500'}`}
            >
              {tag}
            </Badge>
          ))}
        </div>
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
