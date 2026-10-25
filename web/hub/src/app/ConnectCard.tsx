// ConnectCard.tsx — the start-a-session guide every entry shows: the Channels page and Now's empty
// state as a card, the landing page inside a sheet. All hand it the same tabs, one per harness
// (Claude Code first, then Codex and Gemini, then any MCP client over HTTP), each with the two or
// three commands to install and join and a Copy button beside every one; a long command scrolls
// inside its own box. A signed-in caller gets the Invite button that mints a join line; the
// landing (no credential yet) shows the commands instead.

import { CodeBlock } from "@huddle/ui/code.tsx";
import { Card } from "@huddle/ui/card.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { errText, type ToastFn, useHuddle } from "./context.tsx";
import { Icon } from "../icons.tsx";

/** Copies text and says so; a blocked clipboard says that instead (core.js copy). */
async function copyText(text: string, msg: string, toast: ToastFn): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast(msg);
  } catch {
    toast("Could not copy. Select the text and copy it by hand.", { bad: true });
  }
}

/** A command in a box: its label, a Copy button, the code (app.js cxCode). */
function CxCode({ t, c, msg }: { t: string; c: string; msg: string }): JSX.Element {
  const { toast } = useHuddle();
  return (
    <div class="overflow-hidden rounded-[var(--ui-radius)] border border-[var(--ui-line)]">
      <div class="flex items-center gap-2 border-b border-[var(--ui-line)] bg-[var(--ui-surface-2)] px-3 py-1 text-xs">
        <span class="min-w-0 flex-1 truncate font-medium text-[var(--ui-muted)]">{t}</span>
        <button type="button" class="ui-btn ui-btn-ghost" onClick={() => void copyText(c, msg, toast)}>
          <Icon name="copy" class="size-3.5" />
          Copy
          <span class="sr-only"> {t}</span>
        </button>
      </div>
      <CodeBlock code={c} lang="sh" label={t} />
    </div>
  );
}

/** One numbered step of the connect card (app.js cxStep). */
function CxStep({
  n,
  title,
  children,
}: {
  n: number;
  title: string;
  children: ComponentChildren;
}): JSX.Element {
  return (
    <li class="relative flex gap-4 pb-6 last:pb-0">
      <span
        class="relative z-10 inline-flex size-7 shrink-0 items-center justify-center rounded-full border border-[var(--ui-line-strong)] bg-[var(--ui-surface)] text-xs font-semibold tabular-nums text-[var(--ui-muted)]"
        aria-hidden="true"
      >
        {n}
      </span>
      <div class="min-w-0 flex-1 pt-0.5">
        <h3 class="text-[14px] font-semibold">{title}</h3>
        {children}
      </div>
    </li>
  );
}

/** The harnesses the card offers, in tab order: Claude Code first, the MCP escape hatch last. */
const HARNESS_TABS: readonly (readonly [key: string, label: string])[] = [
  ["claude", "Claude Code"],
  ["codex", "Codex"],
  ["gemini", "Gemini"],
  ["mcp", "Other MCP"],
];

/** The three-step Claude Code path: install, invite, paste (app.js connectHTML + wireConnect). */
function ClaudeSteps({ ch, invite }: { ch: string; invite: boolean }): JSX.Element {
  const { api } = useHuddle();
  const [inv, setInv] = useState<{ line: string; until: string } | null>(null);
  const [invErr, setInvErr] = useState("");
  const [busy, setBusy] = useState(false);
  const inviteSession = async (): Promise<void> => {
    setBusy(true);
    try {
      const r = (await api.api("/api/tokens", {
        body: { channel: ch, description: "made in the dashboard" },
      })) as { token: string; expires: string };
      setInv({
        line: `/huddle:join ${location.host} --token ${r.token}`,
        until: new Date(r.expires).toLocaleString(),
      });
      setInvErr("");
    } catch (e) {
      setInvErr(
        (e as ApiError).status === 403
          ? "This browser may not invite (it was signed in by a member). Run /huddle:invite in the Claude session that started Huddle."
          : errText(e),
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <ol class="relative before:absolute before:top-3 before:bottom-3 before:left-3.5 before:w-px before:bg-[var(--ui-line)]">
      <CxStep n={1} title="Install Huddle once per machine">
        <p class="mt-1 mb-2.5 text-sm text-[var(--ui-muted)]">In Claude Code:</p>
        <CxCode
          t="Claude Code"
          c={"/plugin marketplace add MuhmdRaouf/huddle\n/plugin install huddle@muhmdraouf"}
          msg="Copied. Paste it where the step says."
        />
      </CxStep>
      <CxStep n={2} title="Invite the session">
        {invite ? (
          <>
            <p class="mt-1 mb-2.5 text-sm text-[var(--ui-muted)]">
              A join line for <code>{ch}</code>, valid 24 h, good for one project (its later sessions are in
              on their own).
            </p>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
              <button
                type="button"
                class="ui-btn ui-btn-secondary"
                id="cx-invite"
                disabled={busy}
                onClick={() => void inviteSession()}
              >
                <Icon name="plus" class="size-4" />
                Invite a session
              </button>
              <span class="text-xs text-[var(--ui-muted)]" id="cx-inv-note">
                or run <code>/huddle:invite</code> in a Claude session in this huddle
              </span>
            </div>
            <div id="cx-inv" class="mt-3 empty:hidden" aria-live="polite">
              {inv ? (
                <CxCode
                  t={`Join line for ${ch} (valid until ${inv.until})`}
                  c={inv.line}
                  msg="Copied. Paste it into the other Claude session."
                />
              ) : null}
              {invErr ? <p class="m-0 text-sm text-[var(--ui-fail-ink)]">{invErr}</p> : null}
            </div>
          </>
        ) : (
          <>
            <p class="mt-1 mb-2.5 text-sm text-[var(--ui-muted)]">
              In a session that is already in this huddle, run <code>/huddle:invite</code>. It prints the join
              line.
            </p>
            <CxCode t="In a Claude session" c="/huddle:invite" msg="Copied. Paste it where the step says." />
          </>
        )}
      </CxStep>
      <CxStep n={3} title="Paste it into the other session">
        <p class="mt-1 text-sm text-[var(--ui-muted)]">
          It joins this channel and shows its own dashboard link. Do not run <code>/huddle:setup</code> there:
          setup is for the first project.
        </p>
      </CxStep>
    </ol>
  );
}

/** The two-step path of another harness: wire it once, then open it in the project. The word
 *  names the harness as the install command spells it; the name is the word the prose says. */
function HarnessSteps({ name, word }: { name: string; word: string }): JSX.Element {
  return (
    <ol class="relative before:absolute before:top-3 before:bottom-3 before:left-3.5 before:w-px before:bg-[var(--ui-line)]">
      <CxStep n={1} title={`Wire ${name} to this huddle once per machine`}>
        <p class="mt-1 mb-2.5 text-sm text-[var(--ui-muted)]">
          In a terminal of this machine (the huddle CLI ships with the plugin):
        </p>
        <CxCode
          t={`huddle harness install ${word}`}
          c={`huddle harness install ${word}`}
          msg="Copied. Paste it where the step says."
        />
      </CxStep>
      <CxStep n={2} title={`Open ${name} in a project of this huddle`}>
        <p class="mt-1 text-sm text-[var(--ui-muted)]">
          It joins the project's channel on its own, reports its sessions in and answers the same tools.
        </p>
      </CxStep>
    </ol>
  );
}

/** The MCP escape hatch: any client over HTTP, with the credential a session's join keeps. */
function McpSteps({ ch }: { ch: string }): JSX.Element {
  const channel = ch || "<channel>";
  return (
    <div class="flex flex-col gap-3">
      <p class="m-0 text-sm text-[var(--ui-muted)]">
        Any MCP client can call the channel's tools at <code>/mcp/&lt;channel&gt;?as=&lt;session&gt;</code>{" "}
        with a credential in the <code>x-huddle-token</code> header (the one <code>huddle join</code> keeps
        for a session).
      </p>
      <CxCode
        t="claude mcp add"
        c={`claude mcp add --transport http huddle "${location.origin}/mcp/${channel}?as=my-session" --header "x-huddle-token: <credential>"`}
        msg="Copied. Paste it where the step says."
      />
    </div>
  );
}

/** The link to the whole guide, opened in a new tab. */
export function FullGuideLink({
  class: c = "ui-btn ui-btn-ghost shrink-0",
}: {
  class?: string;
}): JSX.Element {
  return (
    <a class={c} href="/connect.md" target="_blank" rel="noopener">
      Full guide
      <Icon name="ext" class="size-3.5" />
      <span class="sr-only">(opens in a new tab)</span>
    </a>
  );
}

/** The guide itself: one tab per harness, the commands each one needs. `ch` names the channel a
 *  signed-in invite mints for; an empty one (the landing page) shows the commands the first
 *  session runs instead of the Invite button. */
export function ConnectGuide({ ch, invite = true }: { ch: string; invite?: boolean }): JSX.Element {
  const [tab, setTab] = useState("claude");
  return (
    <div class="flex min-w-0 flex-col gap-4" data-connect-card="">
      <div role="tablist" aria-label="Harnesses" class="ui-tabs flex-wrap">
        {HARNESS_TABS.map(([key, label]) => (
          <button
            key={key}
            type="button"
            role="tab"
            data-harness={key}
            aria-selected={tab === key ? "true" : "false"}
            onClick={() => setTab(key)}
          >
            {label}
          </button>
        ))}
      </div>
      <div role="tabpanel" data-panel={tab}>
        {tab === "claude" ? (
          <ClaudeSteps ch={ch} invite={invite} />
        ) : tab === "mcp" ? (
          <McpSteps ch={ch} />
        ) : (
          <HarnessSteps word={tab} name={tab === "codex" ? "Codex" : "Gemini"} />
        )}
      </div>
    </div>
  );
}

/** The start-a-session card: the guide in a card with the full guide one link away. */
export function ConnectCard({ ch, invite = true }: { ch: string; invite?: boolean }): JSX.Element {
  return (
    <Card title="Start a session" meta="Bring a coding agent into this huddle." actions={<FullGuideLink />}>
      <ConnectGuide ch={ch} invite={invite} />
    </Card>
  );
}
