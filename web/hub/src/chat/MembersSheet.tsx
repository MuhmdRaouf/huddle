// MembersSheet.tsx — the channel's members on a sheet: each live session with its state, the
// files it claimed (its reservations) and the owner's Remove, which arms an in-sheet confirm row
// first; a confirmed remove takes the session and its subagents out of the channel's live views.

import { Button } from "@huddle/ui/button.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { Avatar } from "../kit.tsx";
import type { ReservationItem } from "../pages/Overview.tsx";
import type { RosterSession } from "../store.ts";

/** What the sheet lists and does: the live members, their claims, and the remove the owner runs
 *  (true once it landed). */
export type MembersSheetProps = {
  open: boolean;
  onClose: () => void;
  members: readonly RosterSession[];
  claims: readonly ReservationItem[];
  onRemove: (name: string) => Promise<boolean>;
};

/** The last part of a claimed path: the file's own name. */
const fileOf = (p: string): string => p.split("/").filter(Boolean).at(-1) ?? p;

/** One member's row: who, its state, its claims, and Remove or the confirm row it arms. */
function MemberRow({
  member,
  claims,
  onRemove,
}: {
  member: RosterSession;
  claims: readonly ReservationItem[];
  onRemove: (name: string) => Promise<boolean>;
}): JSX.Element {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const name = member.name;
  const remove = async (): Promise<void> => {
    setBusy(true);
    const ok = await onRemove(name);
    setBusy(false);
    if (!ok) setConfirming(false);
  };
  return (
    <li data-member={name} class="flex flex-col gap-2 border-b border-[var(--ui-line)] py-3 last:border-b-0">
      <div class="flex min-w-0 items-center gap-3">
        <Avatar name={name} small={true} />
        <div class="flex min-w-0 flex-1 flex-col">
          <span class="truncate text-[length:var(--ui-text-md)] font-medium">{name}</span>
          <span class="truncate text-xs text-[var(--ui-muted)]">
            {[member.role, member.state].filter(Boolean).join(" · ")}
          </span>
        </div>
        {confirming ? null : (
          <Button variant="ghost" label={`Remove ${name}`} onClick={() => setConfirming(true)}>
            Remove
          </Button>
        )}
      </div>
      {claims.length > 0 ? (
        <ul class="m-0 flex list-none flex-col gap-1 p-0 pl-9" aria-label={`Files ${name} claimed`}>
          {claims.map((c) => (
            <li
              key={c.path}
              data-claim=""
              class="flex min-w-0 items-center gap-2 text-xs text-[var(--ui-muted)]"
            >
              <code class="truncate text-[var(--ui-text)]" title={c.path}>
                {fileOf(c.path)}
              </code>
              {c.note ? <span class="truncate">{c.note}</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
      {confirming ? (
        <div data-member-confirm="" class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium">Remove {name} from this channel?</span>
          <Button variant="danger" disabled={busy} onClick={() => void remove()}>
            Remove
          </Button>
          <Button variant="ghost" disabled={busy} onClick={() => setConfirming(false)}>
            Cancel
          </Button>
        </div>
      ) : null}
    </li>
  );
}

/** The sheet. */
export function MembersSheet({ open, onClose, members, claims, onRemove }: MembersSheetProps): JSX.Element {
  return (
    <Sheet open={open} onClose={onClose} title="Members">
      {members.length > 0 ? (
        <ul class="m-0 flex list-none flex-col p-0" aria-label="Members">
          {members.map((m) => (
            <MemberRow
              key={m.name}
              member={m}
              claims={claims.filter((c) => c.holder === m.name)}
              onRemove={onRemove}
            />
          ))}
        </ul>
      ) : (
        <p class="m-0 text-sm text-[var(--ui-muted)]">No session is in this channel.</p>
      )}
    </Sheet>
  );
}
