import { BookmarkPlus, Check, ChevronDown, ChevronUp, Edit3, Loader2, Save, Search, Star, Trash2, X } from 'lucide-react'
import type { ChangeEvent, KeyboardEvent } from 'react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { normalizeApiKey, useApiKey } from '@/lib/api-key-context'
import { cn } from '@/lib/utils'

import {
  createSavedCommand,
  deleteSavedCommand,
  listSavedCommands,
  reorderSavedCommands,
  updateSavedCommand,
  type SavedCommand as APISavedCommand,
} from './api'

// Internal representation that normalizes API response fields
export interface SavedCommand {
  id: string
  label: string
  content: string
  sortOrder: number
  createdAt: string
  updatedAt: string
}

/**
 * fuzzyMatch performs a simple fuzzy match between a query and a target string.
 * It checks if all characters in the query appear in the target in order,
 * while also supporting keyword-based matching.
 */
function fuzzyMatch(query: string, target: string): { matches: boolean; score: number } {
  const normalizedQuery = query.toLowerCase().trim()
  const normalizedTarget = target.toLowerCase()

  if (!normalizedQuery) return { matches: true, score: 0 }

  // Exact substring match gets the highest score
  if (normalizedTarget.includes(normalizedQuery)) {
    const position = normalizedTarget.indexOf(normalizedQuery)
    return { matches: true, score: 100 - position * 0.1 }
  }

  // Word-based matching: check if all query words appear in target
  const queryWords = normalizedQuery.split(/\s+/).filter(Boolean)
  const allWordsMatch = queryWords.every((word) => normalizedTarget.includes(word))
  if (allWordsMatch && queryWords.length > 0) {
    return { matches: true, score: 80 - queryWords.length }
  }

  // Character-by-character fuzzy matching
  let queryIdx = 0
  let consecutiveMatches = 0
  let maxConsecutive = 0

  for (let i = 0; i < normalizedTarget.length && queryIdx < normalizedQuery.length; i++) {
    if (normalizedTarget[i] === normalizedQuery[queryIdx]) {
      queryIdx++
      consecutiveMatches++
      maxConsecutive = Math.max(maxConsecutive, consecutiveMatches)
    } else {
      consecutiveMatches = 0
    }
  }

  if (queryIdx === normalizedQuery.length) {
    return { matches: true, score: 50 + maxConsecutive * 5 }
  }

  return { matches: false, score: 0 }
}

/**
 * normalizeCommand converts API response to internal format
 */
function normalizeCommand(cmd: APISavedCommand): SavedCommand {
  return {
    id: cmd.id,
    label: cmd.label,
    content: cmd.content,
    sortOrder: cmd.sort_order,
    createdAt: cmd.created_at,
    updatedAt: cmd.updated_at,
  }
}

interface SavedCommandsProps {
  currentContent: string
  onSelectCommand: (content: string) => void
  onSaveCurrentContent: (label: string) => void
  disabled?: boolean
}

export function SavedCommands({
  currentContent,
  onSelectCommand,
  onSaveCurrentContent,
  disabled = false,
}: SavedCommandsProps) {
  const { apiKey } = useApiKey()
  const [commands, setCommands] = useState<SavedCommand[]>([])
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [isExpanded, setIsExpanded] = useState(true)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editLabel, setEditLabel] = useState('')
  const [editContent, setEditContent] = useState('')
  const [newLabel, setNewLabel] = useState('')
  const [showSaveInput, setShowSaveInput] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [isSaving, setIsSaving] = useState(false)
  const [isDeleting, setIsDeleting] = useState<string | null>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const commandListRef = useRef<HTMLDivElement>(null)

  const hasContent = useMemo(() => currentContent.trim().length > 0, [currentContent])

  // Fetch commands from API when apiKey changes
  useEffect(() => {
    const key = normalizeApiKey(apiKey)
    if (!key) {
      setCommands([])
      setError(null)
      return
    }

    let disposed = false
    const controller = new AbortController()

    async function fetchCommands() {
      setIsLoading(true)
      setError(null)
      try {
        const response = await listSavedCommands(key, controller.signal)
        if (disposed) return
        setCommands((response.commands ?? []).map(normalizeCommand))
      } catch (err) {
        if (disposed || controller.signal.aborted) return
        setError(err instanceof Error ? err.message : 'Failed to load saved commands')
      } finally {
        if (!disposed) {
          setIsLoading(false)
        }
      }
    }

    fetchCommands()

    return () => {
      disposed = true
      controller.abort()
    }
  }, [apiKey])

  // Filter and sort commands based on search query using fuzzy matching
  const filteredCommands = useMemo(() => {
    if (!searchQuery.trim()) return commands

    const scored = commands
      .map((cmd) => {
        const labelMatch = fuzzyMatch(searchQuery, cmd.label)
        const contentMatch = fuzzyMatch(searchQuery, cmd.content)
        const bestScore = Math.max(labelMatch.score, contentMatch.score)
        const matches = labelMatch.matches || contentMatch.matches
        return { command: cmd, score: bestScore, matches }
      })
      .filter((item) => item.matches)
      .sort((a, b) => b.score - a.score)

    return scored.map((item) => item.command)
  }, [commands, searchQuery])

  // Reset selected index when filtered results change
  useEffect(() => {
    setSelectedIndex(0)
  }, [filteredCommands.length])

  const handleSearchKeyDown = useCallback(
    (e: KeyboardEvent<HTMLInputElement>) => {
      if (filteredCommands.length === 0) return

      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault()
          setSelectedIndex((prev) => Math.min(prev + 1, filteredCommands.length - 1))
          break
        case 'ArrowUp':
          e.preventDefault()
          setSelectedIndex((prev) => Math.max(prev - 1, 0))
          break
        case 'Enter':
          e.preventDefault()
          if (filteredCommands[selectedIndex] && !disabled && !editingId) {
            onSelectCommand(filteredCommands[selectedIndex].content)
            setSearchQuery('')
          }
          break
        case 'Escape':
          e.preventDefault()
          setSearchQuery('')
          searchInputRef.current?.blur()
          break
      }
    },
    [filteredCommands, selectedIndex, disabled, editingId, onSelectCommand]
  )

  // Scroll selected item into view
  useEffect(() => {
    if (!commandListRef.current) return
    const selectedElement = commandListRef.current.querySelector(`[data-index="${selectedIndex}"]`)
    if (selectedElement) {
      selectedElement.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
    }
  }, [selectedIndex])

  const handleStartEdit = useCallback((command: SavedCommand) => {
    setEditingId(command.id)
    setEditLabel(command.label)
    setEditContent(command.content)
  }, [])

  const handleCancelEdit = useCallback(() => {
    setEditingId(null)
    setEditLabel('')
    setEditContent('')
  }, [])

  const handleSaveEdit = useCallback(async () => {
    const key = normalizeApiKey(apiKey)
    if (!editingId || !key) return

    setIsSaving(true)
    try {
      const updated = await updateSavedCommand(key, editingId, {
        label: editLabel,
        content: editContent,
      })
      setCommands((prev) =>
        prev.map((cmd) => (cmd.id === editingId ? normalizeCommand(updated) : cmd))
      )
      handleCancelEdit()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update command')
    } finally {
      setIsSaving(false)
    }
  }, [apiKey, editingId, editLabel, editContent, handleCancelEdit])

  const handleSaveNewCommand = useCallback(async () => {
    const key = normalizeApiKey(apiKey)
    if (!hasContent || !key) return

    const label = newLabel.trim() || `Command ${commands.length + 1}`
    setIsSaving(true)
    try {
      const created = await createSavedCommand(key, label, currentContent)
      setCommands((prev) => [normalizeCommand(created), ...prev])
      onSaveCurrentContent(label)
      setNewLabel('')
      setShowSaveInput(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save command')
    } finally {
      setIsSaving(false)
    }
  }, [apiKey, hasContent, newLabel, commands.length, currentContent, onSaveCurrentContent])

  const handleSelectCommand = useCallback(
    (command: SavedCommand) => {
      if (disabled || editingId) return
      onSelectCommand(command.content)
    },
    [disabled, editingId, onSelectCommand]
  )

  const handleToggleSaveInput = useCallback(() => {
    setShowSaveInput((prev) => !prev)
    setNewLabel('')
  }, [])

  const handleRemoveCommand = useCallback(
    async (id: string) => {
      const key = normalizeApiKey(apiKey)
      if (!key) return

      setIsDeleting(id)
      try {
        await deleteSavedCommand(key, id)
        setCommands((prev) => prev.filter((cmd) => cmd.id !== id))
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to delete command')
      } finally {
        setIsDeleting(null)
      }
    },
    [apiKey]
  )

  const handleReorderCommand = useCallback(
    async (id: string, direction: 'up' | 'down') => {
      const key = normalizeApiKey(apiKey)
      if (!key) return

      const index = commands.findIndex((cmd) => cmd.id === id)
      if (index === -1) return

      const newIndex = direction === 'up' ? index - 1 : index + 1
      if (newIndex < 0 || newIndex >= commands.length) return

      // Optimistic update
      const newCommands = [...commands]
      const [removed] = newCommands.splice(index, 1)
      newCommands.splice(newIndex, 0, removed)
      setCommands(newCommands)

      // Persist to server
      try {
        const orderedIds = newCommands.map((cmd) => cmd.id)
        await reorderSavedCommands(key, orderedIds)
      } catch (err) {
        // Revert on error
        setCommands(commands)
        setError(err instanceof Error ? err.message : 'Failed to reorder commands')
      }
    },
    [apiKey, commands]
  )

  const isDisabled = disabled || !apiKey

  return (
    <Card className="border border-border/60 bg-card shadow-sm">
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <button
            type="button"
            onClick={() => setIsExpanded((prev) => !prev)}
            className="flex items-center gap-2 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-md"
          >
            <Star className="h-5 w-5 text-amber-500" />
            <CardTitle className="text-lg text-foreground">Saved Commands</CardTitle>
            <span className="text-sm text-muted-foreground">({commands.length})</span>
            {isLoading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
            {isExpanded ? (
              <ChevronUp className="h-4 w-4 text-muted-foreground" />
            ) : (
              <ChevronDown className="h-4 w-4 text-muted-foreground" />
            )}
          </button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={handleToggleSaveInput}
            disabled={isDisabled || !hasContent || isSaving}
            className={cn('transition-colors', showSaveInput && 'bg-primary/10 border-primary/40')}
          >
            <BookmarkPlus className="mr-2 h-4 w-4" />
            Save current
          </Button>
        </div>
        <p className="text-sm text-muted-foreground">
          Store frequently used directives for quick access. Click any saved command to load it
          into the editor. Commands are synced across all your devices.
        </p>
      </CardHeader>

      {isExpanded && (
        <CardContent className="space-y-4">
          {error && (
            <div className="rounded-lg border border-rose-500/40 bg-rose-500/10 px-4 py-3 text-sm text-rose-700 dark:text-rose-200">
              {error}
              <button
                type="button"
                onClick={() => setError(null)}
                className="ml-2 underline hover:no-underline"
              >
                Dismiss
              </button>
            </div>
          )}

          {showSaveInput && (
            <div className="flex flex-col gap-2 rounded-lg border border-primary/30 bg-primary/5 p-4 animate-in fade-in slide-in-from-top-2 duration-200">
              <p className="text-sm font-medium text-foreground">Save current content as a command</p>
              <div className="flex gap-2">
                <Input
                  value={newLabel}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setNewLabel(e.target.value)}
                  placeholder="Enter a label for this command…"
                  className="flex-1"
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      handleSaveNewCommand()
                    } else if (e.key === 'Escape') {
                      handleToggleSaveInput()
                    }
                  }}
                  disabled={isSaving}
                  autoFocus
                />
                <Button type="button" size="sm" onClick={handleSaveNewCommand} disabled={isSaving}>
                  {isSaving ? (
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  ) : (
                    <Save className="mr-2 h-4 w-4" />
                  )}
                  Save
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={handleToggleSaveInput}
                  disabled={isSaving}
                >
                  <X className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}

          {!apiKey ? (
            <div className="rounded-lg border border-dashed bg-muted/50 px-4 py-6 text-center text-sm text-muted-foreground">
              Connect with your API key to access saved commands.
            </div>
          ) : isLoading && commands.length === 0 ? (
            <div className="rounded-lg border border-dashed bg-muted/50 px-4 py-6 text-center text-sm text-muted-foreground">
              <Loader2 className="mx-auto h-5 w-5 animate-spin mb-2" />
              Loading saved commands…
            </div>
          ) : commands.length === 0 ? (
            <div className="rounded-lg border border-dashed bg-muted/50 px-4 py-6 text-center text-sm text-muted-foreground">
              No saved commands yet. Write a directive above and click &quot;Save current&quot; to
              create your first template.
            </div>
          ) : (
            <div className="space-y-3">
              {/* Fuzzy Search Input */}
              <div className="relative">
                <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground pointer-events-none" />
                <Input
                  ref={searchInputRef}
                  value={searchQuery}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setSearchQuery(e.target.value)}
                  onKeyDown={handleSearchKeyDown}
                  placeholder="Type to search commands… (↑↓ navigate, Enter select)"
                  className="pl-9 pr-8"
                  disabled={isDisabled}
                />
                {searchQuery && (
                  <button
                    type="button"
                    onClick={() => setSearchQuery('')}
                    className="absolute right-2 top-1/2 -translate-y-1/2 p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
                    aria-label="Clear search"
                  >
                    <X className="h-4 w-4" />
                  </button>
                )}
              </div>

              {/* Search results info */}
              {searchQuery && (
                <div className="text-xs text-muted-foreground">
                  {filteredCommands.length === 0 ? (
                    <span>No commands match &quot;{searchQuery}&quot;</span>
                  ) : (
                    <span>
                      Found {filteredCommands.length} command
                      {filteredCommands.length !== 1 ? 's' : ''}
                      {filteredCommands.length > 0 && (
                        <span className="ml-1 text-primary">
                          — press Enter to select &quot;{filteredCommands[selectedIndex]?.label}
                          &quot;
                        </span>
                      )}
                    </span>
                  )}
                </div>
              )}

              {/* Command List */}
              <div ref={commandListRef} className="space-y-2 max-h-96 overflow-y-auto">
                {filteredCommands.map((command, index) => (
                  <SavedCommandItem
                    key={command.id}
                    command={command}
                    isEditing={editingId === command.id}
                    editLabel={editLabel}
                    editContent={editContent}
                    onEditLabelChange={setEditLabel}
                    onEditContentChange={setEditContent}
                    onStartEdit={handleStartEdit}
                    onCancelEdit={handleCancelEdit}
                    onSaveEdit={handleSaveEdit}
                    onSelect={handleSelectCommand}
                    onRemove={handleRemoveCommand}
                    onReorder={handleReorderCommand}
                    canMoveUp={index > 0}
                    canMoveDown={index < filteredCommands.length - 1}
                    disabled={isDisabled}
                    isSaving={isSaving}
                    isDeleting={isDeleting === command.id}
                    isSelected={searchQuery.length > 0 && index === selectedIndex}
                    dataIndex={index}
                  />
                ))}
              </div>
            </div>
          )}
        </CardContent>
      )}
    </Card>
  )
}

interface SavedCommandItemProps {
  command: SavedCommand
  isEditing: boolean
  editLabel: string
  editContent: string
  onEditLabelChange: (value: string) => void
  onEditContentChange: (value: string) => void
  onStartEdit: (command: SavedCommand) => void
  onCancelEdit: () => void
  onSaveEdit: () => void
  onSelect: (command: SavedCommand) => void
  onRemove: (id: string) => void
  onReorder: (id: string, direction: 'up' | 'down') => void
  canMoveUp: boolean
  canMoveDown: boolean
  disabled: boolean
  isSaving: boolean
  isDeleting: boolean
  isSelected?: boolean
  dataIndex?: number
}

function SavedCommandItem({
  command,
  isEditing,
  editLabel,
  editContent,
  onEditLabelChange,
  onEditContentChange,
  onStartEdit,
  onCancelEdit,
  onSaveEdit,
  onSelect,
  onRemove,
  onReorder,
  canMoveUp,
  canMoveDown,
  disabled,
  isSaving,
  isDeleting,
  isSelected = false,
  dataIndex,
}: SavedCommandItemProps) {
  if (isEditing) {
    return (
      <div className="rounded-lg border border-primary/40 bg-primary/5 p-4 space-y-3 animate-in fade-in duration-150">
        <Input
          value={editLabel}
          onChange={(e: ChangeEvent<HTMLInputElement>) => onEditLabelChange(e.target.value)}
          placeholder="Command label"
          className="font-medium"
          disabled={isSaving}
          autoFocus
        />
        <Textarea
          value={editContent}
          onChange={(e: ChangeEvent<HTMLTextAreaElement>) => onEditContentChange(e.target.value)}
          placeholder="Command content"
          rows={3}
          disabled={isSaving}
        />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onCancelEdit} disabled={isSaving}>
            <X className="mr-2 h-4 w-4" />
            Cancel
          </Button>
          <Button type="button" size="sm" onClick={onSaveEdit} disabled={isSaving}>
            {isSaving ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Check className="mr-2 h-4 w-4" />
            )}
            Save changes
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div
      data-index={dataIndex}
      className={cn(
        'group relative flex items-start gap-3 rounded-lg border border-border/60 bg-card p-3 transition-all duration-150',
        !disabled && 'hover:border-primary/40 hover:bg-primary/5 cursor-pointer',
        disabled && 'opacity-60 cursor-not-allowed',
        isSelected && 'border-primary bg-primary/10 ring-2 ring-primary/30',
        isDeleting && 'opacity-50'
      )}
      onClick={() => onSelect(command)}
      role="button"
      tabIndex={disabled ? -1 : 0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect(command)
        }
      }}
    >
      <div className="flex flex-col gap-0.5 pt-0.5">
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation()
            onReorder(command.id, 'up')
          }}
          disabled={!canMoveUp || disabled}
          className={cn(
            'p-0.5 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors',
            (!canMoveUp || disabled) && 'opacity-30 pointer-events-none'
          )}
          aria-label="Move up"
        >
          <ChevronUp className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation()
            onReorder(command.id, 'down')
          }}
          disabled={!canMoveDown || disabled}
          className={cn(
            'p-0.5 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors',
            (!canMoveDown || disabled) && 'opacity-30 pointer-events-none'
          )}
          aria-label="Move down"
        >
          <ChevronDown className="h-3.5 w-3.5" />
        </button>
      </div>

      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <Star className="h-4 w-4 text-amber-500 flex-shrink-0" />
          <span className="font-medium text-foreground truncate">{command.label}</span>
        </div>
        <p className="mt-1 text-sm text-muted-foreground line-clamp-2 whitespace-pre-wrap">
          {command.content}
        </p>
      </div>

      <div
        className={cn(
          'flex items-center gap-1 opacity-0 transition-opacity',
          'group-hover:opacity-100 focus-within:opacity-100'
        )}
      >
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation()
            onStartEdit(command)
          }}
          disabled={disabled}
          className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
          aria-label="Edit command"
        >
          <Edit3 className="h-4 w-4" />
        </button>
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation()
            if (window.confirm(`Delete "${command.label}"?`)) {
              onRemove(command.id)
            }
          }}
          disabled={disabled || isDeleting}
          className="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
          aria-label="Delete command"
        >
          {isDeleting ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Trash2 className="h-4 w-4" />
          )}
        </button>
      </div>
    </div>
  )
}
