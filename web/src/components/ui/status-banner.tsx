import { cn } from '@/lib/utils';

/**
 * StatusState describes the visual appearance and message content of the banner.
 */
export interface StatusState {
  message: string;
  tone: 'info' | 'success' | 'error';
}

interface StatusBannerProps {
  /** The current status to display. */
  status: StatusState;
  /** Optional additional information to display below the main message (e.g., masked API key suffix). */
  subtext?: string;
  /** Whether to show the subtext only on success tone. Defaults to true. */
  subtextOnSuccessOnly?: boolean;
  /** Additional CSS classes to apply. */
  className?: string;
}

/**
 * StatusBanner displays a styled notification banner with tone-based coloring.
 * Use this component to provide feedback to users about the status of an operation.
 */
export function StatusBanner({ status, subtext, subtextOnSuccessOnly = true, className }: StatusBannerProps) {
  const toneStyles = {
    info: 'border-border bg-muted text-muted-foreground',
    success: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-200 dark:border-emerald-500/40',
    error: 'border-rose-500/40 bg-rose-500/10 text-rose-700 dark:text-rose-200 dark:border-rose-500/40',
  } as const;

  const showSubtext = subtext && (!subtextOnSuccessOnly || status.tone === 'success');

  return (
    <div className={cn('flex flex-col gap-1 rounded-lg border px-4 py-3 text-sm transition-colors', toneStyles[status.tone], className)}>
      <span>{status.message}</span>
      {showSubtext && <span className="text-xs text-inherit/80">{subtext}</span>}
    </div>
  );
}
