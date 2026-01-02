import { FileQuestion } from 'lucide-react';
import { Link } from 'react-router-dom';

import { Button } from '@/components/ui/button';

export function NotFoundPage() {
    return (
        <div className="mx-auto flex max-w-lg flex-col items-center gap-6 pt-20 text-center">
            <div className="flex h-20 w-20 items-center justify-center rounded-full bg-muted">
                <FileQuestion className="h-10 w-10 text-muted-foreground" />
            </div>
            <div className="space-y-2">
                <h1 className="text-3xl font-bold tracking-tight text-foreground">
                    Page not found
                </h1>
                <p className="text-muted-foreground">
                    We could not locate the page you were looking for. The navigation above lists
                    the available modules.
                </p>
            </div>
            <Button asChild>
                <Link to="/">Back to overview</Link>
            </Button>
        </div>
    );
}
