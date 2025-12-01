import { ChevronDown, ClipboardList, Edit3, Package, RotateCcw, Send, Trash2, Undo2 } from "lucide-react";
import type { ChangeEvent, KeyboardEvent } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { ApiKeyInput } from "@/components/api-key-input";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { normalizeApiKey, useApiKey } from "@/lib/api-key-context";
import { cn } from "@/lib/utils";

import {
  createUserRequest,
  deleteAllPendingRequests,
  deleteUserRequest,
  getAuthCollapsed,
  getHoldState,
  getPreferencesFromServer,
  getReturnMode,
  type HoldState,
  listUserRequests,
  releaseHold,
  type ReturnMode,
  setAuthCollapsed,
  setHold,
  setReturnMode as persistReturnModeLocal,
  setReturnModeOnServer,
  type UserRequest,
} from "./api";
import { HoldButton } from "./hold-button";
import { SavedCommands } from "./saved-commands";

interface StatusState {
  message: string;
  tone: "info" | "success" | "error";
}

interface IdentityState {
  userId?: string;
  keyHint?: string;
}

export function UserRequestsPage() {
  const { apiKey } = useApiKey();
  const [status, setStatus] = useState<StatusState | null>(null);
  const [identity, setIdentity] = useState<IdentityState | null>(null);
  const [pending, setPending] = useState<UserRequest[]>([]);
  const [consumed, setConsumed] = useState<UserRequest[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [newContent, setNewContent] = useState("");
  const [taskId, setTaskId] = useState("");
  const [pickedRequestId, setPickedRequestId] = useState<string | null>(null);
  const [editorBackup, setEditorBackup] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [isDeletingAllPending, setIsDeletingAllPending] = useState(false);
  const [pendingDeletes, setPendingDeletes] = useState<Record<string, boolean>>(
    {}
  );
  const [holdState, setHoldState] = useState<HoldState>({
    active: false,
    waiting: false,
    remaining_secs: 0,
  });
  const [isAuthCollapsed, setIsAuthCollapsedState] = useState(() =>
    getAuthCollapsed()
  );
  const [showDeleteAllConfirm, setShowDeleteAllConfirm] = useState(false);
  const [returnMode, setReturnModeState] = useState<ReturnMode>(() =>
    getReturnMode()
  );

  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const holdPollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const pollControlsRef = useRef<{
    schedule: (delay: number) => void;
    refresh: () => void;
  } | null>(null);

  useEffect(() => {
    if (!apiKey) {
      setPending([]);
      setConsumed([]);
      setIdentity(null);
      setStatus({ message: "Disconnected.", tone: "info" });
      setIsAuthCollapsedState(false); // Expand when disconnected
      setAuthCollapsed(false);
      return;
    }

    let disposed = false;
    let inFlight: AbortController | null = null;

    async function fetchData(initial: boolean) {
      if (disposed) return;

      if (initial) {
        setIsLoading(true);
        setStatus({ message: "Connected. Fetching requests…", tone: "info" });
      }

      if (inFlight) {
        inFlight.abort();
      }
      const controller = new AbortController();
      inFlight = controller;

      try {
        // Fetch both requests and preferences in parallel
        const [data, prefs] = await Promise.all([
          listUserRequests(apiKey, controller.signal),
          initial ? getPreferencesFromServer(apiKey, controller.signal).catch(() => null) : Promise.resolve(null),
        ]);
        if (disposed) return;

        setPending(data.pending ?? []);
        setConsumed(data.consumed ?? []);
        setIdentity({ userId: data.user_id, keyHint: data.key_hint });
        setStatus({
          message: identityMessage(data.user_id, data.key_hint),
          tone: "success",
        });

        // Update return mode from server preference (if available)
        if (prefs && prefs.return_mode) {
          setReturnModeState(prefs.return_mode);
          // Also sync to localStorage
          persistReturnModeLocal(prefs.return_mode);
        }

        // Auto-collapse the Authenticate panel on successful authentication
        setIsAuthCollapsedState(true);
        setAuthCollapsed(true);
        schedule(5000);
      } catch (error) {
        if (disposed || controller.signal.aborted) return;
        setStatus({
          message:
            error instanceof Error
              ? error.message
              : "Failed to fetch requests.",
          tone: "error",
        });
        setIsAuthCollapsedState(false); // Expand on error
        setAuthCollapsed(false);
        schedule(8000);
      } finally {
        if (initial && !disposed) {
          setIsLoading(false);
        }
      }
    }

    function schedule(delay: number) {
      if (disposed) return;
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current);
      }
      pollTimerRef.current = setTimeout(() => fetchData(false), delay);
    }

    pollControlsRef.current = {
      schedule,
      refresh: () => fetchData(false),
    };

    fetchData(true);

    return () => {
      disposed = true;
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current);
        pollTimerRef.current = null;
      }
      if (inFlight) {
        inFlight.abort();
      }
      pollControlsRef.current = null;
    };
  }, [apiKey]);

  // Poll hold state when hold is active
  useEffect(() => {
    if (!apiKey || !holdState.active) {
      if (holdPollRef.current) {
        clearInterval(holdPollRef.current);
        holdPollRef.current = null;
      }
      return;
    }

    const fetchHoldState = async () => {
      try {
        const key = normalizeApiKey(apiKey);
        if (!key) return;
        const state = await getHoldState(key);
        setHoldState(state);
        // If hold is no longer active, stop polling
        if (!state.active && holdPollRef.current) {
          clearInterval(holdPollRef.current);
          holdPollRef.current = null;
        }
      } catch {
        // Ignore errors during hold state polling
      }
    };

    // Poll every second for accurate countdown
    holdPollRef.current = setInterval(fetchHoldState, 1000);

    return () => {
      if (holdPollRef.current) {
        clearInterval(holdPollRef.current);
        holdPollRef.current = null;
      }
    };
  }, [apiKey, holdState.active]);

  const handleRefresh = useCallback(() => {
    pollControlsRef.current?.refresh();
  }, []);

  const handleCreateRequest = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      setStatus({
        message: "Connect with your API key before creating requests.",
        tone: "error",
      });
      return;
    }
    const trimmed = newContent.trim();
    if (!trimmed) {
      setStatus({ message: "Content cannot be empty.", tone: "error" });
      return;
    }

    setIsSubmitting(true);
    try {
      await createUserRequest(key, trimmed, taskId.trim() || undefined);
      setNewContent("");
      setPickedRequestId(null);
      setEditorBackup(null);
      // If hold was active, it will be automatically released by the server
      if (holdState.active) {
        setHoldState({ active: false, waiting: false, remaining_secs: 0 });
        setStatus({
          message: "Request queued and delivered to waiting agent.",
          tone: "success",
        });
      } else {
        setStatus({ message: "Request queued successfully.", tone: "success" });
      }
      pollControlsRef.current?.schedule(0);
    } catch (error) {
      setStatus({
        message:
          error instanceof Error ? error.message : "Failed to create request.",
        tone: "error",
      });
    } finally {
      setIsSubmitting(false);
    }
  }, [apiKey, newContent, taskId, holdState.active]);

  const handleActivateHold = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      setStatus({
        message: "Connect with your API key before activating hold.",
        tone: "error",
      });
      return;
    }
    try {
      const state = await setHold(key);
      setHoldState(state);
      // No status message - the Hold button provides sufficient visual feedback
    } catch (error) {
      setStatus({
        message:
          error instanceof Error ? error.message : "Failed to activate hold.",
        tone: "error",
      });
    }
  }, [apiKey]);

  const handleReleaseHold = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      setStatus({
        message: "Connect with your API key before releasing hold.",
        tone: "error",
      });
      return;
    }
    try {
      const state = await releaseHold(key);
      setHoldState(state);
      // No status message - the Hold button provides sufficient visual feedback
    } catch (error) {
      setStatus({
        message:
          error instanceof Error ? error.message : "Failed to release hold.",
        tone: "error",
      });
    }
  }, [apiKey]);

  const handleReturnModeChange = useCallback(async (mode: ReturnMode) => {
    setReturnModeState(mode);
    // Persist to localStorage as fallback
    persistReturnModeLocal(mode);

    // Also persist to server so MCP tool uses this preference
    const key = normalizeApiKey(apiKey);
    if (key) {
      try {
        await setReturnModeOnServer(key, mode);
        setStatus({
          message: `Return mode set to "${mode === "first" ? "first only" : "all commands"}".`,
          tone: "success",
        });
      } catch (error) {
        console.error("Failed to persist return mode to server:", error);
        // Don't show error - localStorage fallback still works
      }
    }
  }, [apiKey]);

  const handleDeleteRequest = useCallback(
    async (requestId: string) => {
      const key = normalizeApiKey(apiKey);
      if (!key) {
        setStatus({
          message: "Connect with your API key before deleting.",
          tone: "error",
        });
        return;
      }
      setPendingDeletes((prev) => ({ ...prev, [requestId]: true }));
      try {
        await deleteUserRequest(key, requestId);
        setStatus({ message: "Request deleted.", tone: "success" });
        if (requestId === pickedRequestId) {
          setPickedRequestId(null);
          setEditorBackup(null);
        }
        pollControlsRef.current?.schedule(0);
      } catch (error) {
        setStatus({
          message:
            error instanceof Error
              ? error.message
              : "Failed to delete request.",
          tone: "error",
        });
      } finally {
        setPendingDeletes((prev) => {
          const next = { ...prev };
          delete next[requestId];
          return next;
        });
      }
    },
    [apiKey, pickedRequestId]
  );

  const maskedKeySuffix = useMemo(() => {
    if (!identity?.keyHint) return "";
    return `token •••${identity.keyHint}`;
  }, [identity]);

  const isEditorDisabled = !apiKey || isSubmitting;

  // Opens the confirmation dialog for deleting all pending requests
  const handleDeleteAllPendingClick = useCallback(() => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      setStatus({
        message: "Connect with your API key before deleting.",
        tone: "error",
      });
      return;
    }
    setShowDeleteAllConfirm(true);
  }, [apiKey]);

  // Actually performs the deletion after user confirms
  const handleDeleteAllPendingConfirm = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      return;
    }
    setIsDeletingAllPending(true);
    try {
      const deleted = await deleteAllPendingRequests(key);
      setStatus({
        message: deleted
          ? `Deleted ${deleted} pending request${deleted === 1 ? "" : "s"}.`
          : "No pending requests to delete.",
        tone: "success",
      });
      pollControlsRef.current?.schedule(0);
    } catch (error) {
      setStatus({
        message:
          error instanceof Error
            ? error.message
            : "Failed to delete pending requests.",
        tone: "error",
      });
    } finally {
      setIsDeletingAllPending(false);
    }
  }, [apiKey]);

  const handleSelectSavedCommand = useCallback((content: string) => {
    setPickedRequestId(null);
    setEditorBackup(null);
    setNewContent(content);
    setStatus({ message: "Command loaded into editor.", tone: "success" });
  }, []);

  const handleSaveCurrentContent = useCallback((label: string) => {
    setStatus({
      message: `Saved "${label}" to your commands.`,
      tone: "success",
    });
  }, []);

  const handleEditorChange = useCallback(
    (event: ChangeEvent<HTMLTextAreaElement>) => {
      if (pickedRequestId) {
        setPickedRequestId(null);
        setEditorBackup(null);
      }
      setNewContent(event.target.value);
    },
    [pickedRequestId]
  );

  /**
   * handleEditInEditor loads a consumed directive into the editor for modification.
   * It preserves the current editor content as a backup in case the user wants to restore it.
   */
  const handleEditInEditor = useCallback(
    (request: UserRequest) => {
      if (!apiKey) {
        setStatus({
          message: "Connect with your API key before editing directives.",
          tone: "error",
        });
        return;
      }
      if (isSubmitting) {
        setStatus({
          message: "Please wait for the current submission to finish.",
          tone: "info",
        });
        return;
      }

      // If already picked, toggle it off (put back)
      if (pickedRequestId === request.id) {
        setPickedRequestId(null);
        setNewContent(editorBackup ?? "");
        setEditorBackup(null);
        setStatus({
          message: "Put the directive back into history.",
          tone: "info",
        });
        return;
      }

      // Backup current content before loading the consumed directive
      setEditorBackup((prev) => (pickedRequestId ? prev : newContent));
      setPickedRequestId(request.id);
      setNewContent(request.content);
      setStatus({
        message: "Loaded consumed directive into the editor.",
        tone: "success",
      });
    },
    [apiKey, editorBackup, isSubmitting, newContent, pickedRequestId]
  );

  /**
   * handleAddToPending re-queues a consumed directive directly to the pending list.
   * This creates a new pending request with the same content as the consumed one.
   */
  const handleAddToPending = useCallback(
    async (request: UserRequest) => {
      const key = normalizeApiKey(apiKey);
      if (!key) {
        setStatus({
          message: "Connect with your API key before re-queuing.",
          tone: "error",
        });
        return;
      }

      setIsSubmitting(true);
      try {
        await createUserRequest(
          key,
          request.content,
          request.task_id || undefined
        );
        setStatus({
          message: "Directive re-queued to pending.",
          tone: "success",
        });
        pollControlsRef.current?.schedule(0);
      } catch (error) {
        setStatus({
          message:
            error instanceof Error
              ? error.message
              : "Failed to re-queue directive.",
          tone: "error",
        });
      } finally {
        setIsSubmitting(false);
      }
    },
    [apiKey]
  );

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <ClipboardList className="h-4 w-4" />
          <span>MCP Tools</span>
        </div>
        <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
          Get User Requests Console
        </h1>
        <p className="max-w-2xl text-lg text-muted-foreground">
          Queue fresh directives for your AI assistants and inspect everything
          they have already consumed. This page manages the{" "}
          <code className="text-sm">get_user_request</code> MCP tool inputs tied
          to your bearer token.
        </p>
        <div className="max-w-2xl space-y-2 text-sm text-muted-foreground">
          <p>
            <strong>How it works:</strong> When your AI agent calls{" "}
            <code className="text-xs">get_user_request</code>, the server
            returns the newest pending directive from this queue and marks it as
            consumed. This allows you to provide real-time feedback or adjust
            the agent's behavior mid-task.
          </p>
          <p>
            <strong>Hold feature:</strong> Click <em>Hold</em> before your agent
            queries for requests. The hold remains active indefinitely until you
            submit a command or manually release it. Once an agent connects and
            starts waiting, a 20-second countdown begins to prevent the agent
            connection from timing out.
          </p>
        </div>
      </section>

      <Card className="border border-border/60 bg-card shadow-sm">
        <CardHeader
          className={cn(
            "cursor-pointer transition-all",
            isAuthCollapsed && "pb-4"
          )}
          onClick={() => {
            if (status?.tone === "success") {
              const newValue = !isAuthCollapsed;
              setIsAuthCollapsedState(newValue);
              setAuthCollapsed(newValue);
            }
          }}
        >
          <div className="flex items-center justify-between">
            <CardTitle className="text-lg text-foreground">
              Authenticate
            </CardTitle>
            {isAuthCollapsed && identity?.keyHint && (
              <span className="text-xs text-muted-foreground">
                token •••{identity.keyHint}
              </span>
            )}
          </div>
          {!isAuthCollapsed && (
            <p className="text-sm text-muted-foreground">
              Enter the bearer token shared with your AI agent. The token stays
              in your browser storage only.
            </p>
          )}
        </CardHeader>
        {!isAuthCollapsed && (
          <CardContent className="space-y-4">
            <ApiKeyInput showRefresh onRefresh={handleRefresh} />
            {status && (
              <StatusBanner status={status} maskedKeySuffix={maskedKeySuffix} />
            )}
          </CardContent>
        )}
      </Card>

      <Card className="border-2 border-primary/40 bg-primary/5 shadow-md dark:bg-primary/10">
        <CardHeader>
          <CardTitle className="text-lg font-semibold text-foreground">
            Create user directive
          </CardTitle>
          <p className="text-sm text-muted-foreground">
            Draft a new instruction for your AI agent. Requests are delivered in
            FIFO order (oldest first) when they call{" "}
            <code>get_user_request</code>.
          </p>
          <div className="mt-3 flex items-center gap-3">
            <span className="text-sm text-muted-foreground">Return mode:</span>
            <div className="inline-flex rounded-md border border-border bg-background shadow-sm">
              <button
                type="button"
                onClick={() => handleReturnModeChange("all")}
                className={cn(
                  "rounded-l-md px-3 py-1.5 text-sm font-medium transition-colors",
                  returnMode === "all"
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground"
                )}
              >
                All commands
              </button>
              <button
                type="button"
                onClick={() => handleReturnModeChange("first")}
                className={cn(
                  "rounded-r-md border-l border-border px-3 py-1.5 text-sm font-medium transition-colors",
                  returnMode === "first"
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground"
                )}
              >
                First (earliest) only
              </button>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <Textarea
            value={newContent}
            onChange={handleEditorChange}
            onKeyDown={(e: KeyboardEvent<HTMLTextAreaElement>) => {
              // Enter without modifiers: submit the form
              // Ctrl+Enter or Shift+Enter: insert newline (default behavior)
              if (e.key === "Enter" && !e.ctrlKey && !e.shiftKey && !e.metaKey) {
                e.preventDefault();
                if (!isEditorDisabled && newContent.trim()) {
                  handleCreateRequest();
                }
              }
            }}
            placeholder="Describe the feedback or task for your AI assistant…"
            disabled={isEditorDisabled}
            className="border-primary/20 bg-background focus-visible:ring-primary/30"
          />
          <div className="flex flex-col gap-3 md:flex-row md:items-center">
            <Input
              value={taskId}
              onChange={(event: ChangeEvent<HTMLInputElement>) =>
                setTaskId(event.target.value)
              }
              placeholder="Optional task identifier"
              disabled={isEditorDisabled}
              className="md:flex-1"
            />
            <div className="flex gap-2">
              <HoldButton
                isActive={holdState.active}
                isWaiting={holdState.waiting}
                remainingSecs={holdState.remaining_secs}
                onActivate={handleActivateHold}
                onRelease={handleReleaseHold}
                disabled={!apiKey}
              />
              <Button onClick={handleCreateRequest} disabled={isEditorDisabled} title="Queue request">
                <Send className="mr-2 h-4 w-4" />
                {isSubmitting ? "Queuing…" : "Queue"}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <SavedCommands
        currentContent={newContent}
        onSelectCommand={handleSelectSavedCommand}
        onSaveCurrentContent={handleSaveCurrentContent}
        disabled={isEditorDisabled}
      />

      <section className="grid gap-6 lg:grid-cols-2">
        <div className="space-y-4">
          <header className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-semibold text-foreground">
                Pending requests
              </h2>
              <Badge variant="secondary">{pending.length}</Badge>
            </div>
            {pending.length > 0 && (
              <Button
                type="button"
                variant="destructive"
                size="sm"
                onClick={handleDeleteAllPendingClick}
                disabled={isDeletingAllPending}
              >
                <Trash2 className="mr-2 h-4 w-4" />
                {isDeletingAllPending ? "Deleting…" : "Delete all"}
              </Button>
            )}
          </header>
          <div className="space-y-4">
            {isLoading && !pending.length ? (
              <EmptyState message="Loading pending requests…" />
            ) : pending.length === 0 ? (
              <EmptyState message="No pending directives right now." />
            ) : (
              pending.map((request) => (
                <PendingRequestCard
                  key={request.id}
                  request={request}
                  onDelete={handleDeleteRequest}
                  deleting={Boolean(pendingDeletes[request.id])}
                />
              ))
            )}
          </div>
        </div>
        <div className="space-y-4">
          <header className="flex items-center justify-between">
            <h2 className="text-xl font-semibold text-foreground">
              Consumed history
            </h2>
            <Badge variant="outline">{consumed.length}</Badge>
          </header>
          <div className="space-y-4">
            {consumed.length === 0 ? (
              <EmptyState message="Nothing consumed yet." subtle />
            ) : (
              consumed.map((request) => (
                <ConsumedCard
                  key={request.id}
                  request={request}
                  onDelete={handleDeleteRequest}
                  deleting={Boolean(pendingDeletes[request.id])}
                  onEditInEditor={handleEditInEditor}
                  onAddToPending={handleAddToPending}
                  isPicked={pickedRequestId === request.id}
                  isEditorDisabled={isEditorDisabled}
                />
              ))
            )}
          </div>
        </div>
      </section>

      <ConfirmDialog
        open={showDeleteAllConfirm}
        onOpenChange={setShowDeleteAllConfirm}
        title="Delete all pending requests?"
        description="This will remove all pending requests from the queue. Consumed history will not be affected."
        confirmText="Delete all"
        cancelText="Cancel"
        confirmVariant="destructive"
        onConfirm={handleDeleteAllPendingConfirm}
        isLoading={isDeletingAllPending}
      />
    </div>
  );
}

function identityMessage(userId?: string, keyHint?: string) {
  const user = userId || "unknown user";
  const suffix = keyHint ? `token •••${keyHint}` : "token hidden";
  return `Linked identity: ${user} (${suffix})`;
}

function StatusBanner({
  status,
  maskedKeySuffix,
  className,
}: {
  status: StatusState;
  maskedKeySuffix: string;
  className?: string;
}) {
  const toneStyles = {
    info: "border-border bg-muted text-muted-foreground",
    success:
      "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-200 dark:border-emerald-500/40",
    error:
      "border-rose-500/40 bg-rose-500/10 text-rose-700 dark:text-rose-200 dark:border-rose-500/40",
  } as const;

  return (
    <div
      className={cn(
        "flex flex-col gap-1 rounded-lg border px-4 py-3 text-sm transition-colors",
        toneStyles[status.tone],
        className
      )}
    >
      <span>{status.message}</span>
      {status.tone === "success" && maskedKeySuffix && (
        <span className="text-xs text-inherit/80">{maskedKeySuffix}</span>
      )}
    </div>
  );
}

function EmptyState({
  message,
  subtle = false,
}: {
  message: string;
  subtle?: boolean;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border border-dashed px-4 py-6 text-sm text-muted-foreground",
        subtle ? "bg-muted/50" : "bg-muted"
      )}
    >
      {message}
    </div>
  );
}

function PendingRequestCard({
  request,
  onDelete,
  deleting,
}: {
  request: UserRequest;
  onDelete: (id: string) => void;
  deleting: boolean;
}) {
  return (
    <Card className="border border-primary/30 bg-card shadow-sm min-h-32 max-h-56 flex flex-col">
      <CardHeader className="gap-2 flex-1 min-h-0 overflow-hidden flex flex-col">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground shrink-0">
          <span>ID: {request.id}</span>
          <span>Queued: {formatDate(request.created_at)}</span>
          {request.task_id && <span>Task: {request.task_id}</span>}
        </div>
        <div className="flex-1 min-h-0 overflow-y-auto">
          <CardTitle className="text-base font-semibold text-foreground whitespace-pre-wrap line-clamp-none">
            {request.content}
          </CardTitle>
        </div>
      </CardHeader>
      <CardContent className="flex justify-end shrink-0 pt-0">
        <Button
          variant="destructive"
          size="icon"
          onClick={() => onDelete(request.id)}
          disabled={deleting}
          title="Delete request"
        >
          <Trash2 className="h-4 w-4" />
          <span className="sr-only">{deleting ? "Deleting…" : "Delete"}</span>
        </Button>
      </CardContent>
    </Card>
  );
}

function ConsumedCard({
  request,
  onDelete,
  deleting,
  onEditInEditor,
  onAddToPending,
  isPicked,
  isEditorDisabled,
}: {
  request: UserRequest;
  onDelete: (id: string) => void;
  deleting: boolean;
  onEditInEditor: (request: UserRequest) => void;
  onAddToPending: (request: UserRequest) => void;
  isPicked: boolean;
  isEditorDisabled: boolean;
}) {
  return (
    <Card className="border border-border/60 bg-card min-h-32 max-h-56 flex flex-col">
      <CardHeader className="gap-2 flex-1 min-h-0 overflow-hidden flex flex-col">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground shrink-0">
          <span>ID: {request.id}</span>
          <span>Queued: {formatDate(request.created_at)}</span>
          {request.consumed_at && (
            <span>Delivered: {formatDate(request.consumed_at)}</span>
          )}
          {request.task_id && <span>Task: {request.task_id}</span>}
        </div>
        <div className="flex-1 min-h-0 overflow-y-auto">
          <CardTitle className="text-base font-semibold text-foreground whitespace-pre-wrap line-clamp-none">
            {request.content}
          </CardTitle>
        </div>
      </CardHeader>
      <CardContent className="flex flex-wrap justify-end gap-2 shrink-0 pt-0">
        {isPicked ? (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => onEditInEditor(request)}
            disabled={isEditorDisabled}
            title="Put back to history"
            className="gap-1.5"
          >
            <Undo2 className="h-4 w-4" />
            <span>Put Back</span>
          </Button>
        ) : (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                disabled={isEditorDisabled}
                className="gap-1.5"
                title="Pick up this directive"
              >
                <Package className="h-4 w-4" />
                <ChevronDown className="h-3.5 w-3.5 opacity-70" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              <DropdownMenuItem
                onClick={() => onAddToPending(request)}
                className="gap-2 cursor-pointer"
              >
                <RotateCcw className="h-4 w-4" />
                <span>Add to Pending</span>
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() => onEditInEditor(request)}
                className="gap-2 cursor-pointer"
              >
                <Edit3 className="h-4 w-4" />
                <span>Edit in Editor</span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <Button
          variant="ghost"
          size="icon"
          onClick={() => onDelete(request.id)}
          disabled={deleting}
          title="Delete from history"
          className="h-9 w-9 text-muted-foreground hover:text-destructive"
        >
          <Trash2 className="h-4 w-4" />
          <span className="sr-only">{deleting ? "Deleting…" : "Delete"}</span>
        </Button>
      </CardContent>
    </Card>
  );
}

function formatDate(value?: string | null) {
  if (!value) return "N/A";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}
