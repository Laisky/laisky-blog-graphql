import { Pause, Play } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

interface HoldButtonProps {
  /** Whether hold is currently active */
  isActive: boolean;
  /** Whether an agent is currently waiting for a command */
  isWaiting: boolean;
  /** Remaining seconds until hold expires (only relevant when isWaiting=true) */
  remainingSecs: number;
  /** Called when user clicks to activate hold */
  onActivate: () => Promise<void>;
  /** Called when user clicks to release hold */
  onRelease: () => Promise<void>;
  /** Whether the button should be disabled */
  disabled?: boolean;
  /** Additional CSS classes */
  className?: string;
}

/**
 * HoldButton displays a button that activates or releases the hold state.
 * Hold has two phases:
 * 1. Active but no agent waiting: shows "Holding..." (indefinite)
 * 2. Agent connected and waiting: shows countdown timer (20s max)
 */
export function HoldButton({
  isActive,
  isWaiting,
  remainingSecs,
  onActivate,
  onRelease,
  disabled,
  className,
}: HoldButtonProps) {
  const [isLoading, setIsLoading] = useState(false);
  const [localRemaining, setLocalRemaining] = useState(remainingSecs);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Sync local remaining with prop when it changes significantly
  useEffect(() => {
    if (Math.abs(localRemaining - remainingSecs) > 1) {
      setLocalRemaining(remainingSecs);
    }
  }, [remainingSecs, localRemaining]);

  // Local countdown timer for smooth countdown display (only when agent is waiting)
  useEffect(() => {
    if (!isActive || !isWaiting) {
      setLocalRemaining(0);
      if (timerRef.current) {
        clearInterval(timerRef.current);
        timerRef.current = null;
      }
      return;
    }

    setLocalRemaining(remainingSecs);

    timerRef.current = setInterval(() => {
      setLocalRemaining((prev) => {
        const next = prev - 1;
        return next < 0 ? 0 : next;
      });
    }, 1000);

    return () => {
      if (timerRef.current) {
        clearInterval(timerRef.current);
        timerRef.current = null;
      }
    };
  }, [isActive, isWaiting, remainingSecs]);

  const handleClick = useCallback(async () => {
    setIsLoading(true);
    try {
      if (isActive) {
        await onRelease();
      } else {
        await onActivate();
      }
    } finally {
      setIsLoading(false);
    }
  }, [isActive, onActivate, onRelease]);

  // Determine button label based on state
  const getButtonLabel = () => {
    if (isLoading) {
      return isActive ? "Releasing…" : "Activating…";
    }
    if (!isActive) {
      return "Hold";
    }
    if (isWaiting) {
      return `Release (${Math.ceil(localRemaining)}s)`;
    }
    return "Holding…";
  };

  return (
    <Button
      type="button"
      variant={isActive ? "default" : "outline"}
      onClick={handleClick}
      disabled={disabled || isLoading}
      className={cn(
        isActive && "bg-amber-600 hover:bg-amber-700 text-white",
        className
      )}
    >
      {isActive ? (
        <>
          <Play className="mr-2 h-4 w-4" />
          {getButtonLabel()}
        </>
      ) : (
        <>
          <Pause className="mr-2 h-4 w-4" />
          {getButtonLabel()}
        </>
      )}
    </Button>
  );
}
