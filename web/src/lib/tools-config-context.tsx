import { createContext, useContext, type ReactNode } from 'react'

import { defaultToolsConfig, type ToolsConfig } from '@/lib/runtime-config'

const ToolsConfigContext = createContext<ToolsConfig>(defaultToolsConfig)

interface ToolsConfigProviderProps {
  children: ReactNode
  config: ToolsConfig
}

/**
 * ToolsConfigProvider provides the tools configuration to the component tree.
 * This allows components to conditionally render based on which tools are enabled.
 */
export function ToolsConfigProvider({ children, config }: ToolsConfigProviderProps) {
  return <ToolsConfigContext.Provider value={config}>{children}</ToolsConfigContext.Provider>
}

/**
 * useToolsConfig returns the current tools configuration.
 * Use this hook to check if specific tools are enabled before rendering related UI.
 */
export function useToolsConfig(): ToolsConfig {
  return useContext(ToolsConfigContext)
}
