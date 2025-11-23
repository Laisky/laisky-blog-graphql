import {
  Activity,
  Cpu,
  Database,
  ExternalLink,
  Globe,
  Key,
  MessageSquare,
  Search,
  Server,
  Terminal,
} from 'lucide-react'
import { Link } from 'react-router-dom'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

export function HomePage() {
  return (
    <div className="space-y-12">
      {/* Hero Section */}
      <section className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <Cpu className="h-4 w-4" />
          <span>MCP Workspace</span>
        </div>
        <h1 className="text-4xl font-bold tracking-tight text-foreground sm:text-5xl">
          Model Context Protocol
        </h1>
        <p className="max-w-3xl text-lg text-muted-foreground">
          A unified interface for AI agents to interact with external tools and data. This workspace
          provides management consoles and documentation for the available capabilities.
        </p>

        <Card className="max-w-3xl border-primary/20 bg-primary/5">
          <CardContent className="flex flex-col gap-4 p-6 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <h3 className="flex items-center gap-2 font-semibold text-foreground">
                <Key className="h-4 w-4 text-primary" />
                API Key Required
              </h3>
              <p className="text-sm text-muted-foreground">
                Access to all tools in this MCP requires an API key.
              </p>
            </div>
            <Button asChild className="shrink-0">
              <a
                href="https://wiki.laisky.com/projects/gpt/pay/"
                target="_blank"
                rel="noopener noreferrer"
              >
                Get API Key
                <ExternalLink className="ml-2 h-4 w-4" />
              </a>
            </Button>
          </CardContent>
        </Card>
      </section>

      {/* Consoles Section */}
      <section className="space-y-6">
        <div className="flex items-center gap-2 border-b border-border pb-2">
          <Terminal className="h-5 w-5 text-foreground" />
          <h2 className="text-2xl font-semibold tracking-tight">Management Consoles</h2>
        </div>
        <div className="grid gap-6 md:grid-cols-3">
          <ConsoleCard
            title="Ask User Console"
            description="Interface for human-in-the-loop interactions. Respond to pending questions from AI agents."
            icon={<MessageSquare className="h-6 w-6" />}
            href="/tools/ask_user"
            action="Open Console"
          />
          <ConsoleCard
            title="MCP Inspector"
            description="Debug and test MCP tools directly. Inspect JSON-RPC traffic and tool definitions."
            icon={<Activity className="h-6 w-6" />}
            href="/debug"
            action="Launch Inspector"
            external
          />
          <ConsoleCard
            title="Call Logs"
            description="Audit trail of all tool invocations, including costs, duration, and error rates."
            icon={<Server className="h-6 w-6" />}
            href="/tools/call_log"
            action="View Logs"
          />
        </div>
      </section>

      {/* Tools Section */}
      <section className="space-y-6">
        <div className="flex items-center gap-2 border-b border-border pb-2">
          <Database className="h-5 w-5 text-foreground" />
          <h2 className="text-2xl font-semibold tracking-tight">Available Tools</h2>
        </div>
        <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-2">
          <ToolCard
            title="web_search"
            description="Performs Google Programmable Search queries to retrieve relevant web results."
            icon={<Search className="h-5 w-5" />}
            tags={['External API', 'Billing']}
          />
          <ToolCard
            title="web_fetch"
            description="Fetches and renders dynamic web pages using a headless browser (via Redis)."
            icon={<Globe className="h-5 w-5" />}
            tags={['Headless Browser', 'Content Extraction']}
          />
          <ToolCard
            title="ask_user"
            description="Suspends execution to request input from a human operator via the console."
            icon={<MessageSquare className="h-5 w-5" />}
            tags={['Human-in-the-loop', 'Async']}
          />
          <ToolCard
            title="extract_key_info"
            description="RAG capability that chunks text and retrieves relevant context using vector embeddings."
            icon={<Database className="h-5 w-5" />}
            tags={['RAG', 'Vector DB', 'Embeddings']}
          />
        </div>
      </section>
    </div>
  )
}

function ConsoleCard({
  title,
  description,
  icon,
  href,
  action,
  external,
}: {
  title: string
  description: string
  icon: React.ReactNode
  href: string
  action: string
  external?: boolean
}) {
  return (
    <Card className="flex flex-col border-border/60 bg-card transition-all hover:border-border hover:shadow-md">
      <CardHeader>
        <div className="mb-2 flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 text-primary">
          {icon}
        </div>
        <CardTitle className="text-xl">{title}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-1 flex-col justify-between gap-4">
        <CardDescription className="text-base">{description}</CardDescription>
        <Button asChild variant="outline" className="group w-full justify-between">
          <Link
            to={href}
            target={external ? '_blank' : undefined}
            rel={external ? 'noopener noreferrer' : undefined}
          >
            {action}
            {external ? (
              <ExternalLink className="h-4 w-4 opacity-50" />
            ) : (
              <span className="opacity-0 transition-opacity group-hover:opacity-100">→</span>
            )}
          </Link>
        </Button>
      </CardContent>
    </Card>
  )
}

function ToolCard({
  title,
  description,
  icon,
  tags,
}: {
  title: string
  description: string
  icon: React.ReactNode
  tags: string[]
}) {
  return (
    <Card className="border-border/40 bg-card/50">
      <CardHeader className="flex flex-row items-start justify-between space-y-0 pb-2">
        <div className="flex items-center gap-2">
          <div className="flex h-8 w-8 items-center justify-center rounded-md bg-muted text-foreground">
            {icon}
          </div>
          <CardTitle className="font-mono text-lg font-medium">{title}</CardTitle>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <CardDescription className="text-sm leading-relaxed">{description}</CardDescription>
        <div className="flex flex-wrap gap-2">
          {tags.map((tag) => (
            <Badge key={tag} variant="secondary" className="text-xs font-normal">
              {tag}
            </Badge>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}
