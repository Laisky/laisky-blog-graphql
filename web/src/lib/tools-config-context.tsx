/* eslint-disable react-refresh/only-export-components */
import { createContext, useContext, type ReactNode } from 'react';

import { defaultToolsConfig, type ToolsConfig } from '@/lib/runtime-config';

const ToolsConfigContext = createContext<ToolsConfig>(defaultToolsConfig);
const ToolPricesContext = createContext<unknown>(undefined);

interface ToolsConfigProviderProps {
  children: ReactNode;
  config: ToolsConfig;
  prices?: unknown;
}

/**
 * ToolsConfigProvider provides the tools configuration to the component tree.
 * This allows components to conditionally render based on which tools are enabled.
 */
export function ToolsConfigProvider({ children, config, prices }: ToolsConfigProviderProps) {
  return <ToolsConfigContext.Provider value={config}>
    <ToolPricesContext.Provider value={prices}>{children}</ToolPricesContext.Provider>
  </ToolsConfigContext.Provider>;
}

/**
 * useToolsConfig returns the current tools configuration.
 * Use this hook to check if specific tools are enabled before rendering related UI.
 */
export function useToolsConfig(): ToolsConfig {
  return useContext(ToolsConfigContext);
}

/** useToolPrices returns server pricing metadata separately from interface switches. */
export function useToolPrices(): unknown {
  return useContext(ToolPricesContext);
}
