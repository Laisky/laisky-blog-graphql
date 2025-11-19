import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { sha256 } from 'js-sha256'
import { Activity } from 'lucide-react'

const inspectorScriptModules = import.meta.glob<InspectorModule>(
  '../../../../node_modules/@modelcontextprotocol/inspector-client/dist/assets/index-*.js'
)
const inspectorStyleModules = import.meta.glob(
  '../../../../node_modules/@modelcontextprotocol/inspector-client/dist/assets/index-*.css'
)
const DEFAULT_ENDPOINT_PATH = (import.meta.env.VITE_MCP_ENDPOINT_PATH as string | undefined) || '/mcp'

type InspectorInstance = {
  destroy?: () => void
  setAuthorizationToken?: (token: string) => void
  setEndpointUrl?: (endpoint: string) => void
}

type CreateInspectorFn = (options: { target: HTMLElement; endpointUrl: string }) => Promise<InspectorInstance>

type InspectorModule = {
  createInspector?: CreateInspectorFn
  default?: CreateInspectorFn
}

type SubtleDigest = (algorithm: AlgorithmIdentifier, data: BufferSource) => Promise<ArrayBuffer>

export function InspectorPage() {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const location = useLocation()
  const [error, setError] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [endpointDisplay, setEndpointDisplay] = useState('')

  useEffect(() => {
    let cancelled = false
    let inspector: InspectorInstance | undefined
    const mount = containerRef.current
    const params = new URLSearchParams(location.search)
    const endpointParam = params.get('endpoint')
    const endpointUrl = endpointParam ? endpointParam : new URL(DEFAULT_ENDPOINT_PATH, window.location.origin).toString()
    const authorization = params.get('token') || params.get('authorization') || ''
    setEndpointDisplay(endpointUrl)

  applyInspectorDefaults(params)

    async function loadInspector() {
      if (!mount) {
        setError('Unable to mount MCP Inspector container')
        setIsLoading(false)
        return
      }

      setError(null)
      setIsLoading(true)

      try {
        const cryptoReady = ensureSubtleCryptoDigest()
        if (!cryptoReady.ok) {
          setError(cryptoReady.message ?? 'Secure WebCrypto APIs are required for the MCP Inspector.')
          return
        }

        const scriptLoader = pickFirstLoader(inspectorScriptModules)
        if (!scriptLoader) {
          throw new Error('Inspector script assets not found. Ensure @modelcontextprotocol/inspector-client is installed.')
        }

        await Promise.all(loadAllStyles(inspectorStyleModules))

        const inspectorModule = await scriptLoader()
        const createInspector = inspectorModule.createInspector ?? inspectorModule.default

        if (typeof createInspector !== 'function') {
          throw new Error('createInspector export not found in inspector module')
        }

        if (cancelled) {
          return
        }

        inspector = await createInspector({
          target: mount,
          endpointUrl,
        })

        if (cancelled) {
          inspector?.destroy?.()
          return
        }

        if (authorization) {
          inspector?.setAuthorizationToken?.(authorization)
        }

        inspector?.setEndpointUrl?.(endpointUrl)
      } catch (err) {
        console.error('Failed to bootstrap MCP Inspector:', err)
        if (!cancelled) {
          setError('Failed to load MCP Inspector. Check console for details or open https://inspector.modelcontextprotocol.io manually.')
        }
      } finally {
        if (!cancelled) {
          setIsLoading(false)
        }
      }
    }

    loadInspector()

    return () => {
      cancelled = true
      inspector?.destroy?.()
      if (mount) {
        mount.innerHTML = ''
      }
    }
  }, [location.search])

  return (
    <div className="flex h-full min-h-[calc(100vh-8rem)] flex-col bg-background text-foreground">
      <header className="border-b border-border bg-card/80 px-6 py-3 text-sm text-muted-foreground">
        <div className="flex items-center gap-2">
          <Activity className="h-5 w-5 text-primary" />
          <strong className="block text-base text-foreground">MCP Inspector</strong>
        </div>
        <div className="mt-1">
          Endpoint:&nbsp;
          <code className="break-all text-xs text-foreground/80">{endpointDisplay}</code>
        </div>
        <span className="mt-1 block">
          Use query parameters ?endpoint=&lt;url&gt; and ?token=&lt;value&gt; to override defaults.
        </span>
      </header>
      <div className="relative flex-1 overflow-hidden">
        {error ? (
          <div className="flex h-full items-center justify-center px-6 text-center text-sm text-destructive">
            {error}
          </div>
        ) : (
          <div ref={containerRef} className="h-full w-full" />
        )}
        {isLoading && !error ? (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-background/60 text-sm text-muted-foreground">
            Loading MCP Inspector...
          </div>
        ) : null}
      </div>
    </div>
  )
}

function pickFirstLoader<T>(modules: Record<string, () => Promise<T>>): (() => Promise<T>) | undefined {
  const loaders = Object.values(modules)
  return loaders.length > 0 ? loaders[0] : undefined
}

function loadAllStyles(modules: Record<string, () => Promise<unknown>>): Array<Promise<unknown>> {
  return Object.values(modules).map((loader) => loader())
}

function ensureSubtleCryptoDigest(): { ok: boolean; message?: string } {
  if (typeof window === 'undefined') {
    return { ok: true }
  }

  const globalCrypto = window.crypto as (Crypto & { subtle?: SubtleCrypto }) | undefined
  if (!globalCrypto) {
    return {
      ok: false,
      message: 'window.crypto is not available in this environment. Use a modern browser or enable secure context.',
    }
  }

  if (globalCrypto.subtle) {
    return { ok: true }
  }

  const digest: SubtleDigest = async (algorithm, data) => {
    const name = typeof algorithm === 'string' ? algorithm : algorithm?.name
    if (!name || name.toUpperCase() !== 'SHA-256') {
      throw new Error('SubtleCrypto polyfill only supports SHA-256 digests')
    }

    const input = normalizeBufferSource(data)
    return sha256.arrayBuffer(input)
  }

  const subtlePolyfill: SubtleCrypto = {
    digest,
  } as SubtleCrypto

  try {
    Object.defineProperty(globalCrypto, 'subtle', {
      configurable: true,
      enumerable: false,
      value: subtlePolyfill,
    })
  } catch (error) {
    console.error('Failed to attach SubtleCrypto polyfill:', error)
    return {
      ok: false,
      message:
        'Secure WebCrypto APIs are unavailable on this origin. Access the site via https:// or localhost (secure contexts are required for MCP Inspector).',
    }
  }

  return { ok: true }
}

function normalizeBufferSource(source: BufferSource): Uint8Array {
  if (source instanceof ArrayBuffer) {
    return new Uint8Array(source)
  }

  if (ArrayBuffer.isView(source)) {
    return new Uint8Array(source.buffer, source.byteOffset, source.byteLength)
  }

  throw new Error('Unsupported BufferSource type for SubtleCrypto polyfill')
}

function applyInspectorDefaults(params: URLSearchParams): void {
  if (typeof window === 'undefined') {
    return
  }

  const defaults: Array<{ key: string; value: string; previous?: string[]; queryOverride?: string }> = [
    {
      key: 'lastTransportType',
      value: 'streamable-http',
      previous: ['stdio', ''],
      queryOverride: 'transport',
    },
    {
      key: 'lastSseUrl',
      value: 'https://mcp.laisky.com',
      previous: ['http://localhost:3001/sse', ''],
      queryOverride: 'serverUrl',
    },
    {
      key: 'lastConnectionType',
      value: 'direct',
      previous: ['proxy', ''],
      queryOverride: 'connectionType',
    },
  ]

  for (const { key, value, previous = [], queryOverride } of defaults) {
    try {
      if (queryOverride && params.has(queryOverride)) {
        continue
      }

      const current = window.localStorage.getItem(key)
      const shouldUpdate = current === null || previous.includes(current)

      if (shouldUpdate) {
        window.localStorage.setItem(key, value)
      }
    } catch (error) {
      console.warn('Failed to set MCP Inspector default preference', { key, error })
    }
  }
}
