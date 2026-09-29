import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const COPY_FEEDBACK_MS = 1200;

export function CopyButton({ text, label }: { text: string; label: string }) {
  const [done, setDone] = useState(false);
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      aria-label={label}
      onClick={() => {
        void navigator.clipboard?.writeText(text);
        setDone(true);
        setTimeout(() => setDone(false), COPY_FEEDBACK_MS);
      }}
    >
      {done ? <Check aria-hidden /> : <Copy aria-hidden />}
    </Button>
  );
}

export function CommandLine({ cmd, copyLabel }: { cmd: string; copyLabel: string }) {
  const mono = "font-[family-name:var(--font-mono)]";
  return (
    <div
      className={cn("flex items-center justify-between rounded-md bg-[var(--surface)] py-1 pr-1 pl-2.5 text-xs", mono)}
    >
      <span className="truncate">{cmd}</span>
      <CopyButton text={cmd} label={copyLabel} />
    </div>
  );
}
