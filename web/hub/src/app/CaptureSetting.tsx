// CaptureSetting.tsx — Settings › Hub › Request bodies, the group's own member with the address
// #/settings/capture: the owner's switch over huddle-router's keeping of request and answer
// bodies. On, the router keeps each request and answer for 7 days, redacted and sealed with a key
// in this Mac's keychain; off, it stops new ones within 2 seconds while the request list and the
// costs keep working. The switch reads and writes the settings route; the Kept-now line reads the
// bodies store.

import { List, ListItem } from "@huddle/ui/list.tsx";
import { Switch } from "@huddle/ui/switch.tsx";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { errText, useHuddle } from "./context.tsx";

/** The switch's route. */
const CAPTURE_PATH = "/api/settings";

/** The bodies store's route. */
const BODIES_PATH = "/api/bodies";

/** A section's heading, the same small caps on every section. */
const HEAD = "mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]";

/** Muted 13 px copy: the note under the row, the read-failure line in its place, and the
 *  Kept-now line under the group. */
const SMALL = "m-0 text-[13px] text-[var(--ui-muted)]";

/** Reads the settings route's answer into the switch; a field the answer leaves out reads as off. */
function captureOf(r: unknown): boolean {
  return (r as { capture?: unknown } | null)?.capture === true;
}

/** The kept counts the bodies route answers. */
type Kept = { requests: number; bytes: number };

/** Reads the bodies route's answer: the counts while the store holds bodies, null while capture
 *  is off or the answer says nothing readable. */
function keptOf(r: unknown): Kept | null {
  const o = (r ?? {}) as { state?: unknown; requests?: unknown; bytes?: unknown };
  if (o.state !== "kept" || typeof o.requests !== "number" || typeof o.bytes !== "number") return null;
  return { requests: o.requests, bytes: o.bytes };
}

/** A size as text: megabytes without decimals at and over 1 MB, kilobytes under it. */
function sizeText(bytes: number): string {
  const mb = bytes / 1048576;
  return mb >= 1 ? `${Math.round(mb)} MB` : `${Math.round(bytes / 1024)} KB`;
}

/** This hub's Request bodies section: the switch and the line under the group. The section reads
 *  both routes once on mount, a flip POSTs its one field and toasts from the answer, a refusal
 *  flips the switch back and says the error, and a kept answer turns the line back after a flip. */
export function CaptureSetting(): JSX.Element {
  const { api, toast } = useHuddle();
  // null while the read is in the air or failed: the switch holds still until the hub speaks
  const [capture, setCapture] = useState<boolean | null>(null);
  const [failed, setFailed] = useState(false);
  const [kept, setKept] = useState<Kept | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .api(CAPTURE_PATH)
      .then((r) => {
        if (alive) setCapture(captureOf(r));
      })
      .catch(() => {
        if (alive) setFailed(true);
      });
    api
      .api(BODIES_PATH)
      .then((r) => {
        if (alive) setKept(keptOf(r));
      })
      .catch(() => {
        if (alive) setKept(null);
      });
    return () => {
      alive = false;
    };
  }, [api]);

  const readBodies = (): void => {
    api
      .api(BODIES_PATH)
      .then((r) => setKept(keptOf(r)))
      .catch(() => setKept(null));
  };

  const flip = (): void => {
    if (capture === null) return;
    const next = !capture;
    setCapture(next); // the switch answers the tap at once; the hub's answer decides whether it holds
    api
      .api(CAPTURE_PATH, { method: "POST", body: { capture: next } })
      .then((r) => {
        const held = captureOf(r);
        setCapture(held);
        toast(held ? "Request bodies on." : "Request bodies off. New requests are not kept.");
        readBodies();
      })
      .catch((e: unknown) => {
        setCapture(capture); // the hub did not take the change, so the switch goes back
        toast(errText(e), { bad: true });
      });
  };

  return (
    <section aria-labelledby="capture-h" class="flex flex-col gap-3">
      <h2 id="capture-h" class={HEAD}>
        Request bodies
      </h2>
      <List label="Request bodies">
        <ListItem
          title="Keep request bodies"
          meta={
            <span class={SMALL}>
              {failed
                ? "Could not read the setting."
                : "huddle-router keeps each request and answer for 7 days, redacted and sealed with a key in this Mac's keychain. Off stops new ones within 2 seconds; the request list and costs keep working."}
            </span>
          }
          trailing={
            <Switch
              id="set-capture"
              label="Keep request bodies"
              checked={capture ?? false}
              disabled={capture === null}
              onChange={flip}
            />
          }
        />
      </List>
      {kept !== null ? (
        <p
          class={SMALL}
        >{`Kept now: ${kept.requests.toLocaleString("en-US")} requests · ${sizeText(kept.bytes)}`}</p>
      ) : null}
    </section>
  );
}
