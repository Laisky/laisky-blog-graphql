import { ApiKeyInput } from '@/components/api-key-input';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { useApiKey } from '@/lib/api-key-context';
import { cn } from '@/lib/utils';
import {
  Activity,
  AlertTriangle,
  Cpu,
  ExternalLink,
  Files,
  Globe,
  Key,
  Loader2,
  LogOut,
  MessageSquare,
  Save,
  ShieldAlert,
  ShieldCheck,
  User,
  Wrench,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { getPreferencesFromServer, setDisabledToolsOnServer } from '../user-requests/api';

export function SettingsPage() {
  const { apiKey, status, remainQuota, keyEntries, setAliasForKey, disconnect } = useApiKey();
  const [availableTools, setAvailableTools] = useState<string[]>([]);
  const [disabledTools, setDisabledTools] = useState<string[]>([]);
  const [isToolsLoading, setIsToolsLoading] = useState(false);
  const [isSavingTool, setIsSavingTool] = useState<string | null>(null);
  const [toolsError, setToolsError] = useState<string | null>(null);
  const [aliasDrafts, setAliasDrafts] = useState<Record<string, string>>({});

  const getStatusDisplay = () => {
    switch (status) {
      case 'valid':
        return {
          title: 'Authenticated',
          description: `Balance: ${remainQuota?.toLocaleString() ?? 'Unknown'}`,
          color: 'text-green-500 border-green-500/20 bg-green-500/10',
          icon: <ShieldCheck className="h-10 w-10" />,
        };
      case 'insufficient':
        return {
          title: 'Insufficient Balance',
          description: `Remaining quota is ${remainQuota?.toLocaleString() ?? 0}. Please top up.`,
          color: 'text-amber-500 border-amber-500/20 bg-amber-500/10',
          icon: <AlertTriangle className="h-10 w-10" />,
        };
      case 'error':
        return {
          title: 'Invalid API Key',
          description: 'The API key you entered is incorrect or expired.',
          color: 'text-destructive border-destructive/20 bg-destructive/10',
          icon: <ShieldAlert className="h-10 w-10" />,
        };
      case 'validating':
        return {
          title: 'Validating...',
          description: 'Checking your API key balance...',
          color: 'text-primary border-primary/20 bg-primary/10',
          icon: <Loader2 className="h-10 w-10 animate-spin" />,
        };
      default:
        return {
          title: 'No Active Key',
          description: 'Please set an API key to enable MCP features.',
          color: 'text-muted-foreground border-muted bg-muted/50',
          icon: <ShieldAlert className="h-10 w-10" />,
        };
    }
  };

  const statusDisplay = getStatusDisplay();

  const disabledSet = useMemo(() => new Set(disabledTools), [disabledTools]);

  const groupedTools = useMemo(() => {
    const categories: Record<string, { name: string; icon: React.ReactNode }> = {
      file: { name: 'Files System', icon: <Files className="h-3.5 w-3.5" /> },
      web: { name: 'Web & Search', icon: <Globe className="h-3.5 w-3.5" /> },
      mcp: { name: 'Protocol', icon: <Cpu className="h-3.5 w-3.5" /> },
      get: { name: 'Interaction', icon: <MessageSquare className="h-3.5 w-3.5" /> },
      ask: { name: 'Interaction', icon: <MessageSquare className="h-3.5 w-3.5" /> },
      extract: { name: 'Analysis', icon: <Activity className="h-3.5 w-3.5" /> },
    };

    const groups: Record<string, { name: string; icon: React.ReactNode; tools: string[] }> = {};

    availableTools.forEach((tool) => {
      const prefix = tool.split('_')[0];
      const category = categories[prefix] || { name: 'General', icon: <Wrench className="h-3.5 w-3.5" /> };

      if (!groups[category.name]) {
        groups[category.name] = { ...category, tools: [] };
      }
      groups[category.name].tools.push(tool);
    });

    return Object.values(groups).sort((a, b) => a.name.localeCompare(b.name));
  }, [availableTools]);

  useEffect(() => {
    setAliasDrafts((previous) => {
      const next: Record<string, string> = {};
      for (const entry of keyEntries) {
        next[entry.key] = previous[entry.key] ?? entry.alias;
      }
      return next;
    });
  }, [keyEntries]);

  useEffect(() => {
    if (!apiKey) {
      setAvailableTools([]);
      setDisabledTools([]);
      setToolsError(null);
      return;
    }

    let disposed = false;
    const controller = new AbortController();

    async function loadPreferences() {
      setIsToolsLoading(true);
      setToolsError(null);
      try {
        const pref = await getPreferencesFromServer(apiKey, controller.signal);
        if (disposed) return;
        setAvailableTools(normalizeToolList(pref.available_tools));
        setDisabledTools(normalizeToolList(pref.disabled_tools));
      } catch (error) {
        if (disposed) return;
        setToolsError(error instanceof Error ? error.message : 'Failed to load MCP tool settings.');
      } finally {
        if (!disposed) {
          setIsToolsLoading(false);
        }
      }
    }

    loadPreferences();
    return () => {
      disposed = true;
      controller.abort();
    };
  }, [apiKey]);

  async function toggleTool(toolName: string) {
    if (!apiKey) {
      return;
    }

    const currentlyDisabled = disabledSet.has(toolName);
    const nextDisabled = currentlyDisabled ? disabledTools.filter((name) => name !== toolName) : [...disabledTools, toolName];

    setDisabledTools(nextDisabled);
    setIsSavingTool(toolName);
    setToolsError(null);

    try {
      const pref = await setDisabledToolsOnServer(apiKey, nextDisabled);
      setDisabledTools(normalizeToolList(pref.disabled_tools));
      if (pref.available_tools) {
        setAvailableTools(normalizeToolList(pref.available_tools));
      }
    } catch (error) {
      setDisabledTools(disabledTools);
      setToolsError(error instanceof Error ? error.message : 'Failed to update tool setting.');
    } finally {
      setIsSavingTool(null);
    }
  }

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <User className="h-4 w-4" />
          <span>User Settings</span>
        </div>
        <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">Profile & Authentication</h1>
        <p className="max-w-2xl text-lg text-muted-foreground">
          Manage your API key and authentication settings for Laisky MCP tools. The API key is stored securely in your browser's local
          storage.
        </p>
      </section>

      <div className="grid gap-6 md:grid-cols-2">
        <Card className="border border-border/60 bg-card shadow-sm">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-lg">
              <Key className="h-5 w-5 text-primary" />
              API Key Configuration
            </CardTitle>
            <CardDescription>Enter your API key to authenticate with MCP tools.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            <div className="space-y-4">
              <ApiKeyInput />

              <div className="rounded-lg border border-border bg-muted/30 p-4 text-sm text-muted-foreground">
                <h4 className="mb-2 font-medium text-foreground">What is an API Key?</h4>
                <p className="mb-4 leading-relaxed">
                  The API key (Bearer Token) is used to identify you and authorize MCP tools to act on your behalf or access your specific
                  context.
                </p>
                <Button variant="outline" size="sm" className="w-full justify-between" asChild>
                  <a href="https://wiki.laisky.com/projects/gpt/pay/" target="_blank" rel="noopener noreferrer">
                    How to obtain an API key
                    <ExternalLink className="ml-2 h-3.5 w-3.5" />
                  </a>
                </Button>
              </div>

              {apiKey && (
                <div className="flex flex-col gap-3">
                  <div
                    className={cn(
                      'flex items-center gap-2 rounded-md px-3 py-2 text-sm',
                      status === 'valid'
                        ? 'bg-green-500/10 text-green-600 dark:text-green-400'
                        : status === 'insufficient'
                          ? 'bg-amber-500/10 text-amber-600 dark:text-amber-400'
                          : status === 'error'
                            ? 'bg-destructive/10 text-destructive'
                            : 'bg-muted text-muted-foreground'
                    )}
                  >
                    {status === 'valid' && <ShieldCheck className="h-4 w-4" />}
                    {status === 'insufficient' && <AlertTriangle className="h-4 w-4" />}
                    {status === 'error' && <ShieldAlert className="h-4 w-4" />}
                    {status === 'validating' && <Loader2 className="h-4 w-4 animate-spin" />}
                    <span>
                      {status === 'valid' && 'Authenticated and ready to use.'}
                      {status === 'insufficient' && 'Insufficient balance. Please top up.'}
                      {status === 'error' && 'API key validation failed.'}
                      {status === 'validating' && 'Validating your API key...'}
                    </span>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                    onClick={disconnect}
                  >
                    <LogOut className="mr-2 h-4 w-4" />
                    Disconnect & Clear key
                  </Button>
                </div>
              )}

              {keyEntries.length > 0 && (
                <div className="space-y-4 rounded-xl border border-border/40 bg-muted/10 p-5">
                  <div className="flex items-center justify-between">
                    <div>
                      <h4 className="text-sm font-medium text-foreground">API Key Aliases</h4>
                      <p className="mt-1 text-xs text-muted-foreground">
                        Set a readable alias for each stored API key for quick switching.
                      </p>
                    </div>
                  </div>
                  <div className="space-y-2.5">
                    {keyEntries.map((entry) => {
                      const isActive = entry.key === apiKey;
                      const value = aliasDrafts[entry.key] ?? entry.alias;
                      const isDirty = value !== entry.alias;

                      return (
                        <div
                          key={entry.key}
                          className={cn(
                            'group relative flex flex-col gap-3 rounded-lg border p-3 transition-all duration-200 sm:flex-row sm:items-center sm:justify-between',
                            isActive
                              ? 'border-primary/30 bg-primary/5 shadow-sm'
                              : 'border-border/50 bg-card hover:border-border hover:shadow-sm'
                          )}
                        >
                          <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                            <div className="flex items-center gap-2.5">
                              <div className="relative flex w-full max-w-[240px] items-center">
                                <Input
                                  value={value}
                                  onChange={(event) =>
                                    setAliasDrafts((previous) => ({
                                      ...previous,
                                      [entry.key]: event.target.value,
                                    }))
                                  }
                                  placeholder="Alias"
                                  maxLength={60}
                                  className={cn(
                                    'h-8 w-full text-sm font-medium transition-colors border-transparent hover:border-border focus:border-primary',
                                    isActive
                                      ? 'bg-background/50 focus:bg-background'
                                      : 'bg-transparent hover:bg-muted/50 focus:bg-background'
                                  )}
                                />
                              </div>
                              {isActive && (
                                <Badge
                                  variant="default"
                                  className="border-none bg-primary/10 px-2 text-[10px] font-semibold tracking-wider text-primary hover:bg-primary/20"
                                >
                                  ACTIVE
                                </Badge>
                              )}
                            </div>
                            <p className="truncate pl-1 font-mono text-[11px] text-muted-foreground/70" title={entry.key}>
                              {entry.key}
                            </p>
                          </div>
                          <div className="flex shrink-0 items-center justify-end">
                            <Button
                              type="button"
                              size="sm"
                              variant={isDirty ? 'default' : 'ghost'}
                              className={cn(
                                'h-8 gap-1.5 transition-all duration-200',
                                isDirty
                                  ? 'translate-x-0 opacity-100'
                                  : 'translate-x-2 opacity-0 group-hover:translate-x-0 group-hover:opacity-100'
                              )}
                              onClick={() => setAliasForKey(entry.key, value)}
                              disabled={!isDirty}
                            >
                              <Save className="h-3.5 w-3.5" />
                              {isDirty ? 'Save' : 'Saved'}
                            </Button>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                </div>
              )}
            </div>
          </CardContent>
        </Card>

        <Card className="border border-border/60 bg-card shadow-sm">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-lg text-foreground">
              <User className="h-5 w-5 text-primary" />
              Status
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex flex-col items-center justify-center space-y-4 py-8 text-center">
              <div className={cn('flex h-20 w-20 items-center justify-center rounded-full border-4', statusDisplay.color)}>
                {statusDisplay.icon}
              </div>
              <div className="space-y-1">
                <h3 className="text-xl font-semibold">{statusDisplay.title}</h3>
                <p className="text-sm text-muted-foreground">{statusDisplay.description}</p>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      <Card className="border border-border/60 bg-card shadow-sm">
        <CardHeader className="pb-4">
          <CardTitle className="flex items-center gap-2 text-lg">
            <Wrench className="h-5 w-5 text-primary" />
            MCP Tools
          </CardTitle>
          <CardDescription>Toggle available MCP tools. Grouped by system category.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {!apiKey && <p className="text-sm text-muted-foreground">Please configure an API key first.</p>}

          {apiKey && isToolsLoading && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading tool preferences...
            </div>
          )}

          {apiKey && !isToolsLoading && toolsError && (
            <div className="rounded-md border border-destructive/20 bg-destructive/10 px-3 py-2 text-sm text-destructive">{toolsError}</div>
          )}

          {apiKey && !isToolsLoading && !toolsError && availableTools.length === 0 && (
            <p className="text-sm text-muted-foreground">No MCP tools are currently available for this account.</p>
          )}

          {apiKey && !isToolsLoading && availableTools.length > 0 && (
            <div className="space-y-6">
              {groupedTools.map((group) => (
                <div key={group.name} className="space-y-3">
                  <div className="flex items-center gap-2 border-b border-border/40 pb-1.5">
                    <div className="text-primary">{group.icon}</div>
                    <h3 className="font-mono text-xs font-bold uppercase tracking-wider text-muted-foreground">{group.name}</h3>
                    <Badge variant="outline" className="ml-auto flex h-5 items-center border-none bg-muted/50 px-2 font-mono text-[11px]">
                      {group.tools.length} TOOLS
                    </Badge>
                  </div>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
                    {group.tools.map((toolName) => {
                      const isDisabled = disabledSet.has(toolName);
                      const isSaving = isSavingTool === toolName;

                      return (
                        <div
                          key={toolName}
                          className={cn(
                            'group flex items-center justify-between rounded border px-2 py-1.5 transition-all duration-200',
                            isDisabled ? 'border-border/30 bg-muted/5 opacity-60' : 'border-border/60 bg-card hover:border-primary/30'
                          )}
                        >
                          <span className="truncate font-mono text-sm font-medium text-foreground" title={toolName}>
                            {toolName}
                          </span>
                          <Button
                            type="button"
                            size="sm"
                            variant="ghost"
                            className={cn(
                              'h-6 px-2 font-mono text-xs font-bold uppercase tracking-tighter',
                              isDisabled ? 'text-muted-foreground hover:text-foreground' : 'text-primary hover:bg-primary/10'
                            )}
                            onClick={() => toggleTool(toolName)}
                            disabled={isSavingTool !== null}
                          >
                            {isSaving ? (
                              <Loader2 className="h-3 w-3 animate-spin" />
                            ) : (
                              <div className="flex items-center gap-1.5">
                                <span className={cn('h-1.5 w-1.5 rounded-full', isDisabled ? 'bg-muted-foreground/30' : 'bg-green-500')} />
                                {isDisabled ? 'OFF' : 'ON'}
                              </div>
                            )}
                          </Button>
                        </div>
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function normalizeToolList(toolNames?: string[]): string[] {
  if (!toolNames || toolNames.length === 0) {
    return [];
  }

  const unique = new Set<string>();
  for (const name of toolNames) {
    const normalized = name.trim();
    if (!normalized) {
      continue;
    }
    unique.add(normalized);
  }

  return Array.from(unique);
}
