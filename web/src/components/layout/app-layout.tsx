import { Link, NavLink, Outlet, useLocation } from 'react-router-dom'
import { useEffect, useRef, useState } from 'react'

import { ThemeToggle } from '@/components/theme/theme-toggle'
import { cn } from '@/lib/utils'

interface NavItemConfig {
  to: string
  label: string
}

interface ConsoleMenuItem extends NavItemConfig {
  newTab?: boolean
}

const navItems: NavItemConfig[] = [{ to: '/', label: 'Overview' }]

const consoleItems: ConsoleMenuItem[] = [
  { to: '/tools/ask_user', label: 'ask_user' },
  { to: '/debug', label: 'Inspector', newTab: true },
]

export function AppLayout() {
  const location = useLocation()
  const isInspectorRoute = location.pathname.startsWith('/debug')
  const isConsoleRoute = consoleItems.some((item) => location.pathname.startsWith(item.to))

  return (
    <div className="flex min-h-screen flex-col bg-background text-foreground">
      <header className="border-b border-border bg-card/80 backdrop-blur">
        <div className="container mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-4 px-6 py-4 md:flex-nowrap">
          <Link to="/" className="text-lg font-semibold tracking-tight text-foreground">
            laisky MCP
          </Link>
          <div className="flex w-full items-center justify-between gap-4 md:w-auto md:justify-end">
            <nav className="flex flex-1 flex-wrap items-center gap-3 text-sm font-medium text-muted-foreground md:flex-none">
              {navItems.map((item) => (
                <NavItem key={item.to} to={item.to} label={item.label} />
              ))}
              <ConsoleMenu items={consoleItems} isActive={isConsoleRoute} />
              <NavItem to="/tools/call_log" label="Logs" />
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

interface ConsoleMenuProps {
  items: ConsoleMenuItem[]
  isActive: boolean
}

function ConsoleMenu({ items, isActive }: ConsoleMenuProps) {
  const [open, setOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    function handleClick(event: MouseEvent) {
      if (!containerRef.current || containerRef.current.contains(event.target as Node)) {
        return
      }
      setOpen(false)
    }

    function handleKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    document.addEventListener('click', handleClick)
    document.addEventListener('keydown', handleKey)
    return () => {
      document.removeEventListener('click', handleClick)
      document.removeEventListener('keydown', handleKey)
    }
  }, [])

  useEffect(() => {
    setOpen(false)
  }, [isActive])

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((prev) => !prev)}
        className={cn(
          'flex items-center gap-1 rounded-md px-3 py-1.5 transition-colors hover:bg-muted hover:text-foreground',
          isActive || open ? 'bg-muted text-foreground' : 'text-muted-foreground'
        )}
        aria-expanded={open}
        aria-haspopup="true"
      >
        Console
        <span aria-hidden="true" className="text-xs">
          ▾
        </span>
      </button>
      {open ? (
        <div className="absolute right-0 mt-2 w-44 rounded-md border border-border/60 bg-card shadow-lg">
          <ul className="py-1 text-sm text-foreground">
            {items.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  target={item.newTab ? '_blank' : undefined}
                  rel={item.newTab ? 'noopener noreferrer' : undefined}
                  className={({ isActive: linkActive }) =>
                    cn(
                      'block px-3 py-2 transition-colors hover:bg-muted',
                      linkActive ? 'bg-muted text-foreground' : 'text-muted-foreground'
                    )
                  }
                  onClick={() => setOpen(false)}
                >
                  {item.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  )
}
