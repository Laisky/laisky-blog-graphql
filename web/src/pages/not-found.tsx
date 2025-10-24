import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'

export function NotFoundPage() {
  return (
    <div className="mx-auto flex max-w-lg flex-col items-center gap-4 text-center">
      <h1 className="text-3xl font-semibold text-foreground">Page not found</h1>
      <p className="text-sm text-muted-foreground">
        We could not locate the page you were looking for. The navigation above lists the available
        modules.
      </p>
      <Button asChild>
        <Link to="/">Back to overview</Link>
      </Button>
    </div>
  )
}
