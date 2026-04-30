import type { ComponentPropsWithoutRef, ReactNode } from 'react';

import { cn } from '@/lib/utils';

export const LAISKY_ABOUT_URL = 'https://blog.laisky.com/about/me/';

interface LaiskyLinkProps extends Omit<ComponentPropsWithoutRef<'a'>, 'children' | 'href'> {
  children?: ReactNode;
}

/** LaiskyLink accepts standard anchor props and returns the canonical external link for visible Laisky branding text. */
export function LaiskyLink({ children = 'Laisky', className, ...props }: LaiskyLinkProps) {
  return (
    <a
      href={LAISKY_ABOUT_URL}
      target="_blank"
      rel="noopener noreferrer"
      className={cn('transition-colors hover:text-primary', className)}
      {...props}
    >
      {children}
    </a>
  );
}
