/* eslint-disable react-refresh/only-export-components */
import { ChevronDown, Pin, PinOff, Trash2, X } from 'lucide-react';
import type { ChangeEvent, KeyboardEvent } from 'react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

const STORAGE_KEY = 'mcp-task-id-history';
const MAX_HISTORY_ITEMS = 10;
const HISTORY_UPDATE_EVENT = 'mcp-task-id-history-update';

/**
 * TaskIdEntry represents a single task identifier entry in the history.
 */
export interface TaskIdEntry {
    id: string;
    value: string;
    pinned: boolean;
    usedAt: number; // timestamp for sorting recent items
}

/**
 * loadTaskIdHistory retrieves stored task ID history from localStorage.
 * Returns an empty array if no history exists or parsing fails.
 */
export function loadTaskIdHistory(): TaskIdEntry[] {
    try {
        const raw = localStorage.getItem(STORAGE_KEY);
        if (!raw) return [];
        const parsed = JSON.parse(raw) as TaskIdEntry[];
        if (!Array.isArray(parsed)) return [];
        return parsed;
    } catch {
        return [];
    }
}

/**
 * saveTaskIdHistory persists task ID history to localStorage.
 */
export function saveTaskIdHistory(entries: TaskIdEntry[]): void {
    try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(entries));
    } catch {
        // localStorage may be unavailable
    }
}

/**
 * addOrUpdateTaskId adds a new task ID to history or updates its usedAt timestamp.
 * Pinned items are preserved; unpinned items are capped at MAX_HISTORY_ITEMS.
 */
export function addOrUpdateTaskId(history: TaskIdEntry[], value: string): TaskIdEntry[] {
    const trimmed = value.trim();
    if (!trimmed) return history;

    const existing = history.find((e) => e.value === trimmed);
    if (existing) {
        // Update usedAt timestamp
        return history.map((e) => (e.value === trimmed ? { ...e, usedAt: Date.now() } : e));
    }

    // Add new entry
    const newEntry: TaskIdEntry = {
        id: crypto.randomUUID(),
        value: trimmed,
        pinned: false,
        usedAt: Date.now(),
    };

    const updated = [newEntry, ...history];

    // Keep all pinned items + limit unpinned to MAX_HISTORY_ITEMS
    const pinned = updated.filter((e) => e.pinned);
    const unpinned = updated.filter((e) => !e.pinned).slice(0, MAX_HISTORY_ITEMS);

    return [...pinned, ...unpinned];
}

interface TaskIdSelectorProps {
    value: string;
    onChange: (value: string) => void;
    disabled?: boolean;
    placeholder?: string;
    className?: string;
    /** Called when a task ID is submitted (e.g., used in a request) */
    onSubmit?: (value: string) => void;
}

/**
 * TaskIdSelector provides a dropdown input for selecting from recent task identifiers.
 * Features:
 * - Shows up to 10 recent entries
 * - Allows pinning favorite entries to keep them at the top
 * - Supports deleting unwanted history items
 * - Keyboard navigation support
 */
export function TaskIdSelector({
    value,
    onChange,
    disabled = false,
    placeholder = 'Optional task identifier',
    className,
    onSubmit,
}: TaskIdSelectorProps) {
    const [isOpen, setIsOpen] = useState(false);
    const [history, setHistory] = useState<TaskIdEntry[]>(() => loadTaskIdHistory());
    const [highlightedIndex, setHighlightedIndex] = useState(-1);
    const inputRef = useRef<HTMLInputElement>(null);
    const dropdownRef = useRef<HTMLDivElement>(null);
    const containerRef = useRef<HTMLDivElement>(null);
    const skipFocusRef = useRef(false);

    // Sorted entries: pinned first (by usedAt desc), then unpinned (by usedAt desc)
    const sortedEntries = useMemo(() => {
        const pinned = history.filter((e) => e.pinned).sort((a, b) => b.usedAt - a.usedAt);
        const unpinned = history.filter((e) => !e.pinned).sort((a, b) => b.usedAt - a.usedAt);
        return [...pinned, ...unpinned];
    }, [history]);

    // Filter entries based on current input value
    const filteredEntries = useMemo(() => {
        const query = value.trim().toLowerCase();
        if (!query) return sortedEntries;
        return sortedEntries.filter((e) => e.pinned || e.value.toLowerCase().includes(query));
    }, [sortedEntries, value]);

    // Persist history changes
    useEffect(() => {
        saveTaskIdHistory(history);
    }, [history]);

    // Listen for history updates from useTaskIdHistory hook
    useEffect(() => {
        function handleHistoryUpdate() {
            setHistory(loadTaskIdHistory());
        }

        window.addEventListener(HISTORY_UPDATE_EVENT, handleHistoryUpdate);
        return () => {
            window.removeEventListener(HISTORY_UPDATE_EVENT, handleHistoryUpdate);
        };
    }, []);

    // Close dropdown when clicking outside
    useEffect(() => {
        function handleClickOutside(event: MouseEvent) {
            if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
                setIsOpen(false);
                setHighlightedIndex(-1);
            }
        }

        document.addEventListener('mousedown', handleClickOutside);
        return () => document.removeEventListener('mousedown', handleClickOutside);
    }, []);

    // Handle input changes
    const handleInputChange = useCallback(
        (event: ChangeEvent<HTMLInputElement>) => {
            onChange(event.target.value);
            setIsOpen(true);
            setHighlightedIndex(-1);
        },
        [onChange]
    );

    // Handle selecting an entry from dropdown
    const handleSelectEntry = useCallback(
        (entry: TaskIdEntry) => {
            onChange(entry.value);
            setHistory((prev) => addOrUpdateTaskId(prev, entry.value));
            setIsOpen(false);
            setHighlightedIndex(-1);
            skipFocusRef.current = true;
            inputRef.current?.focus();
        },
        [onChange]
    );

    // Handle pinning/unpinning an entry
    const handleTogglePin = useCallback((event: React.MouseEvent, entryId: string) => {
        event.stopPropagation();
        setHistory((prev) => prev.map((e) => (e.id === entryId ? { ...e, pinned: !e.pinned } : e)));
    }, []);

    // Handle deleting an entry
    const handleDeleteEntry = useCallback((event: React.MouseEvent, entryId: string) => {
        event.stopPropagation();
        setHistory((prev) => prev.filter((e) => e.id !== entryId));
    }, []);

    // Handle keyboard navigation
    const handleKeyDown = useCallback(
        (event: KeyboardEvent<HTMLInputElement>) => {
            if (disabled) return;

            switch (event.key) {
                case 'ArrowDown':
                    event.preventDefault();
                    if (!isOpen && filteredEntries.length > 0) {
                        setIsOpen(true);
                        setHighlightedIndex(0);
                    } else if (isOpen) {
                        setHighlightedIndex((prev) =>
                            prev < filteredEntries.length - 1 ? prev + 1 : prev
                        );
                    }
                    break;
                case 'ArrowUp':
                    event.preventDefault();
                    if (isOpen) {
                        setHighlightedIndex((prev) => (prev > 0 ? prev - 1 : 0));
                    }
                    break;
                case 'Enter':
                    if (isOpen && highlightedIndex >= 0) {
                        event.preventDefault();
                        const entry = filteredEntries[highlightedIndex];
                        if (entry) {
                            handleSelectEntry(entry);
                        }
                    }
                    break;
                case 'Escape':
                    if (isOpen) {
                        event.preventDefault();
                        setIsOpen(false);
                        setHighlightedIndex(-1);
                    }
                    break;
                case 'Tab':
                    setIsOpen(false);
                    setHighlightedIndex(-1);
                    break;
            }
        },
        [disabled, isOpen, filteredEntries, highlightedIndex, handleSelectEntry]
    );

    // Handle focus - open dropdown if there are entries
    const handleFocus = useCallback(() => {
        if (skipFocusRef.current) {
            skipFocusRef.current = false;
            return;
        }
        if (!disabled && sortedEntries.length > 0) {
            setIsOpen(true);
        }
    }, [disabled, sortedEntries.length]);

    // Toggle dropdown visibility
    const handleToggleDropdown = useCallback(() => {
        if (!disabled) {
            setIsOpen((prev) => !prev);
            if (!isOpen) {
                inputRef.current?.focus();
            }
        }
    }, [disabled, isOpen]);

    // Clear current value
    const handleClear = useCallback(() => {
        onChange('');
        inputRef.current?.focus();
    }, [onChange]);

    // Record usage when a task ID is submitted
    const recordUsage = useCallback((taskIdValue: string) => {
        const trimmed = taskIdValue.trim();
        if (trimmed) {
            setHistory((prev) => addOrUpdateTaskId(prev, trimmed));
        }
    }, []);

    // Expose recordUsage via onSubmit callback
    useEffect(() => {
        if (onSubmit) {
            // This is a way to let parent component trigger recording
            // The parent should call onSubmit when the form is submitted
        }
    }, [onSubmit]);

    // Export recordUsage for parent to call
    useEffect(() => {
        // Store recordUsage on the input element for parent access
        if (inputRef.current) {
            (
                inputRef.current as HTMLInputElement & {
                    recordUsage?: (v: string) => void;
                }
            ).recordUsage = recordUsage;
        }
    }, [recordUsage]);

    return (
        <div ref={containerRef} className={cn('relative', className)}>
            <div className="relative flex items-center">
                <Input
                    ref={inputRef}
                    value={value}
                    onChange={handleInputChange}
                    onKeyDown={handleKeyDown}
                    onFocus={handleFocus}
                    placeholder={placeholder}
                    disabled={disabled}
                    className="pr-16"
                />
                <div className="absolute right-1 flex items-center gap-0.5">
                    {value && !disabled && (
                        <button
                            type="button"
                            onClick={handleClear}
                            className="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                            title="Clear"
                        >
                            <X className="h-3.5 w-3.5" />
                        </button>
                    )}
                    {sortedEntries.length > 0 && (
                        <button
                            type="button"
                            onClick={handleToggleDropdown}
                            disabled={disabled}
                            className={cn(
                                'rounded p-1.5 text-muted-foreground transition-colors',
                                disabled
                                    ? 'cursor-not-allowed opacity-50'
                                    : 'hover:bg-muted hover:text-foreground'
                            )}
                            title="Show recent task IDs"
                        >
                            <ChevronDown
                                className={cn(
                                    'h-4 w-4 transition-transform',
                                    isOpen && 'rotate-180'
                                )}
                            />
                        </button>
                    )}
                </div>
            </div>

            {isOpen && filteredEntries.length > 0 && (
                <div
                    ref={dropdownRef}
                    className="absolute left-0 right-0 top-full z-50 mt-1 max-h-64 overflow-auto rounded-md border bg-popover shadow-lg"
                >
                    <div className="p-1">
                        {filteredEntries.map((entry, index) => (
                            <div
                                key={entry.id}
                                onClick={() => handleSelectEntry(entry)}
                                className={cn(
                                    'group flex cursor-pointer items-center justify-between rounded-sm px-2 py-1.5 text-sm transition-colors',
                                    highlightedIndex === index
                                        ? 'bg-accent text-accent-foreground'
                                        : 'hover:bg-muted'
                                )}
                                onMouseEnter={() => setHighlightedIndex(index)}
                            >
                                <div className="flex min-w-0 flex-1 items-center gap-2">
                                    {entry.pinned && (
                                        <Pin className="h-3 w-3 flex-shrink-0 text-primary" />
                                    )}
                                    <span className="truncate">{entry.value}</span>
                                </div>
                                <div className="ml-2 flex flex-shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
                                    <button
                                        type="button"
                                        onClick={(e) => handleTogglePin(e, entry.id)}
                                        className="rounded p-1 text-muted-foreground hover:bg-background hover:text-foreground"
                                        title={entry.pinned ? 'Unpin' : 'Pin to top'}
                                    >
                                        {entry.pinned ? (
                                            <PinOff className="h-3.5 w-3.5" />
                                        ) : (
                                            <Pin className="h-3.5 w-3.5" />
                                        )}
                                    </button>
                                    <button
                                        type="button"
                                        onClick={(e) => handleDeleteEntry(e, entry.id)}
                                        className="rounded p-1 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                                        title="Delete from history"
                                    >
                                        <Trash2 className="h-3.5 w-3.5" />
                                    </button>
                                </div>
                            </div>
                        ))}
                    </div>
                    {sortedEntries.length > filteredEntries.length && (
                        <div className="border-t px-2 py-1.5 text-xs text-muted-foreground">
                            {sortedEntries.length - filteredEntries.length} more hidden by filter
                        </div>
                    )}
                </div>
            )}

            {isOpen && filteredEntries.length === 0 && sortedEntries.length > 0 && (
                <div
                    ref={dropdownRef}
                    className="absolute left-0 right-0 top-full z-50 mt-1 rounded-md border bg-popover p-3 text-center text-sm text-muted-foreground shadow-lg"
                >
                    No matching task IDs
                </div>
            )}
        </div>
    );
}

/**
 * useTaskIdHistory hook provides access to task ID history operations.
 * Used by parent components to record task ID usage when a request is submitted.
 */
export function useTaskIdHistory() {
    const [history, setHistory] = useState<TaskIdEntry[]>(() => loadTaskIdHistory());

    const recordUsage = useCallback((value: string) => {
        const trimmed = value.trim();
        if (trimmed) {
            setHistory((prev) => {
                const updated = addOrUpdateTaskId(prev, trimmed);
                saveTaskIdHistory(updated);
                // Emit event to notify TaskIdSelector components to refresh
                window.dispatchEvent(new CustomEvent(HISTORY_UPDATE_EVENT));
                return updated;
            });
        }
    }, []);

    return { history, recordUsage };
}
