import { ChevronDown, Trash2 } from 'lucide-react'
import type { ChangeEvent, FormEvent } from 'react'
import { useCallback, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { normalizeApiKey, useApiKey } from '@/lib/api-key-context'
import { cn } from '@/lib/utils'

interface ApiKeyInputProps {
  /** Called when the user submits a key (and the key changes). */
  onConnect?: () => void
  /** Called when the user disconnects. */
  onDisconnect?: () => void
  /** If true, show a Disconnect button when connected. */
  showDisconnect?: boolean
  /** If true, show a Refresh button when connected. */
  showRefresh?: boolean
  /** Called when the user clicks Refresh. */
  onRefresh?: () => void
  className?: string
}

/**
 * A shared API-key input widget with history dropdown.
 * Uses the site-wide ApiKeyContext under the hood.
 */
export function ApiKeyInput({
  onConnect,
  onDisconnect,
  showDisconnect = true,
  showRefresh = false,
  onRefresh,
  className,
}: ApiKeyInputProps) {
  const { apiKey, history, setApiKey, removeFromHistory, disconnect } = useApiKey()
  const [formValue, setFormValue] = useState(apiKey)
  const [isKeyVisible, setIsKeyVisible] = useState(false)
  const [showDropdown, setShowDropdown] = useState(false)
  const containerRef = useRef<HTMLDivElement | null>(null)

  // Sync form when context key changes externally
  useEffect(() => {
    setFormValue(apiKey)
    setIsKeyVisible(false)
  }, [apiKey])

  // Close dropdown on outside click or escape
  useEffect(() => {
    function handleClick(event: MouseEvent) {
      if (!containerRef.current?.contains(event.target as Node)) {
        setShowDropdown(false)
      }
    }
    function handleKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setShowDropdown(false)
      }
    }
    document.addEventListener('click', handleClick)
    document.addEventListener('keydown', handleKey)
    return () => {
      document.removeEventListener('click', handleClick)
      document.removeEventListener('keydown', handleKey)
    }
  }, [])

  const handleSubmit = useCallback(
    (event: FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      const normalised = normalizeApiKey(formValue)
      if (!normalised) {
        return
      }
      setApiKey(normalised)
      setShowDropdown(false)
      onConnect?.()
    },
    [formValue, setApiKey, onConnect],
  )

  const handleDisconnect = useCallback(() => {
    disconnect()
    setFormValue('')
    setIsKeyVisible(false)
    onDisconnect?.()
  }, [disconnect, onDisconnect])

  const handleSelectHistory = useCallback(
    (key: string) => {
      const normalised = normalizeApiKey(key)
      setFormValue(normalised)
      setApiKey(normalised)
      setShowDropdown(false)
      onConnect?.()
    },
    [setApiKey, onConnect],
  )

  const handleDeleteHistory = useCallback(
    (event: React.MouseEvent, key: string) => {
      event.stopPropagation()
      removeFromHistory(key)
    },
    [removeFromHistory],
  )

  const maskedKey = (key: string) => {
    if (key.length <= 8) return key
    return `${key.slice(0, 4)}••••${key.slice(-4)}`
  }

  return (
    <div ref={containerRef} className={cn('relative w-full', className)}>
      <form onSubmit={handleSubmit} className="flex flex-col gap-3 md:flex-row md:items-center">
        <div className="relative w-full md:max-w-md">
          <Input
            value={formValue}
            onChange={(event: ChangeEvent<HTMLInputElement>) => setFormValue(event.target.value)}
            type={isKeyVisible ? 'text' : 'password'}
            placeholder="Enter your API key"
            autoComplete="off"
            className="pr-28"
            required
          />
          <div className="absolute right-1 top-1/2 -translate-y-1/2 flex gap-1">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setIsKeyVisible((prev) => !prev)}
              className="px-2"
              aria-pressed={isKeyVisible}
              aria-label={isKeyVisible ? 'Hide API key' : 'Show API key'}
            >
              {isKeyVisible ? 'Hide' : 'Show'}
            </Button>
            {history.length > 0 && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setShowDropdown((prev) => !prev)}
                className="px-2"
                aria-expanded={showDropdown}
                aria-label="Select from history"
              >
                <ChevronDown className="h-4 w-4" />
              </Button>
            )}
          </div>
        </div>
        <div className="flex gap-2">
          <Button type="submit">Connect</Button>
          {showRefresh && apiKey && (
            <Button type="button" variant="secondary" onClick={onRefresh}>
              Refresh
            </Button>
          )}
          {showDisconnect && apiKey && (
            <Button type="button" variant="outline" onClick={handleDisconnect}>
              Disconnect
            </Button>
          )}
        </div>
      </form>

      {showDropdown && history.length > 0 && (
        <div className="absolute left-0 mt-2 w-full md:max-w-md rounded-md border border-border/60 bg-card shadow-lg z-50">
          <ul className="py-1 text-sm">
            {history.map((key) => (
              <li
                key={key}
                className="flex items-center justify-between px-3 py-2 hover:bg-muted cursor-pointer"
                onClick={() => handleSelectHistory(key)}
              >
                <span className="font-mono truncate">{maskedKey(key)}</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive"
                  onClick={(event) => handleDeleteHistory(event, key)}
                  aria-label="Remove from history"
                >
                  <Trash2 className="h-3 w-3" />
                </Button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
