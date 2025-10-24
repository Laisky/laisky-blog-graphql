import { useTheme, type ThemeSetting } from '@/components/theme/theme-provider'
import { cn } from '@/lib/utils'

const THEME_LABELS: Record<ThemeSetting, string> = {
  light: 'Light',
  dark: 'Dark',
  system: 'System',
}

export function ThemeToggle() {
  const { theme, setTheme } = useTheme()

  return (
    <label className="flex items-center gap-2 text-sm text-muted-foreground">
      <span className="hidden text-xs font-medium uppercase tracking-wide md:inline">Theme</span>
      <select
        value={theme}
        onChange={(event) => setTheme(event.target.value as ThemeSetting)}
        className={cn(
          'rounded-md border border-input bg-background px-3 py-2 text-xs font-medium uppercase tracking-wide text-muted-foreground shadow-sm transition-colors',
          'hover:bg-muted focus:outline-none focus:ring-2 focus:ring-ring'
        )}
        aria-label="Select color theme"
      >
        {(Object.keys(THEME_LABELS) as ThemeSetting[]).map((value) => (
          <option key={value} value={value}>
            {THEME_LABELS[value]}
          </option>
        ))}
      </select>
    </label>
  )
}
