import { ApiKeyInput } from '@/components/api-key-input';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { useApiKey } from '@/lib/api-key-context';
import { ExternalLink, Key, LogOut, ShieldAlert, ShieldCheck, User } from 'lucide-react';

export function SettingsPage() {
    const { apiKey, disconnect } = useApiKey();

    return (
        <div className="space-y-8">
            <section className="space-y-4">
                <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
                    <User className="h-4 w-4" />
                    <span>User Settings</span>
                </div>
                <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
                    Profile & Authentication
                </h1>
                <p className="max-w-2xl text-lg text-muted-foreground">
                    Manage your API key and authentication settings for Laisky MCP tools.
                    The API key is stored securely in your browser's local storage.
                </p>
            </section>

            <div className="grid gap-6 md:grid-cols-2">
                <Card className="border border-border/60 bg-card shadow-sm">
                    <CardHeader>
                        <CardTitle className="flex items-center gap-2 text-lg">
                            <Key className="h-5 w-5 text-primary" />
                            API Key Configuration
                        </CardTitle>
                        <CardDescription>
                            Enter your API key to authenticate with MCP tools.
                        </CardDescription>
                    </CardHeader>
                    <CardContent className="space-y-6">
                        <div className="space-y-4">
                            <ApiKeyInput />

                            <div className="rounded-lg border border-border bg-muted/30 p-4 text-sm text-muted-foreground">
                                <h4 className="mb-2 font-medium text-foreground">What is an API Key?</h4>
                                <p className="mb-4 leading-relaxed">
                                    The API key (Bearer Token) is used to identify you and authorize
                                    MCP tools to act on your behalf or access your specific context.
                                </p>
                                <Button
                                    variant="outline"
                                    size="sm"
                                    className="w-full justify-between"
                                    asChild
                                >
                                    <a
                                        href="https://wiki.laisky.com/projects/gpt/pay/"
                                        target="_blank"
                                        rel="noopener noreferrer"
                                    >
                                        How to obtain an API key
                                        <ExternalLink className="ml-2 h-3.5 w-3.5" />
                                    </a>
                                </Button>
                            </div>

                            {apiKey && (
                                <div className="flex flex-col gap-3">
                                    <div className="flex items-center gap-2 rounded-md bg-green-500/10 px-3 py-2 text-sm text-green-600 dark:text-green-400">
                                        <ShieldCheck className="h-4 w-4" />
                                        <span>Authenticated and ready to use.</span>
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
                            <div
                                className={`flex h-20 w-20 items-center justify-center rounded-full border-4 ${
                                    apiKey
                                        ? 'border-green-500/20 bg-green-500/10 text-green-500'
                                        : 'border-muted bg-muted/50 text-muted-foreground'
                                }`}
                            >
                                {apiKey ? (
                                    <ShieldCheck className="h-10 w-10" />
                                ) : (
                                    <ShieldAlert className="h-10 w-10" />
                                )}
                            </div>
                            <div className="space-y-1">
                                <h3 className="text-xl font-semibold">
                                    {apiKey ? 'Active Session' : 'No Active Key'}
                                </h3>
                                <p className="text-sm text-muted-foreground">
                                    {apiKey
                                        ? 'Your API key is set and persisted in this browser.'
                                        : 'Please set an API key to enable MCP features.'}
                                </p>
                            </div>
                        </div>
                    </CardContent>
                </Card>
            </div>
        </div>
    );
}
