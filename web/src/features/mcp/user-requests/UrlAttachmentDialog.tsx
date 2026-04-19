import type { FormEvent } from 'react';
import { useState } from 'react';

import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/confirm-dialog';
import { Input } from '@/components/ui/input';

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSubmit: (url: string) => void;
}

/**
 * UrlAttachmentDialog is a tiny inline form that collects one image URL and
 * hands it back to the compose box. The compose box performs the actual
 * server submission (so errors surface with the rest of the attachment state).
 */
export function UrlAttachmentDialog({ open, onOpenChange, onSubmit }: Props) {
  const [value, setValue] = useState('');
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) {
      setError('Enter a URL');
      return;
    }
    try {
      const parsed = new URL(trimmed);
      if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
        setError('Only http/https URLs are accepted');
        return;
      }
    } catch {
      setError('Invalid URL');
      return;
    }
    onSubmit(trimmed);
    setValue('');
    setError(null);
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>Attach image from URL</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-3">
          <Input
            autoFocus
            value={value}
            placeholder="https://example.com/image.png"
            onChange={(event) => {
              setValue(event.target.value);
              setError(null);
            }}
          />
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit">Attach</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
