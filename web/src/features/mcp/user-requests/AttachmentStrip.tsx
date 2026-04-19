import { Loader2, X } from 'lucide-react';
import type { KeyboardEvent } from 'react';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

import type { ImageErrorCode } from './api';

export type AttachmentKind = 'file' | 'url';

/**
 * ComposeAttachment tracks the upload lifecycle for a single attachment in the
 * compose box. Items with status === 'pending' display a spinner; 'error'
 * items surface the typed error code as a tooltip and a red badge.
 */
export interface ComposeAttachment {
  clientId: string;
  kind: AttachmentKind;
  label: string;
  previewUrl?: string;
  url?: string;
  file?: File;
  status: 'pending' | 'ready' | 'error';
  errorCode?: ImageErrorCode;
  errorMessage?: string;
}

interface Props {
  attachments: ComposeAttachment[];
  onRemove: (clientId: string) => void;
  disabled?: boolean;
}

/**
 * AttachmentStrip renders a horizontally scrolling list of attachment
 * thumbnails below the compose textarea. Each thumbnail exposes a hover /
 * keyboard-accessible delete button.
 */
export function AttachmentStrip({ attachments, onRemove, disabled = false }: Props) {
  if (!attachments.length) {
    return null;
  }
  return (
    <ul className="flex flex-wrap gap-2" aria-label="Attached images">
      {attachments.map((attachment) => (
        <li key={attachment.clientId}>
          <AttachmentTile
            attachment={attachment}
            onRemove={onRemove}
            disabled={disabled}
          />
        </li>
      ))}
    </ul>
  );
}

function AttachmentTile({
  attachment,
  onRemove,
  disabled,
}: {
  attachment: ComposeAttachment;
  onRemove: (clientId: string) => void;
  disabled: boolean;
}) {
  const handleKey = (event: KeyboardEvent<HTMLDivElement>) => {
    if (disabled) return;
    if (event.key === 'Delete' || event.key === 'Backspace') {
      event.preventDefault();
      onRemove(attachment.clientId);
    }
  };

  const src = attachment.previewUrl ?? attachment.url;
  return (
    <div
      tabIndex={0}
      role="group"
      aria-label={`${attachment.kind === 'file' ? 'File' : 'URL'} attachment ${attachment.label}`}
      onKeyDown={handleKey}
      className={cn(
        'group relative h-16 w-16 overflow-hidden rounded-lg border border-border bg-muted/40 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary',
        attachment.status === 'error' && 'border-destructive'
      )}
      title={attachment.errorMessage ?? attachment.label}
    >
      {src ? (
        // eslint-disable-next-line jsx-a11y/img-redundant-alt
        <img src={src} alt={`Attached image ${attachment.label}`} className="h-full w-full object-cover" />
      ) : (
        <div className="flex h-full w-full items-center justify-center text-xs font-medium text-muted-foreground">
          {attachment.kind === 'url' ? 'URL' : 'IMG'}
        </div>
      )}

      {attachment.status === 'pending' && (
        <div className="absolute inset-0 flex items-center justify-center bg-background/60">
          <Loader2 className="h-5 w-5 animate-spin text-primary" />
        </div>
      )}
      {attachment.status === 'error' && (
        <div className="absolute bottom-1 left-1 rounded bg-destructive px-1.5 py-0.5 text-[10px] font-bold text-destructive-foreground">
          !
        </div>
      )}

      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="absolute right-1 top-1 h-6 w-6 rounded-full bg-background/80 opacity-0 shadow-sm transition-opacity group-hover:opacity-100 group-focus-within:opacity-100"
        onClick={() => onRemove(attachment.clientId)}
        aria-label={`Remove attachment ${attachment.label}`}
        disabled={disabled}
      >
        <X className="h-3.5 w-3.5" />
      </Button>
    </div>
  );
}
