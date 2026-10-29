// LinkSheet.tsx — the sheet that ties the picked sessions into a channel (SPEC 8): the owner
// names a new channel or picks an existing one, chooses how much the sessions share, and the
// link posts once — the passkey sheet a 401 opens first, the retry riding it. The channel list
// comes from the read the shell already makes for the switcher; no second one here.

import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { Field } from "@huddle/ui/field.tsx";
import { RadioGroup, type RadioOption } from "@huddle/ui/radio-group.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { channelNameError, LEVEL_LABEL, LEVEL_TEXT, type LinkLevel } from "../links.ts";
import { sessionName } from "../registry.ts";
import { errText, useHuddle } from "./context.tsx";
import { useStepUp } from "./Needs.tsx";

/** The three levels, in the order the picker offers them. */
const LEVELS: readonly LinkLevel[] = ["overhear", "digest", "on-request"];

/** The two ways the sheet names a channel, as the radio group's options. */
const MODES: readonly RadioOption<"new" | "existing">[] = [
  { value: "new", label: "New channel" },
  { value: "existing", label: "Existing channel" },
];

/** The channel half of the sheet: the new/existing radio pair and the field each one shows. */
function ChannelChoice({
  mode,
  name,
  nameError,
  chosen,
  options,
  onMode,
  onName,
  onChosen,
}: {
  mode: "new" | "existing";
  name: string;
  nameError: string | null;
  chosen: string;
  options: readonly string[];
  onMode: (m: "new" | "existing") => void;
  onName: (n: string) => void;
  onChosen: (c: string) => void;
}): JSX.Element {
  return (
    <div class="flex flex-col gap-2">
      <RadioGroup label="Channel mode" name="channel-mode" value={mode} options={MODES} onChange={onMode} />
      {mode === "new" ? (
        <div class="flex flex-col gap-1 pb-1 pl-7">
          <Field label="Channel name" value={name} onInput={onName} placeholder="kanban-release" />
          {nameError ? (
            <p role="alert" class="m-0 text-xs text-[var(--ui-fail-ink)]">
              {nameError}
            </p>
          ) : null}
        </div>
      ) : null}
      {mode === "existing" ? (
        <div class="pb-1 pr-4 pl-7">
          <select
            class="ui-input"
            aria-label="Channel"
            value={chosen}
            onChange={(e) => onChosen(e.currentTarget.value)}
          >
            {options.map((n) => (
              <option key={n} value={n}>
                #{n}
              </option>
            ))}
          </select>
        </div>
      ) : null}
    </div>
  );
}

/** The level half of the sheet: one radio per level, its own line under the picker's eye. */
function LevelChoice({ level, onLevel }: { level: LinkLevel; onLevel: (l: LinkLevel) => void }): JSX.Element {
  return (
    <RadioGroup
      label="How much they share"
      name="link-level"
      value={level}
      options={LEVELS.map((lv) => ({ value: lv, label: LEVEL_LABEL[lv], help: LEVEL_TEXT[lv] }))}
      onChange={onLevel}
    />
  );
}

/** The sheet over the picked sessions: `channel` starts it on an existing one (the Settings
 *  section's link another), `onLinked` runs once the server took the link, before the close —
 *  the Now list drops its picks there. */
export function LinkSheet({
  sessions,
  onClose,
  channel,
  onLinked,
}: {
  sessions: readonly string[];
  onClose: () => void;
  channel?: string | undefined;
  onLinked?: (() => void) | undefined;
}): JSX.Element {
  const { api, toast, registry, state } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [mode, setMode] = useState<"new" | "existing">(channel ? "existing" : "new");
  const [name, setName] = useState("");
  const [chosen, setChosen] = useState(channel ?? "");
  const [level, setLevel] = useState<LinkLevel>("digest");
  const [busy, setBusy] = useState(false);

  // what the sheet calls each picked session: its registry title, or the id it answered by
  const names = sessions.map((id) => {
    const row = registry.getState().sessions?.find((s) => s.session_id === id);
    return row ? sessionName(row) : id;
  });
  const known = state.channels?.map((c) => c.name) ?? [];
  const options = channel && !known.includes(channel) ? [channel, ...known] : known;

  // an untouched field is only empty, not wrong: the rule speaks once the owner typed
  const nameError = name === "" ? null : channelNameError(name);
  const dead = busy || (mode === "new" ? name === "" || nameError !== null : chosen === "");

  const post = (): Promise<unknown> =>
    mode === "new"
      ? api.api("/api/links", { body: { sessions: [...sessions], channel: name, create: true, level } })
      : api.api("/api/links", { body: { sessions: [...sessions], channel: chosen, level } });

  const celebrate = (linked: { channel?: string }): void => {
    toast(`Linked ${sessions.length} sessions into #${linked.channel ?? (mode === "new" ? name : chosen)}`);
    onLinked?.();
    onClose();
  };

  // the tie carries the owner's reach into sessions (SPEC 7.4): a 401 runs the passkey sheet
  // once and posts the same body again
  const send = async (): Promise<void> => {
    setBusy(true);
    try {
      await post();
    } catch (e) {
      await sendRetry(e as ApiError);
      return;
    }
    celebrate({});
  };

  const sendRetry = async (e: ApiError): Promise<void> => {
    if (e.status !== 401 || e.message !== "step-up required") {
      toast(errText(e), { bad: true });
      setBusy(false);
      return;
    }
    if (!(await ask({ action: "Link sessions", session: names.join(", ") }))) {
      setBusy(false);
      return;
    }
    try {
      const linked = (await post()) as { channel?: string };
      celebrate(linked);
    } catch (e2) {
      toast(errText(e2), { bad: true });
      setBusy(false);
    }
  };

  return (
    <Dialog open onClose={onClose} title={`Link ${sessions.length} sessions`}>
      <div class="flex flex-col gap-4" data-link-sheet="">
        {sheet}
        <p class="m-0 text-sm text-[var(--ui-muted)]">
          Linked sessions share one channel. Each one sees what the others are doing, at the level you pick.
        </p>
        <ChannelChoice
          mode={mode}
          name={name}
          nameError={nameError}
          chosen={chosen}
          options={options}
          onMode={setMode}
          onName={setName}
          onChosen={setChosen}
        />
        <LevelChoice level={level} onLevel={setLevel} />
        <Button data-link-submit="" disabled={dead} onClick={() => void send()}>
          Link {sessions.length} sessions
        </Button>
      </div>
    </Dialog>
  );
}
