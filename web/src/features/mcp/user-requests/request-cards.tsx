import { useSortable } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import {
    ChevronDown,
    Copy,
    Edit3,
    GripVertical,
    Package,
    RotateCcw,
    Trash2,
    Undo2,
} from 'lucide-react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { cn } from '@/lib/utils';

import type { UserRequest } from './api';
import { formatDate } from './utils';

interface EmptyStateProps {
    message: string;
    subtle?: boolean;
}

/**
 * EmptyState displays a placeholder message when no items are available.
 */
export function EmptyState({ message, subtle = false }: EmptyStateProps) {
    return (
        <div
            className={cn(
                'rounded-lg border border-dashed px-4 py-6 text-sm text-muted-foreground',
                subtle ? 'bg-muted/50' : 'bg-muted'
            )}
        >
            {message}
        </div>
    );
}

interface PendingRequestCardProps {
    request: UserRequest;
    onDelete: (request: UserRequest) => void | Promise<void>;
    deleting: boolean;
    onPickup: (request: UserRequest) => void;
    onCopy: (request: UserRequest) => void;
    isEditorDisabled: boolean;
}

/**
 * PendingRequestCard displays a single pending request with a delete action.
 */
export function PendingRequestCard({
    request,
    onDelete,
    deleting,
    onPickup,
    onCopy,
    isEditorDisabled,
}: PendingRequestCardProps) {
    const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
        id: request.id,
    });

    const style = {
        transform: CSS.Transform.toString(transform),
        transition,
        zIndex: isDragging ? 50 : undefined,
        opacity: isDragging ? 0.5 : undefined,
    };

    return (
        <Card
            ref={setNodeRef}
            style={style}
            className={cn(
                'border border-primary/30 bg-card shadow-sm min-h-32 max-h-56 flex flex-col relative group',
                isDragging && 'ring-2 ring-primary'
            )}
        >
            <div
                {...attributes}
                {...listeners}
                className="absolute left-1 top-1/2 -translate-y-1/2 cursor-grab active:cursor-grabbing p-1 text-muted-foreground/30 hover:text-muted-foreground group-hover:opacity-100 opacity-0 transition-opacity"
                title="Drag to reorder"
            >
                <GripVertical className="h-4 w-4" />
            </div>
            <CardHeader className="gap-2 flex-1 min-h-0 overflow-hidden flex flex-col pl-8">
                <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground shrink-0">
                    <span>ID: {request.id}</span>
                    <span>Queued: {formatDate(request.created_at)}</span>
                    {request.task_id && <span>Task: {request.task_id}</span>}
                </div>
                <div className="flex-1 min-h-0 overflow-y-auto">
                    <CardTitle className="text-base font-semibold text-foreground whitespace-pre-wrap line-clamp-none">
                        {request.content}
                    </CardTitle>
                </div>
            </CardHeader>
            <CardContent className="flex justify-end gap-2 shrink-0 pt-0">
                <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                        <Button
                            variant="outline"
                            size="sm"
                            disabled={isEditorDisabled}
                            className="gap-1.5"
                            title="Edit options"
                        >
                            <Edit3 className="h-4 w-4" />
                            <ChevronDown className="h-3.5 w-3.5 opacity-70" />
                        </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-48">
                        <DropdownMenuItem
                            onClick={() => onPickup(request)}
                            className="gap-2 cursor-pointer"
                        >
                            <Edit3 className="h-4 w-4" />
                            <span>Edit & Delete</span>
                        </DropdownMenuItem>
                        <DropdownMenuItem
                            onClick={() => onCopy(request)}
                            className="gap-2 cursor-pointer"
                        >
                            <Copy className="h-4 w-4" />
                            <span>Edit</span>
                        </DropdownMenuItem>
                    </DropdownMenuContent>
                </DropdownMenu>
                <Button
                    variant="destructive"
                    size="icon"
                    onClick={() => onDelete(request)}
                    disabled={deleting}
                    title="Delete request"
                >
                    <Trash2 className="h-4 w-4" />
                    <span className="sr-only">{deleting ? 'Deleting…' : 'Delete'}</span>
                </Button>
            </CardContent>
        </Card>
    );
}

interface ConsumedCardProps {
    request: UserRequest;
    onDelete: (request: UserRequest) => void | Promise<void>;
    deleting: boolean;
    onEditInEditor: (request: UserRequest) => void;
    onAddToPending: (request: UserRequest) => void;
    isPicked: boolean;
    isEditorDisabled: boolean;
}

/**
 * ConsumedCard displays a consumed request with options to edit, re-queue, or delete.
 */
export function ConsumedCard({
    request,
    onDelete,
    deleting,
    onEditInEditor,
    onAddToPending,
    isPicked,
    isEditorDisabled,
}: ConsumedCardProps) {
    return (
        <Card className="border border-border/60 bg-card min-h-32 max-h-56 flex flex-col">
            <CardHeader className="gap-2 flex-1 min-h-0 overflow-hidden flex flex-col">
                <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground shrink-0">
                    <span>ID: {request.id}</span>
                    <span>Queued: {formatDate(request.created_at)}</span>
                    {request.consumed_at && (
                        <span>Delivered: {formatDate(request.consumed_at)}</span>
                    )}
                    {request.task_id && <span>Task: {request.task_id}</span>}
                </div>
                <div className="flex-1 min-h-0 overflow-y-auto">
                    <CardTitle className="text-base font-semibold text-foreground whitespace-pre-wrap line-clamp-none">
                        {request.content}
                    </CardTitle>
                </div>
            </CardHeader>
            <CardContent className="flex flex-wrap justify-end gap-2 shrink-0 pt-0">
                {isPicked ? (
                    <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => onEditInEditor(request)}
                        disabled={isEditorDisabled}
                        title="Put back to history"
                        className="gap-1.5"
                    >
                        <Undo2 className="h-4 w-4" />
                        <span>Put Back</span>
                    </Button>
                ) : (
                    <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                            <Button
                                variant="outline"
                                size="sm"
                                disabled={isEditorDisabled}
                                className="gap-1.5"
                                title="Pick up this directive"
                            >
                                <Package className="h-4 w-4" />
                                <ChevronDown className="h-3.5 w-3.5 opacity-70" />
                            </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" className="w-48">
                            <DropdownMenuItem
                                onClick={() => onAddToPending(request)}
                                className="gap-2 cursor-pointer"
                            >
                                <RotateCcw className="h-4 w-4" />
                                <span>Add to Pending</span>
                            </DropdownMenuItem>
                            <DropdownMenuItem
                                onClick={() => onEditInEditor(request)}
                                className="gap-2 cursor-pointer"
                            >
                                <Edit3 className="h-4 w-4" />
                                <span>Edit in Editor</span>
                            </DropdownMenuItem>
                        </DropdownMenuContent>
                    </DropdownMenu>
                )}
                <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => onDelete(request)}
                    disabled={deleting}
                    title="Delete from history"
                    className="h-9 w-9 text-muted-foreground hover:text-destructive"
                >
                    <Trash2 className="h-4 w-4" />
                    <span className="sr-only">{deleting ? 'Deleting…' : 'Delete'}</span>
                </Button>
            </CardContent>
        </Card>
    );
}

interface RequestListSectionProps {
    title: string;
    badge: number;
    emptyMessage: string;
    children: React.ReactNode;
    isEmpty: boolean;
    subtle?: boolean;
}

/**
 * RequestListSection provides a consistent layout for request list sections with a header and badge.
 */
export function RequestListSection({
    title,
    badge,
    emptyMessage,
    children,
    isEmpty,
    subtle = false,
}: RequestListSectionProps) {
    return (
        <div className="space-y-4">
            <header className="flex items-center justify-between">
                <h2 className="text-xl font-semibold text-foreground">{title}</h2>
                <Badge variant="outline">{badge}</Badge>
            </header>
            <div className="space-y-4">
                {isEmpty ? <EmptyState message={emptyMessage} subtle={subtle} /> : children}
            </div>
        </div>
    );
}
