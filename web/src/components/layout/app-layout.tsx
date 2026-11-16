import { AlertCircle, AlertTriangle, Check, Cpu, Key, Loader2, ShieldAlert, ShieldCheck, User } from 'lucide-react';
import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { LaiskyLink } from '@/components/ui/laisky-link';
import { useApiKey } from '@/lib/api-key-context';
import { preloadConsoleRoute } from '@/lib/route-modules';
import { useToolsConfig } from '@/lib/tools-config-context';
import { cn } from '@/lib/utils';

interface NavItemConfig {
  to: string;
  label: string;
}

interface ConsoleMenuItem extends NavItemConfig {
  newTab?: boolean;
  /** Tool key to check if enabled. If undefined, item is always shown. */
  toolKey?: 'ask_user' | 'get_user_request' | 'web_search' | 'web_fetch' | 'extract_key_info' | 'file_io' | 'memory';
}

const navItems: NavItemConfig[] = [{ to: '/', label: 'Overview' }];

const consoleItems: ConsoleMenuItem[] = [
  { to: '/debug', label: 'Inspector', newTab: true },
  { to: '/tools/ask_user', label: 'ask_user', toolKey: 'ask_user' },
  { to: '/tools/get_user_requests', label: 'get_user_requests', toolKey: 'get_user_request' },
  { to: '/tools/web_search', label: 'web_search', toolKey: 'web_search' },
  { to: '/tools/web_fetch', label: 'web_fetch', toolKey: 'web_fetch' },
  { to: '/tools/extract_key_info', label: 'extract_key_info', toolKey: 'extract_key_info' },
  { to: '/tools/file_io', label: 'file_io', toolKey: 'file_io' },
  { to: '/tools/memory', label: 'memory', toolKey: 'memory' },
];

/** AppLayout is the shell every console page renders inside: header, navigation and the routed outlet. */
export function AppLayout() {
  const location = useLocation();
  const toolsConfig = useToolsConfig();
  const { apiKey, isToolConsoleLocked, status, sessionId } = useApiKey();

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
    <div className="flex min-h-dvh w-full max-w-full flex-col overflow-x-clip bg-background text-foreground">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[60] focus:rounded-md focus:bg-card focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground focus:shadow-lg focus:outline-none focus:ring-2 focus:ring-ring"
      >
        Skip to main content
      </a>
      {/* One sticky stack, so the API key banner and the header never overlap while scrolled. */}
      <div className="sticky top-0 z-40">
      {status !== 'valid' && !isSettingsPage && (
        <div
          className={cn(
            'border-b',
            isToolConsoleLocked ? 'bg-primary text-primary-foreground' : 'bg-amber-500 text-amber-950'
          )}
        >
          <div className="container mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-2 text-sm font-medium">
            <div className="flex items-center gap-2">
              {status === 'insufficient' ? <AlertTriangle className="h-4 w-4 shrink-0" /> : <AlertCircle className="h-4 w-4 shrink-0" />}
              <span>
                {status === 'none' && 'API key required. Set one in Settings to enable tools.'}
                {status === 'error' && 'Invalid API key. Update it in Settings.'}
                {status === 'insufficient' && 'Insufficient balance. Some features may be limited.'}
                {status === 'validating' && 'Validating API key...'}
              </span>
            </div>
            <Link
              to="/settings"
              className={cn(
                'shrink-0 rounded-md border px-3 py-1.5 text-sm font-medium transition-colors',
                isToolConsoleLocked
                  ? 'border-primary-foreground/25 bg-primary-foreground text-primary hover:bg-primary-foreground/90'
                  : 'border-amber-950/20 bg-amber-50 text-amber-900 hover:bg-amber-100'
              )}
            >
              {status === 'none' ? 'Set API Key' : 'Settings'}
            </Link>
          </div>
        </div>
      )}
      <header className="border-b border-border bg-card/80 backdrop-blur">
        <div className="container mx-auto flex max-w-6xl items-center justify-between gap-2 px-3 py-2 md:gap-4 md:px-6 md:py-4">
          <div className="flex shrink-0 items-center gap-1.5 text-base font-semibold tracking-tight text-foreground md:gap-2 md:text-lg">
            <Link
              to="/"
              className="flex shrink-0 items-center gap-1.5 text-foreground transition-colors hover:text-primary md:gap-2"
              aria-label="Go to overview"
            >
              <Cpu className="h-5 w-5 text-primary" />
              <span className="hidden sm:inline">Laisky MCP</span>
            </Link>
          </div>
          <div className="flex min-w-0 flex-1 items-center justify-end gap-2 md:gap-4">
            <nav aria-label="Primary" className="flex min-w-0 items-center gap-1 text-sm font-medium text-muted-foreground md:gap-3">
              {navItems.map((item) => (
                <NavItem key={item.to} to={item.to} label={item.label} />
              ))}
              {filteredConsoleItems.length > 0 && <ConsoleMenu items={filteredConsoleItems} isActive={isConsoleRoute} />}
              <NavItem to="/tools/call_log" label="Logs" />
            </nav>
            <div className="flex shrink-0 items-center gap-1 border-l border-border pl-2 md:gap-2 md:pl-4">
              <ApiKeyAliasSwitcher />
              <ThemeToggle />
              <NavLink
                to="/settings"
                onPointerEnter={() => preloadConsoleRoute('/settings')}
                onFocus={() => preloadConsoleRoute('/settings')}
                aria-label={`Settings: ${statusLabel(status)}`}
                className={cn(
                  'flex h-8 w-8 items-center justify-center rounded-full transition-colors md:h-9 md:w-9',
                  status === 'valid' && 'bg-green-500/10 text-green-600 hover:bg-green-500/20 dark:text-green-400',
                  status === 'insufficient' && 'bg-amber-500/10 text-amber-600 hover:bg-amber-500/20 dark:text-amber-400',
                  status === 'error' && 'bg-destructive/10 text-destructive hover:bg-destructive/20',
                  status === 'validating' && 'bg-primary/10 text-primary hover:bg-primary/20',
                  status === 'none' && 'bg-muted text-muted-foreground hover:bg-muted/80',
                  isSettingsPage && 'ring-2 ring-primary ring-offset-2 ring-offset-background'
                )}
                title={statusLabel(status)}
              >
                {status === 'valid' && <ShieldCheck className="h-5 w-5" />}
                {status === 'insufficient' && <AlertTriangle className="h-5 w-5" />}
                {status === 'error' && <ShieldAlert className="h-5 w-5" />}
                {status === 'validating' && <Loader2 className="h-5 w-5 animate-spin" />}
                {status === 'none' && <User className="h-5 w-5" />}
              </NavLink>
            </div>
          </div>
        </div>
      </header>
      </div>
      <main id="main-content" tabIndex={-1} className="flex-1 bg-background focus:outline-none">
        {isInspectorRoute ? (
          <div key={`${apiKey}:${sessionId}`}>
            <Outlet />
          </div>
        ) : (
          <div className="container mx-auto max-w-6xl px-4 py-10">
            <div key={`${apiKey}:${sessionId}`}>
              <Outlet />
            </div>
          </div>
        )}
      </main>
      <footer className="border-t border-border bg-card/80 py-4 text-center text-xs text-muted-foreground">
        Empower your agents. &copy; 2026 <LaiskyLink className="underline-offset-4 hover:text-foreground hover:underline" />.
      </footer>
    </div>
  );
}

/** statusLabel describes the API key status for the settings link's name and tooltip. */
function statusLabel(status: ReturnType<typeof useApiKey>['status']): string {
  switch (status) {
    case 'valid':
      return 'Authenticated';
    case 'insufficient':
      return 'Insufficient Balance';
    case 'error':
      return 'Invalid API Key';
    case 'validating':
      return 'Validating...';
    default:
      return 'Configure API Key';
  }
}

/** currentPageClass marks the current page with an underline as well as color, so it does not rely on color alone. */
const currentPageClass = 'aria-[current=page]:underline aria-[current=page]:decoration-2 aria-[current=page]:underline-offset-4';

/** maskKey returns a safe key preview for menu display. */
function maskKey(key: string): string {
  if (key.length <= 8) {
    return key;
  }
  return `${key.slice(0, 4)}••••${key.slice(-4)}`;
}

/** ApiKeyAliasSwitcher renders a quick-switch menu for stored API key aliases. */
function ApiKeyAliasSwitcher() {
  const { apiKey, keyEntries, status, switchApiKey } = useApiKey();

  if (keyEntries.length === 0) {
    return null;
  }

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="flex h-8 w-8 items-center justify-center rounded-full bg-muted text-muted-foreground transition-colors hover:bg-muted/80 hover:text-foreground md:h-9 md:w-9"
          aria-label="Switch API key"
          title="Switch API key alias"
        >
          <Key className="h-4 w-4" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <DropdownMenuLabel className="text-xs uppercase tracking-wide text-muted-foreground">API Key Aliases</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {keyEntries.map((entry) => {
          const isActive = entry.key === apiKey;
          const isDisabled = status === 'validating' || isActive;

          return (
            <DropdownMenuItem
              key={entry.key}
              disabled={isDisabled}
              onClick={() => {
                switchApiKey(entry.key);
              }}
              className="flex items-center gap-2"
            >
              <div className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-sm font-medium text-foreground">{entry.alias}</span>
                <span className="font-mono text-xs text-muted-foreground">{maskKey(entry.key)}</span>
              </div>
              {isActive && <Check className="h-4 w-4 text-primary" />}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** NavItem renders one top-level navigation link with its active state. */
function NavItem({ to, label }: { to: string; label: string }) {
  return (
    <NavLink
      to={to}
      onPointerEnter={() => preloadConsoleRoute(to)}
      onFocus={() => preloadConsoleRoute(to)}
      className={({ isActive }) =>
        cn(
          'whitespace-nowrap rounded-md px-2 py-1 text-xs transition-colors hover:bg-muted hover:text-foreground md:px-3 md:py-1.5 md:text-sm',
          currentPageClass,
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

/**
 * ConsoleMenu renders the per-tool console links as a navigation disclosure, listing only the
 * tools this deployment actually serves. The links stay in the Tab order (this is site
 * navigation, not an application menu); Escape closes it and returns focus to the trigger,
 * and it closes when focus or a click moves outside it.
 */
function ConsoleMenu({ items, isActive }: ConsoleMenuProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const listId = useId();

  useEffect(() => {
    if (!open) {
      return;
    }

    function handleClick(event: MouseEvent) {
      if (!containerRef.current || containerRef.current.contains(event.target as Node)) {
        return;
      }
      setOpen(false);
    }

    function handleKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false);
        triggerRef.current?.focus();
      }
    }

    document.addEventListener('click', handleClick);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('click', handleClick);
      document.removeEventListener('keydown', handleKey);
    };
  }, [open]);

  useEffect(() => {
    setOpen(false);
  }, [isActive]);

  return (
    <div
      ref={containerRef}
      className="relative"
      onBlur={(event) => {
        // Tabbing past the last link (or Shift+Tab before the trigger) closes the list.
        if (event.relatedTarget && !event.currentTarget.contains(event.relatedTarget as Node)) {
          setOpen(false);
        }
      }}
    >
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((prev) => !prev)}
        className={cn(
          'flex items-center gap-1 whitespace-nowrap rounded-md px-2 py-1 text-xs transition-colors hover:bg-muted hover:text-foreground md:px-3 md:py-1.5 md:text-sm',
          isActive && 'underline decoration-2 underline-offset-4',
          isActive || open ? 'bg-muted text-foreground' : 'text-muted-foreground'
        )}
        aria-expanded={open}
        aria-controls={listId}
      >
        Console
        <span aria-hidden="true" className="text-xs">
          ▾
        </span>
      </button>
      <div id={listId} hidden={!open} className="absolute right-0 mt-2 w-44 rounded-md border border-border/60 bg-card shadow-lg">
        <ul className="py-1 text-sm text-foreground">
          {items.map((item) => (
            <li key={item.to}>
              <NavLink
                to={item.to}
                target={item.newTab ? '_blank' : undefined}
                rel={item.newTab ? 'noopener noreferrer' : undefined}
                onPointerEnter={() => preloadConsoleRoute(item.to)}
                onFocus={() => preloadConsoleRoute(item.to)}
                className={({ isActive: linkActive }) =>
                  cn(
                    'block px-3 py-2 transition-colors hover:bg-muted',
                    currentPageClass,
                    linkActive ? 'bg-muted text-foreground' : 'text-muted-foreground'
                  )
                }
                onClick={() => setOpen(false)}
              >
                {item.label}
                {item.newTab ? <span className="sr-only"> (opens in a new tab)</span> : null}
              </NavLink>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
