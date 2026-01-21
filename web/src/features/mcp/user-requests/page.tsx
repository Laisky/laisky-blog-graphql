import { closestCenter, DndContext, type DragEndEvent, KeyboardSensor, PointerSensor, useSensor, useSensors } from '@dnd-kit/core';
import { arrayMove, SortableContext, sortableKeyboardCoordinates, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { ChevronDown, ChevronUp, ClipboardList, Send, Trash2 } from 'lucide-react';
import type { ChangeEvent, KeyboardEvent } from 'react';
import { useCallback, useEffect, useRef, useState } from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ConfirmDialog } from '@/components/ui/confirm-dialog';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { Textarea } from '@/components/ui/textarea';
import { normalizeApiKey, useApiKey } from '@/lib/api-key-context';
import { cn } from '@/lib/utils';

import {
  createUserRequest,
  deleteAllPendingRequests,
  deleteConsumedRequests,
  deleteUserRequest,
  getDescriptionCollapsed,
  getHoldState,
  getPreferencesFromServer,
  getReturnMode,
  type HoldState,
  listUserRequests,
  setReturnMode as persistReturnModeLocal,
  releaseHold,
  reorderUserRequests,
  type ReturnMode,
  setDescriptionCollapsed,
  setHold,
  setReturnModeOnServer,
  type UserRequest,
} from './api';
import { HoldButton } from './hold-button';
import { ConsumedCard, EmptyState, PendingRequestCard } from './request-cards';
import { SavedCommands } from './saved-commands';
import { TaskIdSelector, useTaskIdHistory } from './task-id-selector';

type DeleteOption = {
  label: string;
  keepCount?: number;
  keepDays?: number;
};

const DELETE_OPTIONS: DeleteOption[] = [
  { label: 'Delete everything' },
  { label: 'Keep recent 50', keepCount: 50 },
  { label: 'Keep last 7 days', keepDays: 7 },
  { label: 'Keep last 30 days', keepDays: 30 },
];

export function UserRequestsPage() {
  const { apiKey } = useApiKey();
  const [pending, setPending] = useState<UserRequest[]>([]);
  const [consumed, setConsumed] = useState<UserRequest[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [newContent, setNewContent] = useState('');
  const [taskId, setTaskId] = useState('');
  const [pickedRequestId, setPickedRequestId] = useState<string | null>(null);
  const [editorBackup, setEditorBackup] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [isDeletingAllPending, setIsDeletingAllPending] = useState(false);
  const [pendingDeletes, setPendingDeletes] = useState<Record<string, boolean>>({});
  const [holdState, setHoldState] = useState<HoldState>({
    active: false,
    waiting: false,
    remaining_secs: 0,
  });
  const [showDeleteAllConfirm, setShowDeleteAllConfirm] = useState(false);
  const [returnMode, setReturnModeState] = useState<ReturnMode>(() => getReturnMode());
  const [isDescriptionCollapsed, setIsDescriptionCollapsed] = useState(() => getDescriptionCollapsed());
  const [selectedDeleteOptionIdx, setSelectedDeleteOptionIdx] = useState(() => {
    const saved = localStorage.getItem('mcp_delete_consumed_option');
    return saved ? parseInt(saved, 10) : 0;
  });
  const [showDeleteConsumedConfirm, setShowDeleteConsumedConfirm] = useState(false);
  const [isDeletingConsumed, setIsDeletingConsumed] = useState(false);
  const [visibleConsumedCount, setVisibleConsumedCount] = useState(10);
  const [hasMoreConsumed, setHasMoreConsumed] = useState(true);
  const [isLoadingMore, setIsLoadingMore] = useState(false);

  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  );

  const { recordUsage: recordTaskIdUsage } = useTaskIdHistory();

  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const holdPollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const pollControlsRef = useRef<{
    schedule: (delay: number) => void;
    refresh: () => void;
  } | null>(null);

  const visibleCountRef = useRef(visibleConsumedCount);
  useEffect(() => {
    visibleCountRef.current = visibleConsumedCount;
  }, [visibleConsumedCount]);

  useEffect(() => {
    if (!apiKey) {
      setPending([]);
      setConsumed([]);
      setVisibleConsumedCount(10);
      setHasMoreConsumed(true);
      return;
    }

    let disposed = false;
    let inFlight: AbortController | null = null;

    async function fetchData(initial: boolean) {
      if (disposed) return;

      if (initial) {
        setIsLoading(true);
      }

      if (inFlight) {
        inFlight.abort();
      }
      const controller = new AbortController();
      inFlight = controller;

      try {
        // Fetch both requests and preferences in parallel
        const [data, prefs] = await Promise.all([
          listUserRequests(apiKey, {
            limit: visibleCountRef.current,
            signal: controller.signal,
          }),
          initial ? getPreferencesFromServer(apiKey, controller.signal).catch(() => null) : Promise.resolve(null),
        ]);
        if (disposed) return;

        setPending(data.pending ?? []);
        setConsumed(data.consumed ?? []);
        setHasMoreConsumed((data.consumed?.length ?? 0) >= visibleCountRef.current);

        // Update return mode from server preference only if localStorage has no value
        // This prevents overwriting user's local choice with server default
        if (prefs && prefs.return_mode) {
          const localMode = getReturnMode();
          // Only update if localStorage is empty or has default value AND server has non-default
          if (localMode === 'all' && prefs.return_mode !== 'all') {
            setReturnModeState(prefs.return_mode);
            persistReturnModeLocal(prefs.return_mode);
          } else {
            // Keep localStorage value as source of truth for UI
            setReturnModeState(localMode);
          }
        }

        schedule(5000);
      } catch (error) {
        if (disposed || controller.signal.aborted) return;
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

  //   const handleRefresh = useCallback(() => {
  //     setVisibleConsumedCount(10);
  //     pollControlsRef.current?.refresh();
  //   }, []);

  const handleLoadMore = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key || consumed.length === 0) return;

    const lastItem = consumed[consumed.length - 1];
    setIsLoadingMore(true);
    try {
      const data = await listUserRequests(key, {
        cursor: lastItem.id,
        limit: 10,
      });
      const newItems = data.consumed ?? [];
      setConsumed((prev) => [...prev, ...newItems]);
      setVisibleConsumedCount((prev) => prev + newItems.length);
      setHasMoreConsumed(newItems.length >= 10);
    } catch (error) {
    } finally {
      setIsLoadingMore(false);
    }
  }, [apiKey, consumed]);

  const handleCreateRequest = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      return;
    }
    const trimmed = newContent.trim();
    if (!trimmed) {
      return;
    }

    setIsSubmitting(true);
    try {
      // Record task ID usage for history before creating request
      if (taskId.trim()) {
        recordTaskIdUsage(taskId.trim());
      }
      const createdRequest = await createUserRequest(key, trimmed, taskId.trim() || undefined);
      setNewContent('');
      setPickedRequestId(null);
      setEditorBackup(null);
      // Check if the command was directly delivered to a waiting agent
      // The server marks the request as "consumed" if it was sent to a waiting agent
      if (createdRequest.status === 'consumed') {
        // Command was delivered directly to the waiting agent
        setHoldState({ active: false, waiting: false, remaining_secs: 0 });
      } else if (holdState.active) {
        // Hold was active but no agent was waiting, command is queued
      } else {
      }
      pollControlsRef.current?.schedule(0);
    } catch (error) {
    } finally {
      setIsSubmitting(false);
    }
  }, [apiKey, newContent, taskId, holdState.active, recordTaskIdUsage]);

  const handleActivateHold = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      return;
    }
    try {
      const state = await setHold(key);
      setHoldState(state);
      // No status message - the Hold button provides sufficient visual feedback
    } catch (error) {}
  }, [apiKey]);

  const handleReleaseHold = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      return;
    }
    try {
      const state = await releaseHold(key);
      setHoldState(state);
      // No status message - the Hold button provides sufficient visual feedback
    } catch (error) {}
  }, [apiKey]);

  const handleReturnModeChange = useCallback(
    async (mode: ReturnMode) => {
      setReturnModeState(mode);
      // Persist to localStorage as fallback
      persistReturnModeLocal(mode);

      // Also persist to server so MCP tool uses this preference
      const key = normalizeApiKey(apiKey);
      if (key) {
        try {
          await setReturnModeOnServer(key, mode);
        } catch (error) {
          console.error('Failed to persist return mode to server:', error);
          // Show warning to user - the preference was saved locally but may not persist
          // across devices or if browser data is cleared
        }
      }
    },
    [apiKey]
  );

  const handleDeleteRequest = useCallback(
    async (request: UserRequest) => {
      const key = normalizeApiKey(apiKey);
      if (!key) {
        return;
      }
      setPendingDeletes((prev) => ({ ...prev, [request.id]: true }));
      try {
        await deleteUserRequest(key, request.id, { taskId: request.task_id });
        if (request.id === pickedRequestId) {
          setPickedRequestId(null);
          setEditorBackup(null);
        }
        pollControlsRef.current?.schedule(0);
      } catch (error) {
      } finally {
        setPendingDeletes((prev) => {
          const next = { ...prev };
          delete next[request.id];
          return next;
        });
      }
    },
    [apiKey, pickedRequestId]
  );

  const handleDragEnd = useCallback(
    async (event: DragEndEvent) => {
      const { active, over } = event;
      if (!over || active.id === over.id) {
        return;
      }

      const oldIndex = pending.findIndex((r) => r.id === active.id);
      const newIndex = pending.findIndex((r) => r.id === over.id);

      const newPending = arrayMove(pending, oldIndex, newIndex);
      setPending(newPending);

      const key = normalizeApiKey(apiKey);
      if (!key) return;

      try {
        await reorderUserRequests(
          key,
          newPending.map((r) => r.id)
        );
      } catch (error) {
        console.error('Failed to reorder requests:', error);
        // Optionally revert on failure, but usually better to keep local state
        // and let the next poll fix it if it's a transient error.
      }
    },
    [apiKey, pending]
  );

  const isEditorDisabled = !apiKey || isSubmitting;

  // Opens the confirmation dialog for deleting all pending requests
  const handleDeleteAllPendingClick = useCallback(() => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
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
      await deleteAllPendingRequests(key, {
        allTasks: true,
      });
      pollControlsRef.current?.schedule(0);
    } catch (error) {
    } finally {
      setIsDeletingAllPending(false);
    }
  }, [apiKey]);

  const handleDeleteConsumedClick = useCallback(() => {
    const key = normalizeApiKey(apiKey);
    if (!key) {
      return;
    }
    setShowDeleteConsumedConfirm(true);
  }, [apiKey]);

  const handleDeleteConsumedConfirm = useCallback(async () => {
    const key = normalizeApiKey(apiKey);
    if (!key) return;

    setIsDeletingConsumed(true);
    try {
      const option = DELETE_OPTIONS[selectedDeleteOptionIdx];
      await deleteConsumedRequests(key, {
        keepCount: option.keepCount,
        keepDays: option.keepDays,
        allTasks: true,
      });
      pollControlsRef.current?.schedule(0);
    } catch (error) {
    } finally {
      setIsDeletingConsumed(false);
    }
  }, [apiKey, selectedDeleteOptionIdx]);

  const handleSelectDeleteOption = useCallback((idx: number) => {
    setSelectedDeleteOptionIdx(idx);
    localStorage.setItem('mcp_delete_consumed_option', idx.toString());
  }, []);

  const handleSelectSavedCommand = useCallback((content: string) => {
    setPickedRequestId(null);
    setEditorBackup(null);
    setNewContent(content);
  }, []);

  const handleSaveCurrentContent = useCallback((_label: string) => {}, []);

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
   * handleEditInEditor loads a directive (pending or consumed) into the editor for modification.
   * It preserves the current editor content as a backup in case the user wants to restore it.
   */
  const handleEditInEditor = useCallback(
    (request: UserRequest) => {
      if (!apiKey) {
        return;
      }
      if (isSubmitting) {
        return;
      }

      // If already picked, toggle it off (put back)
      if (pickedRequestId === request.id) {
        setPickedRequestId(null);
        setNewContent(editorBackup ?? '');
        setEditorBackup(null);
        return;
      }

      // Backup current content before loading the directive
      setEditorBackup((prev) => (pickedRequestId ? prev : newContent));
      setPickedRequestId(request.id);
      setNewContent(request.content);
    },
    [apiKey, editorBackup, isSubmitting, newContent, pickedRequestId]
  );

  const handlePickUpPending = useCallback(
    async (request: UserRequest) => {
      if (!apiKey) return;

      // Load into editor
      setNewContent(request.content);
      if (request.task_id) {
        setTaskId(request.task_id);
      }

      // Delete from pending
      await handleDeleteRequest(request);
    },
    [apiKey, handleDeleteRequest]
  );

  const handleCopyPending = useCallback((request: UserRequest) => {
    setNewContent(request.content);
    if (request.task_id) {
      setTaskId(request.task_id);
    }
  }, []);

  /**
   * handleAddToPending re-queues a consumed directive directly to the pending list.
   * This creates a new pending request with the same content as the consumed one.
   */
  const handleAddToPending = useCallback(
    async (request: UserRequest) => {
      const key = normalizeApiKey(apiKey);
      if (!key) {
        return;
      }

      setIsSubmitting(true);
      try {
        await createUserRequest(key, request.content, request.task_id || undefined);
        pollControlsRef.current?.schedule(0);
      } catch (error) {
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
        <div className="flex items-center justify-between">
          <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">Get User Requests Console</h1>
          <button
            type="button"
            onClick={() => {
              setIsDescriptionCollapsed((prev) => {
                const newValue = !prev;
                setDescriptionCollapsed(newValue);
                return newValue;
              });
            }}
            className="flex items-center gap-1 rounded px-2 py-1 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            title={isDescriptionCollapsed ? 'Show description' : 'Hide description'}
          >
            {isDescriptionCollapsed ? (
              <>
                <span>Show info</span>
                <ChevronDown className="h-4 w-4" />
              </>
            ) : (
              <>
                <span>Hide info</span>
                <ChevronUp className="h-4 w-4" />
              </>
            )}
          </button>
        </div>
        {!isDescriptionCollapsed && (
          <>
            <p className="max-w-2xl text-lg text-muted-foreground">
              Queue new directives for your AI Agents. This page manages the <code className="text-sm">get_user_request</code> MCP tool
              inputs tied to your API Key.
            </p>
            <div className="max-w-2xl space-y-2 text-sm text-muted-foreground">
              <strong>Key benefit:</strong> Your agent can interact with you while running, not just after finishing a task. If your agent
              charges per run, you can send multiple new directives in one session by calling{' '}
              <code className="text-xs">get_user_request</code> repeatedly, only one charge applies.
              <p>
                <strong>How it works:</strong> When your AI agent calls <code className="text-xs">get_user_request</code>, the server
                returns the newest pending directive from this queue and marks it as consumed. This lets your agent immediately start the
                next task after finishing the current one.
              </p>
              <p>
                <strong>Hold feature:</strong> Click <em>Hold</em> before your agent queries for requests. The hold remains active
                indefinitely until you submit a command or manually release it. Once an agent connects and starts waiting, a 20-second
                countdown begins to prevent the agent connection from timing out.
              </p>
            </div>
          </>
        )}
      </section>

      <Card className="border-2 border-primary/40 bg-primary/5 shadow-md dark:bg-primary/10">
        <CardHeader>
          <CardTitle className="text-lg font-semibold text-foreground">Create user directive</CardTitle>
          <p className="text-sm text-muted-foreground">
            Draft a new instruction for your AI agent. Requests are delivered in FIFO order (oldest first) when they call{' '}
            <code>get_user_request</code>.
          </p>
          <div className="mt-3 flex items-center gap-3">
            <span className="text-sm text-muted-foreground">Return mode:</span>
            <div className="inline-flex rounded-md border border-border bg-background shadow-sm">
              <button
                type="button"
                onClick={() => handleReturnModeChange('all')}
                className={cn(
                  'rounded-l-md px-3 py-1.5 text-sm font-medium transition-colors',
                  returnMode === 'all' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground'
                )}
              >
                All commands
              </button>
              <button
                type="button"
                onClick={() => handleReturnModeChange('first')}
                className={cn(
                  'rounded-r-md border-l border-border px-3 py-1.5 text-sm font-medium transition-colors',
                  returnMode === 'first'
                    ? 'bg-primary text-primary-foreground'
                    : 'text-muted-foreground hover:bg-muted hover:text-foreground'
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
              // Ignore if composing in IME (Input Method Editor)
              // We check both synthetic event and nativeEvent for max compatibility
              if (e.isComposing || e.nativeEvent.isComposing) {
                return;
              }

              // Ctrl+Enter or Meta+Enter (Cmd+Enter on macOS) to submit
              // Standard Enter now just inserts a newline (default textarea behavior)
              if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                e.preventDefault();
                if (!isEditorDisabled && newContent.trim()) {
                  console.debug('[UserRequests] Keyboard shortcut triggered request creation');
                  handleCreateRequest();
                }
              }
            }}
            placeholder="Describe the feedback or task for your AI assistant… (Ctrl + Enter to queue)"
            disabled={isEditorDisabled}
            className="border-primary/20 bg-background focus-visible:ring-primary/30"
          />
          <div className="flex flex-col gap-3 md:flex-row md:items-center">
            <TaskIdSelector
              value={taskId}
              onChange={setTaskId}
              disabled={isEditorDisabled}
              placeholder="Optional task identifier"
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
                {isSubmitting ? 'Queuing…' : 'Queue'}
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
              <h2 className="text-xl font-semibold text-foreground">Pending requests</h2>
              <Badge variant="secondary">{pending.length}</Badge>
            </div>
            {pending.length > 0 && (
              <Button type="button" variant="destructive" size="sm" onClick={handleDeleteAllPendingClick} disabled={isDeletingAllPending}>
                <Trash2 className="mr-2 h-4 w-4" />
                {isDeletingAllPending ? 'Deleting…' : 'Delete all'}
              </Button>
            )}
          </header>
          <div className="space-y-4">
            {isLoading && !pending.length ? (
              <EmptyState message="Loading pending requests…" />
            ) : pending.length === 0 ? (
              <EmptyState message="No pending directives right now." />
            ) : (
              <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
                <SortableContext items={pending.map((r) => r.id)} strategy={verticalListSortingStrategy}>
                  <div className="space-y-4">
                    {pending.map((request) => (
                      <PendingRequestCard
                        key={request.id}
                        request={request}
                        onDelete={handleDeleteRequest}
                        deleting={Boolean(pendingDeletes[request.id])}
                        onPickup={handlePickUpPending}
                        onCopy={handleCopyPending}
                        isEditorDisabled={isEditorDisabled}
                      />
                    ))}
                  </div>
                </SortableContext>
              </DndContext>
            )}
          </div>
        </div>
        <div className="space-y-4">
          <header className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-semibold text-foreground">Consumed history</h2>
              <Badge variant="outline">{consumed.length}</Badge>
            </div>
            {consumed.length > 0 && (
              <div className="flex items-center">
                <Button
                  type="button"
                  variant="destructive"
                  size="sm"
                  className="rounded-r-none border-r border-destructive-foreground/20"
                  onClick={handleDeleteConsumedClick}
                  disabled={isDeletingConsumed}
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  {isDeletingConsumed ? 'Deleting…' : DELETE_OPTIONS[selectedDeleteOptionIdx].label}
                </Button>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button type="button" variant="destructive" size="sm" className="rounded-l-none px-2" disabled={isDeletingConsumed}>
                      <ChevronDown className="h-4 w-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    {DELETE_OPTIONS.map((option, idx) => (
                      <DropdownMenuItem
                        key={option.label}
                        onClick={() => handleSelectDeleteOption(idx)}
                        className={idx === selectedDeleteOptionIdx ? 'bg-accent' : ''}
                      >
                        {option.label}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            )}
          </header>
          <div className="space-y-4">
            {consumed.length === 0 ? (
              <EmptyState message="Nothing consumed yet." subtle />
            ) : (
              <>
                {consumed.map((request) => (
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
                ))}
                {hasMoreConsumed && (
                  <Button
                    variant="ghost"
                    className="w-full border-2 border-dashed hover:bg-accent/50"
                    onClick={handleLoadMore}
                    disabled={isLoadingMore}
                  >
                    {isLoadingMore ? 'Loading…' : 'Load more'}
                  </Button>
                )}
              </>
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

      <ConfirmDialog
        open={showDeleteConsumedConfirm}
        onOpenChange={setShowDeleteConsumedConfirm}
        title={`Confirm: ${DELETE_OPTIONS[selectedDeleteOptionIdx].label}?`}
        description="This action cannot be undone."
        confirmText="Delete"
        cancelText="Cancel"
        confirmVariant="destructive"
        onConfirm={handleDeleteConsumedConfirm}
        isLoading={isDeletingConsumed}
      />
    </div>
  );
}
