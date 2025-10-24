import { Link, NavLink, Outlet, useLocation } from 'react-router-dom'

import { ThemeToggle } from '@/components/theme/theme-toggle'
import { cn } from '@/lib/utils'

const navItems = [
  { to: '/', label: 'Overview' },
  { to: '/mcp/tools/ask_user', label: 'MCP ask_user' },
  { to: '/mcp/debug', label: 'MCP Inspector' },
]

export function AppLayout() {
  const location = useLocation()
  const isInspectorRoute = location.pathname.startsWith('/mcp/debug')

  return (
    <div className="flex min-h-screen flex-col bg-background text-foreground">
      <header className="border-b border-border bg-card/80 backdrop-blur">
        <div className="container mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-4 px-6 py-4 md:flex-nowrap">
          <Link to="/" className="text-lg font-semibold tracking-tight text-foreground">
            laisky front-ends
          </Link>
          <div className="flex w-full items-center justify-between gap-4 md:w-auto md:justify-end">
            <nav className="flex flex-1 flex-wrap items-center gap-3 text-sm font-medium text-muted-foreground md:flex-none">
              {navItems.map((item) => (
                <NavItem key={item.to} to={item.to} label={item.label} />
              ))}
            </nav>
            <ThemeToggle />
          </div>
        </div>
      </header>
      <main className="flex-1 bg-background">
        {isInspectorRoute ? (
          <Outlet />
        ) : (
          <div className="container mx-auto max-w-6xl px-4 py-10">
            <Outlet />
          </div>
        )}
      </main>
      <footer className="border-t border-border bg-card/80 py-4 text-center text-xs text-muted-foreground">
        Built with Vite, React, and shadcn/ui.
      </footer>
    </div>
  )
}

function NavItem({ to, label }: { to: string; label: string }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        cn(
          'rounded-md px-3 py-1.5 transition-colors hover:bg-muted hover:text-foreground',
          isActive ? 'bg-muted text-foreground' : 'text-muted-foreground'
        )
      }
      end={to === '/'}
    >
      {label}
    </NavLink>
  )
}
