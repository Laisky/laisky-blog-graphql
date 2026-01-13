import { ApiKeyInput } from '@/components/api-key-input';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { useApiKey } from '@/lib/api-key-context';
import { cn } from '@/lib/utils';
import { AlertTriangle, ExternalLink, Key, Loader2, LogOut, ShieldAlert, ShieldCheck, User } from 'lucide-react';

export function SettingsPage() {
  const { apiKey, status, remainQuota, disconnect } = useApiKey();

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
    </div>
  );
}
