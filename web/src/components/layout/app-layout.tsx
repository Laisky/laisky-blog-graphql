import { AlertCircle, AlertTriangle, Cpu, Loader2, ShieldAlert, ShieldCheck, User } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import { useApiKey } from '@/lib/api-key-context';
import { useToolsConfig } from '@/lib/tools-config-context';
import { cn } from '@/lib/utils';

interface NavItemConfig {
  to: string;
  label: string;
}

interface ConsoleMenuItem extends NavItemConfig {
  newTab?: boolean;
  /** Tool key to check if enabled. If undefined, item is always shown. */
  toolKey?: 'ask_user' | 'get_user_request' | 'web_search' | 'web_fetch' | 'extract_key_info' | 'file_io';
}

const navItems: NavItemConfig[] = [{ to: '/', label: 'Overview' }];

const consoleItems: ConsoleMenuItem[] = [
  { to: '/debug', label: 'Inspector', newTab: true },
  { to: '/tools/ask_user', label: 'ask_user', toolKey: 'ask_user' },
  { to: '/tools/get_user_requests', label: 'get_user_requests', toolKey: 'get_user_request' },
  { to: '/tools/web_search', label: 'web_search', toolKey: 'web_search' },
  { to: '/tools/web_fetch', label: 'web_fetch', toolKey: 'web_fetch' },
  { to: '/tools/file_io', label: 'file_io', toolKey: 'file_io' },
];

export function AppLayout() {
  const location = useLocation();
  const toolsConfig = useToolsConfig();
  const { status } = useApiKey();

  // Filter console items based on enabled tools
  const filteredConsoleItems = useMemo(() => {
    return consoleItems.filter((item) => {
      if (!item.toolKey) {
        return true; // Always show items without a toolKey
      }
      return toolsConfig[item.toolKey];
    });
  }, [toolsConfig]);

  const isInspectorRoute = location.pathname.startsWith('/debug');
  const isConsoleRoute = filteredConsoleItems.some((item) => location.pathname.startsWith(item.to));

  const isSettingsPage = location.pathname === '/settings';

  return (
    <div className="flex min-h-screen flex-col bg-background text-foreground">
      {status !== 'valid' && !isSettingsPage && (
        <div
          className={cn(
            'px-4 py-2 text-center text-sm font-medium shadow-inner',
            status === 'insufficient' ? 'bg-amber-500/10 text-amber-600' : 'bg-primary/10 text-primary'
          )}
        >
          <div className="container mx-auto flex items-center justify-center gap-2">
            {status === 'insufficient' ? <AlertTriangle className="h-4 w-4" /> : <AlertCircle className="h-4 w-4" />}
            <span>
              {status === 'none' && 'API key is not set. MCP features are disabled.'}
              {status === 'error' && 'API key is invalid. Please check your settings.'}
              {status === 'insufficient' && 'Insufficient balance. Some features may be limited.'}
              {status === 'validating' && 'Validating API key...'}
            </span>
            <Link to="/settings" className="underline hover:opacity-80">
              {status === 'none' ? 'Set API Key' : 'Check Settings'}
            </Link>
          </div>
        </div>
      )}
      <header className="border-b border-border bg-card/80 backdrop-blur">
        <div className="container mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-4 px-6 py-4 md:flex-nowrap">
          <Link to="/" className="flex items-center gap-2 text-lg font-semibold tracking-tight text-foreground">
            <Cpu className="h-5 w-5" />
            <span>Laisky MCP</span>
          </Link>
          <div className="flex w-full items-center justify-between gap-4 md:w-auto md:justify-end">
            <nav className="flex flex-1 flex-wrap items-center gap-3 text-sm font-medium text-muted-foreground md:flex-none">
              {navItems.map((item) => (
                <NavItem key={item.to} to={item.to} label={item.label} />
              ))}
              {filteredConsoleItems.length > 0 && <ConsoleMenu items={filteredConsoleItems} isActive={isConsoleRoute} />}
              <NavItem to="/tools/call_log" label="Logs" />
            </nav>
            <div className="flex items-center gap-2 border-l border-border pl-4">
              <ThemeToggle />
              <Link
                to="/settings"
                className={cn(
                  'flex h-9 w-9 items-center justify-center rounded-full transition-colors',
                  status === 'valid' && 'bg-green-500/10 text-green-600 hover:bg-green-500/20 dark:text-green-400',
                  status === 'insufficient' && 'bg-amber-500/10 text-amber-600 hover:bg-amber-500/20 dark:text-amber-400',
                  status === 'error' && 'bg-destructive/10 text-destructive hover:bg-destructive/20',
                  status === 'validating' && 'bg-primary/10 text-primary hover:bg-primary/20',
                  status === 'none' && 'bg-muted text-muted-foreground hover:bg-muted/80',
                  isSettingsPage && 'ring-2 ring-primary ring-offset-2 ring-offset-background'
                )}
                title={
                  status === 'valid'
                    ? 'Authenticated'
                    : status === 'insufficient'
                      ? 'Insufficient Balance'
                      : status === 'error'
                        ? 'Invalid API Key'
                        : status === 'validating'
                          ? 'Validating...'
                          : 'Configure API Key'
                }
              >
                {status === 'valid' && <ShieldCheck className="h-5 w-5" />}
                {status === 'insufficient' && <AlertTriangle className="h-5 w-5" />}
                {status === 'error' && <ShieldAlert className="h-5 w-5" />}
                {status === 'validating' && <Loader2 className="h-5 w-5 animate-spin" />}
                {status === 'none' && <User className="h-5 w-5" />}
              </Link>
            </div>
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
        Enpower your agents. &copy; 2026 Laisky.
      </footer>
    </div>
  );
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
  );
}

interface ConsoleMenuProps {
  items: ConsoleMenuItem[];
  isActive: boolean;
}

function ConsoleMenu({ items, isActive }: ConsoleMenuProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    function handleClick(event: MouseEvent) {
      if (!containerRef.current || containerRef.current.contains(event.target as Node)) {
        return;
      }
      setOpen(false);
    }

    function handleKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    }

    document.addEventListener('click', handleClick);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('click', handleClick);
      document.removeEventListener('keydown', handleKey);
    };
  }, []);

  useEffect(() => {
    setOpen(false);
  }, [isActive]);

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
  );
}
