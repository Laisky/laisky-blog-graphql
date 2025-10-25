import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

export function HomePage() {
  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <p className="text-sm uppercase tracking-widest text-muted-foreground">Front-end workspace</p>
        <h1 className="text-3xl font-semibold tracking-tight text-foreground">
          Unified interface playground
        </h1>
        <p className="max-w-2xl text-base text-muted-foreground">
          This Vite + React project collects the web UIs for the GraphQL service. Each module exposes
          its tooling via dedicated routes so we can develop and build everything together.
        </p>
      </section>

      <section className="grid gap-6 md:grid-cols-2">
        <Card className="border border-border/60 bg-card shadow-sm">
          <CardHeader>
            <CardTitle className="text-lg text-foreground">MCP ask_user console</CardTitle>
            <CardDescription className="text-muted-foreground">
              Browser console for responding to queued MCP questions.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between">
            <p className="max-w-xs text-sm text-muted-foreground">
              Connect with your bearer token to review pending requests, submit answers, and review
              history. The route stays under <code className="text-xs text-muted-foreground">/tools/ask_user</code> for compatibility.
            </p>
            <Button asChild variant="secondary">
              <Link to="/tools/ask_user">Open tool</Link>
            </Button>
          </CardContent>
        </Card>

        <Card className="border border-dashed border-border/60 bg-card/80">
          <CardHeader>
            <CardTitle className="text-lg text-foreground">Additional modules</CardTitle>
            <CardDescription className="text-muted-foreground">
              Ready for future sub-modules that need dedicated UI.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              Add a new route under <code className="text-xs text-muted-foreground">src/pages</code> and register it in <code className="text-xs text-muted-foreground">main.tsx</code> to expand the workspace.
            </p>
          </CardContent>
        </Card>
      </section>
    </div>
  )
}
