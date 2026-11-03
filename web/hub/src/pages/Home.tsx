// Home.tsx — the Home tab: the count of what waits on the owner and one link row per open need,
// each to the session that asks. Nothing waiting reads "Nothing needs you". The phone's shell
// header carries the title and the chip, so the page draws them on the tablet and the desktop.

import { Chip } from "@huddle/ui/chip.tsx";
import { Empty } from "@huddle/ui/empty.tsx";
import type { JSX } from "preact";
import { useHuddle } from "../app/context.tsx";
import { useLayout } from "../app/layout.ts";
import { Icon } from "../icons.tsx";
import type { NeedItem, RegistrySession } from "../registry.ts";

/** The chip that says how many sessions wait on the owner; nothing while none does. */
export function NeedsChip({ n }: { n: number }): JSX.Element | null {
  return n > 0 ? (
    <Chip tone="warn" dot data-needs-chip="">
      {n} {n === 1 ? "needs" : "need"} you
    </Chip>
  ) : null;
}

/** One open need as a link row: the session's title over what it waits for. */
function NeedRow({ item, session }: { item: NeedItem; session: RegistrySession | undefined }): JSX.Element {
  return (
    <a
      href={`#/home/${encodeURIComponent(item.session)}`}
      data-item=""
      class="flex min-h-[var(--ui-tap)] items-center gap-3 rounded-[var(--ui-radius)] bg-[var(--ui-surface)] px-4 py-2 shadow-[var(--ui-shadow-1)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
    >
      <span class="flex min-w-0 flex-1 flex-col">
        <span class="truncate text-row font-semibold">{session?.title || item.session}</span>
        <span class="truncate text-meta text-[var(--ui-warn-ink)]">{item.summary}</span>
      </span>
      <Icon name="right" class="size-4 text-[var(--ui-muted)]" />
    </a>
  );
}

/** The Home page. */
export function Home(): JSX.Element {
  const { registry } = useHuddle();
  const layout = useLayout();
  const st = registry.getState();
  const open = (st.needs ?? []).filter((n) => n.state === "open");
  const byId = new Map((st.sessions ?? []).map((s) => [s.session_id, s]));
  return (
    <div class="mx-auto flex w-full max-w-lg flex-col gap-4 px-4 py-5" data-home="">
      {layout === "phone" ? null : (
        <div class="flex items-center gap-2" data-lead="">
          <h1 class="flex-1 text-xl font-bold">Home</h1>
          <NeedsChip n={registry.needs()} />
        </div>
      )}
      {open.length === 0 ? (
        <Empty icon={<Icon name="checkc" class="size-5" />}>Nothing needs you</Empty>
      ) : (
        <div class="flex flex-col gap-2" data-list="">
          {open.map((n) => (
            <NeedRow key={n.id} item={n} session={byId.get(n.session)} />
          ))}
        </div>
      )}
    </div>
  );
}
