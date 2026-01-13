import { useEffect, useId, useRef, useState } from 'react';
import { Check, Laptop, Moon, Sun, type LucideIcon } from 'lucide-react';

import { Button } from '@/components/ui/button';
import { useTheme, type ThemeSetting } from '@/components/theme/theme-provider';
import { cn } from '@/lib/utils';

type ThemeOption = {
  value: ThemeSetting;
  label: string;
  Icon: LucideIcon;
};

const THEME_OPTIONS: ThemeOption[] = [
  { value: 'light', label: 'Light', Icon: Sun },
  { value: 'dark', label: 'Dark', Icon: Moon },
  { value: 'system', label: 'System', Icon: Laptop },
];

/**
 * ThemeToggle renders a compact theme switcher that exposes the available
 * themes through a dropdown menu. It shows the active theme icon and lets the
 * user pick another ThemeSetting value, updating the theme context.
 */
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const menuId = useId();

  const activeOption = THEME_OPTIONS.find((option) => option.value === theme) ?? THEME_OPTIONS[0];
  const ActiveIcon = activeOption.Icon;

  useEffect(() => {
    if (!open) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      if (!menuRef.current?.contains(target) && !buttonRef.current?.contains(target)) {
        setOpen(false);
      }
    };

    const handleKeydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
        buttonRef.current?.focus();
      }
    };

    window.addEventListener('mousedown', handlePointerDown);
    window.addEventListener('keydown', handleKeydown);

    return () => {
      window.removeEventListener('mousedown', handlePointerDown);
      window.removeEventListener('keydown', handleKeydown);
    };
  }, [open]);

  const handleSelect = (value: ThemeSetting) => {
    setTheme(value);
    setOpen(false);
  };

  return (
    <div className="relative">
      <Button
        ref={buttonRef}
        type="button"
        variant="ghost"
        size="icon"
        className="h-9 w-9"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((prev) => !prev)}
      >
        <ActiveIcon className="h-4 w-4" />
        <span className="sr-only">Toggle color theme</span>
      </Button>
      {open ? (
        <div
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-orientation="vertical"
          className="absolute right-0 z-50 mt-2 w-44 rounded-md border border-border bg-background p-1 shadow-lg"
        >
          {THEME_OPTIONS.map(({ value, label, Icon }) => {
            const isActive = value === theme;
            return (
              <button
                key={value}
                type="button"
                role="menuitemradio"
                aria-checked={isActive}
                onClick={() => handleSelect(value)}
                className={cn(
                  'flex w-full items-center gap-2 rounded-sm px-2 py-2 text-sm transition-colors focus:outline-none',
                  isActive
                    ? 'bg-muted text-foreground'
                    : 'text-muted-foreground hover:bg-muted hover:text-foreground focus:bg-muted focus:text-foreground'
                )}
              >
                <Icon className="h-4 w-4" />
                <span className="flex-1 text-left">{label}</span>
                {isActive ? <Check className="h-4 w-4 text-primary" aria-hidden="true" /> : null}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
