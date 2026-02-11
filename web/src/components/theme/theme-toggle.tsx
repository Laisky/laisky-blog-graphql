/* eslint-disable react-refresh/only-export-components */
import { Check, Laptop, Moon, Sun, type LucideIcon } from 'lucide-react';

import { useMemo } from 'react';

import { Button, type ButtonProps } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { useTheme, type ThemeSetting } from '@/components/theme/theme-provider';
import { cn } from '@/lib/utils';

/**
 * ThemeToggleOption describes one selectable theme in the toggle menu.
 * Parameters: value is the theme preference to apply, label is the UI text, and Icon is the trigger/menu icon.
 * Returns: The type is used to keep theme menu options strongly typed.
 */
export interface ThemeToggleOption {
  value: ThemeSetting;
  label: string;
  Icon: LucideIcon;
}

/**
 * DEFAULT_THEME_TOGGLE_OPTIONS is the canonical option list used by default.
 * Parameters: This constant does not accept input parameters.
 * Returns: A stable option list containing system/light/dark theme choices.
 */
export const DEFAULT_THEME_TOGGLE_OPTIONS: readonly ThemeToggleOption[] = [
  { value: 'system', label: 'System', Icon: Laptop },
  { value: 'light', label: 'Light', Icon: Sun },
  { value: 'dark', label: 'Dark', Icon: Moon },
];

/**
 * ThemeToggleProps describes the extension points for the reusable ThemeToggle component.
 * Parameters: Each prop controls trigger style, menu position, labels, and custom options for embedding in different layouts.
 * Returns: The interface is used as the public API for teams that consume ThemeToggle.
 */
export interface ThemeToggleProps {
  align?: 'start' | 'center' | 'end';
  sideOffset?: number;
  buttonVariant?: ButtonProps['variant'];
  buttonSize?: ButtonProps['size'];
  buttonClassName?: string;
  menuClassName?: string;
  triggerLabel?: string;
  menuLabel?: string;
  options?: readonly ThemeToggleOption[];
}

/**
 * getThemeOption returns the matching option for a theme preference.
 * Parameters: options is the candidate option list and theme is the current preference value.
 * Returns: The selected option or the first option when no exact match is found.
 */
function getThemeOption(options: readonly ThemeToggleOption[], theme: ThemeSetting): ThemeToggleOption {
  return options.find((option) => option.value === theme) ?? options[0];
}

/**
 * ThemeToggle renders a reusable three-state theme switcher for system/light/dark preferences.
 * Parameters: props customizes trigger style and menu rendering while preserving the same theme behavior.
 * Returns: A JSX element that updates the global theme preference via ThemeProvider.
 */
export function ThemeToggle({
  align = 'end',
  sideOffset = 6,
  buttonVariant = 'ghost',
  buttonSize = 'icon',
  buttonClassName,
  menuClassName,
  triggerLabel = 'Toggle color theme',
  menuLabel = 'Theme',
  options = DEFAULT_THEME_TOGGLE_OPTIONS,
}: ThemeToggleProps) {
  const { theme, setTheme } = useTheme();
  const activeOption = useMemo(() => {
    return getThemeOption(options, theme);
  }, [options, theme]);
  const ActiveIcon = activeOption.Icon;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant={buttonVariant} size={buttonSize} className={cn('h-9 w-9', buttonClassName)}>
          <ActiveIcon className="h-4 w-4" />
          <span className="sr-only">{triggerLabel}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align={align} sideOffset={sideOffset} className={cn('w-44', menuClassName)}>
        <DropdownMenuLabel className="text-xs uppercase tracking-wide text-muted-foreground">{menuLabel}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {options.map(({ value, label, Icon }) => {
          const isActive = value === theme;
          return (
            <DropdownMenuItem
              key={value}
              onSelect={() => setTheme(value)}
              className={cn(
                'cursor-pointer gap-2',
                isActive
                  ? 'bg-muted text-foreground focus:bg-muted focus:text-foreground'
                  : 'text-muted-foreground focus:bg-muted focus:text-foreground'
              )}
              role="menuitemradio"
              aria-checked={isActive}
            >
              <Icon className="h-4 w-4" />
              <span className="flex-1">{label}</span>
              {isActive ? <Check className="h-4 w-4 text-primary" aria-hidden="true" /> : null}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
