// CodeField.tsx — the six-box field an authenticator code is typed into: paste fills from the
// first box, typing auto-advances, backspace steps back, and the sixth digit submits the code
// on its own. The field takes digits only (an app's code is six of them); the recovery-code
// spelling has its own single-line field where it is offered. `autoFocus` hands the first box
// the focus on mount, and `autocomplete` spells what the first box offers: the one-time-code
// hint alone, or the passkey beside it where the browser's conditional mediation can fill it.

import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";

/** How many boxes, and so how long a code: six, the RFC 6238 default every app shows. */
const BOXES = 6;

/** One code field: six boxes over one shared value, submitting itself when the last box fills.
 * `error` clears the field and refocuses the first box, so a refused code is retyped whole. */
export function CodeField({
  onCode,
  busy = false,
  error = "",
  label = "Authenticator code",
  autoFocus = false,
  autocomplete = "one-time-code",
}: {
  onCode: (code: string) => void;
  busy?: boolean | undefined;
  error?: string | undefined;
  label?: string | undefined;
  autoFocus?: boolean | undefined;
  autocomplete?: string | undefined;
}): JSX.Element {
  const [digits, setDigits] = useState<string[]>(() => Array.from({ length: BOXES }, () => ""));
  const boxes = useRef<(HTMLInputElement | null)[]>([]);
  const sent = useRef("");
  const wanted = useRef(-1);
  const code = digits.join("");

  // the page this field is the way into hands it the focus straight away
  useEffect(() => {
    if (autoFocus) boxes.current[0]?.focus();
  }, [autoFocus]);

  // the focus a change asks for lands after the repaint, so a re-render never undoes it
  useEffect(() => {
    if (wanted.current < 0) return;
    boxes.current[wanted.current]?.focus();
    wanted.current = -1;
  }, [digits]);

  // a fresh refusal empties the field and hands the focus back to its first box. The refused
  // code stays marked as sent: the clear and the submit share one commit, and a re-armed field
  // would send the same code twice.
  useEffect(() => {
    if (error === "") return;
    sent.current = digits.join("");
    setDigits(Array.from({ length: BOXES }, () => ""));
    wanted.current = 0;
  }, [error]);

  // the sixth digit submits once; an edit that breaks the code re-arms the submit
  useEffect(() => {
    if (code.length === BOXES) {
      if (code === sent.current) return;
      sent.current = code;
      onCode(code);
      return;
    }
    sent.current = "";
  }, [code, onCode]);

  /** Places what was typed (or pasted mid-field) from box i on; the focus follows after the
   *  repaint, on the box after the text's last, clamped to the final box. A keystroke that is
   *  no digit still rerenders, so the box snaps back to the digit it holds. */
  const fill = (i: number, text: string): void => {
    const clean = text.replace(/[^0-9]/g, "");
    setDigits((before) => {
      const next = [...before];
      for (let j = 0; j < clean.length && i + j < BOXES; j++) next[i + j] = clean[j] ?? "";
      return next;
    });
    if (clean === "") return;
    wanted.current = Math.min(i + clean.length, BOXES - 1);
  };

  const onKey = (i: number, e: KeyboardEvent): void => {
    if (e.key === "Backspace" && digits[i] === "" && i > 0) {
      e.preventDefault();
      setDigits((before) => {
        const next = [...before];
        next[i - 1] = "";
        return next;
      });
      wanted.current = i - 1;
    }
    if (e.key === "ArrowLeft" && i > 0) wanted.current = i - 1;
    if (e.key === "ArrowRight" && i < BOXES - 1) wanted.current = i + 1;
  };

  return (
    <div class="flex flex-col gap-1.5" data-code-field="">
      <div class="flex max-w-[22rem] gap-2" role="group" aria-label={label}>
        {digits.map((d, i) => (
          <input
            key={i}
            ref={(el) => {
              boxes.current[i] = el;
            }}
            class="ui-input ui-mono h-12 min-w-0 text-center disabled:opacity-60"
            type="text"
            inputmode="numeric"
            autocomplete={i === 0 ? autocomplete : "off"}
            pattern="[0-9]*"
            maxlength={BOXES}
            value={d}
            disabled={busy}
            aria-label={`${label}, digit ${i + 1}`}
            data-box={i}
            onInput={(e) => fill(i, e.currentTarget.value)}
            onKeyDown={(e) => onKey(i, e)}
            onPaste={(e) => {
              e.preventDefault();
              const text = e.clipboardData?.getData("text") ?? "";
              // a paste lands whole from the first box, wherever it was dropped
              fill(0, text);
            }}
          />
        ))}
      </div>
      {error ? (
        <p role="alert" class="m-0 text-sm text-[var(--ui-fail-ink)]">
          {error}
        </p>
      ) : null}
    </div>
  );
}
