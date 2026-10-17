// ThemePicker.tsx — the Settings theme picker (the hub's PhoneSettings screen): a System · Dark ·
// Light segment beside the heading, and a two-column grid of family cards, each showing its dark
// and light halves with the variant's own tokens. Both groups are radios: the arrows walk, select
// and focus, only the checked option keeps its tab stop.

import type { JSX } from "preact";
import { useRef, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import { currentTheme, FAMILIES, MODES, setTheme, type ThemePref } from "./theme.ts";

/** Where the arrows move a radio group: to the neighbour (wrapping at the ends) or Home/End;
 *  null for any key the group ignores. */
function radioKeyTo(n: number, key: string, at: number): number | null {
  if (key === "ArrowRight" || key === "ArrowDown") return (at + 1) % n;
  if (key === "ArrowLeft" || key === "ArrowUp") return (at + n - 1) % n;
  if (key === "Home") return 0;
  if (key === "End") return n - 1;
  return null;
}

/** One variant's swatch half: its own base, an ink bar, and the primary, success and warning dots. */
function Half({
  variant,
}: {
  variant: { id: string; base: string; content: string; primary: string; success: string; warning: string };
}): JSX.Element {
  return (
    <div class="flex flex-col gap-[5px] p-2.5" data-variant={variant.id} style={{ background: variant.base }}>
      <span class="h-1.5 w-[70%] rounded-[3px]" style={{ background: variant.content }} />
      <span class="flex gap-1">
        <i class="size-3 rounded-full" style={{ background: variant.primary }} />
        <i class="size-3 rounded-full" style={{ background: variant.success }} />
        <i class="size-3 rounded-full" style={{ background: variant.warning }} />
      </span>
    </div>
  );
}

/** The theme picker: the mode segment and the family grid, over the pref kept in this browser. */
export function ThemePicker(): JSX.Element {
  const [pref, setPref] = useState<ThemePref>(() => currentTheme(localStorage));
  const familyBtns = useRef<(HTMLButtonElement | null)[]>([]);
  const modeBtns = useRef<(HTMLButtonElement | null)[]>([]);

  const choose = (p: ThemePref): void => {
    setTheme(localStorage, p, window.matchMedia("(prefers-color-scheme: dark)").matches);
    setPref(p);
  };

  const familyKey = (at: number, key: string): void => {
    const to = radioKeyTo(FAMILIES.length, key, at);
    if (to === null) return;
    const f = FAMILIES[to];
    if (!f) return;
    choose({ family: f.id, mode: pref.mode });
    familyBtns.current[to]?.focus();
  };

  const modeKey = (at: number, key: string): void => {
    const to = radioKeyTo(MODES.length, key, at);
    if (to === null) return;
    const m = MODES[to]?.[0];
    if (m === undefined) return;
    choose({ family: pref.family, mode: m });
    modeBtns.current[to]?.focus();
  };

  return (
    <section class="flex flex-col gap-2.5" aria-labelledby="th-h">
      <div class="mx-1 flex items-center justify-between gap-3">
        <h2 class="muted text-[13px] font-semibold tracking-[0.06em] uppercase" id="th-h">
          Theme
        </h2>
        <div
          class="flex gap-0.5 rounded-[10px] border hairline bg-base-200 p-[3px]"
          role="radiogroup"
          aria-label="Light or dark"
        >
          {MODES.map(([m, label], i) => (
            /* biome-ignore lint/a11y/useSemanticElements: a segment button carries the radio role, as the design draws it */
            <button
              key={m}
              type="button"
              role="radio"
              data-mode={m}
              aria-checked={pref.mode === m}
              tabindex={pref.mode === m ? 0 : -1}
              ref={(b) => {
                modeBtns.current[i] = b;
              }}
              class={`min-h-8 rounded-[7px] border-0 px-2.5 text-[13px] ${
                pref.mode === m
                  ? "bg-neutral font-semibold text-base-content"
                  : "bg-transparent font-medium muted"
              }`}
              onClick={() => choose({ family: pref.family, mode: m })}
              onKeyDown={(e) => modeKey(i, e.key)}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      <div class="grid grid-cols-2 gap-2.5" role="radiogroup" aria-labelledby="th-h">
        {FAMILIES.map((f, i) => {
          const chosen = pref.family === f.id;
          return (
            /* biome-ignore lint/a11y/useSemanticElements: a card carries the radio role, as the design draws it */
            <button
              key={f.id}
              type="button"
              role="radio"
              data-family={f.id}
              aria-checked={chosen}
              tabindex={chosen ? 0 : -1}
              ref={(b) => {
                familyBtns.current[i] = b;
              }}
              class={`overflow-hidden rounded-[14px] border bg-base-100 text-left ${
                chosen ? "border-2 border-primary" : "hairline"
              }`}
              onClick={() => choose({ family: f.id, mode: pref.mode })}
              onKeyDown={(e) => familyKey(i, e.key)}
            >
              <div class="grid h-[62px] grid-cols-2">
                <Half variant={f.dark} />
                <Half variant={f.light} />
              </div>
              <div class="flex items-center justify-between px-3 py-[9px]">
                <span>
                  <span class="block text-sm font-semibold">{f.label}</span>
                  <span class="block text-xs muted">{f.variants}</span>
                </span>
                {chosen && <Icon name="check" class="size-4.5 text-primary" />}
              </div>
            </button>
          );
        })}
      </div>
      <p class="mx-1 text-xs muted">Saved on this device. Each paired device keeps its own theme.</p>
    </section>
  );
}
