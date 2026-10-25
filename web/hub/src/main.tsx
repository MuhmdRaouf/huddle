// main.tsx — the browser entry: the API client, the store, the live stream and the toast stack
// over the browser's own IO; the boot question that tells a signed-in browser from a signed-out
// one before any real API call runs; navigation (channel opens and closes follow the hash); the
// composer the shell opens (its button and the C key); and one paint of <App/> per change. Thin
// glue on purpose: everything testable lives in the modules it wires together (same call as
// radar's main).

import { type ToastFn, ToastProvider, useToast } from "@huddle/ui/toast.tsx";
import type { ComponentChildren, ComponentType } from "preact";
import { render } from "preact";
import { createApi } from "./api.ts";
import "./app.css";
import { boot } from "./boot.ts";
import { App, NoChannel } from "./app/App.tsx";
import { Landing } from "./app/Landing.tsx";
import { createAct, createCopy, type HuddleContextValue, HuddleProvider } from "./app/context.tsx";
import { CmdKeys, CmdPalette, HelpDialog } from "./app/Palette.tsx";
import { parseHash, pathOf } from "./app/router.ts";
import { settingsSectionOf } from "./app/SettingsTab.tsx";
import { TotpFirstRun } from "./app/Totp.tsx";
import { ComposeDialog } from "./compose/ComposeDialog.tsx";
import type { ComposeSent } from "./compose/Composer.tsx";
import type { ComposeOptions } from "./compose/drafts.ts";
import { DEV_TOKENS_HASH, Tokens } from "./dev/Tokens.tsx";
import { createLive, type Notifications } from "./live.ts";
import { Home } from "./pages/Home.tsx";
import { Inbox } from "./pages/Inbox.tsx";
import { Knowledge } from "./pages/Knowledge.tsx";
import { Overview } from "./pages/Overview.tsx";
import { Settings } from "./pages/Settings.tsx";
import { Team } from "./pages/Team.tsx";
import { Today } from "./pages/Today.tsx";
import { paintGuard } from "./paint.ts";
import { createPushSwitch, registerServiceWorker, syncAppBadge } from "./pwa.ts";
import { ensureRadar, stopRadar } from "./radar/mount.tsx";
import { createRegistry } from "./registry.ts";
import { readPref, writePref } from "./storage.ts";
import { createStore, type Timers } from "./store.ts";
import { SessionDrawer } from "./team/SessionDrawer.tsx";
import type { ViewId } from "./work/model.ts";
import { TaskDrawer } from "./work/TaskDrawer.tsx";
import { Work } from "./work/Work.tsx";

const queried = document.querySelector<HTMLElement>("#app");
const root: HTMLElement =
  queried ?? document.body.appendChild(Object.assign(document.createElement("div"), { id: "app" }));
root.replaceChildren();

const timers: Timers = {
  after: (fn, ms) => setTimeout(fn, ms),
  cancel: (h) => clearTimeout(h as number),
  interval: (fn, ms) => setInterval(fn, ms),
  stop: (h) => clearInterval(h as number),
};

/** The screen-reader live region the store announces fresh Inbox items through. */
const announcer = (): HTMLElement => {
  let el = document.getElementById("announce");
  if (!el) {
    el = document.createElement("div");
    el.id = "announce";
    el.className = "sr-only";
    el.setAttribute("aria-live", "polite");
    el.setAttribute("aria-atomic", "true");
    document.body.append(el);
  }
  return el;
};

/** The OS's dark preference, the one query the tokens page follows across repaints. */
const darkQuery = window.matchMedia("(prefers-color-scheme: dark)");

let signedOutPainted = false;
let failedCh: string | null = null;
let toastFn: ToastFn = () => {};

const api = createApi(fetch, () => paintSignedOut());

// this device's push subscription: the worker's registration where push exists, and whether the
// page runs as the installed app (iOS pushes only there)
const push = createPushSwitch(
  {
    ...("serviceWorker" in navigator && "PushManager" in window
      ? { serviceWorker: navigator.serviceWorker }
      : {}),
    ...("Notification" in window ? { notification: Notification } : {}),
    standalone:
      window.matchMedia("(display-mode: standalone)").matches ||
      (navigator as Navigator & { standalone?: boolean }).standalone === true,
  },
  api.api,
);
const store = createStore({
  api,
  timers,
  hidden: () => document.hidden,
  announce: (text) => {
    announcer().textContent = text;
  },
});

/** The browser's EventSource behind the stream's slice of it (radar's BrowserStream). */
class BrowserSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  private readonly es: EventSource;

  constructor(url: string) {
    this.es = new EventSource(url);
    this.es.onopen = () => this.onopen?.();
    this.es.onerror = () => this.onerror?.();
    this.es.onmessage = (ev) => this.onmessage?.({ data: ev.data });
  }

  close(): void {
    this.es.close();
  }
}

const live = createLive({
  store,
  EventSource: (url) => new BrowserSource(url),
  fetch: (url) => fetch(url),
  storage: localStorage,
  notify: {
    supported: () => "Notification" in window,
    permission: () => Notification.permission,
    requestPermission: () => Notification.requestPermission(),
    show: (title, body, tag, onClick) => {
      const n = new Notification(title, { body, tag });
      n.onclick = () => {
        window.focus();
        n.close();
        onClick();
      };
    },
  } satisfies Notifications,
  onSignedOut: () => paintSignedOut(),
  toast: (text, o) => toastFn(text, o),
  hidden: () => document.hidden,
  inboxOpen: () => parseHash(location.hash, (k, fb) => readPref(localStorage, k, fb)).dest === "inbox",
  focusInbox: (ch) => {
    window.focus();
    go(ch ? `#/channels/${ch}/inbox` : "#/channels");
  },
});

// the registry of every reported session (the Now list and the Session screen), on its own
// all-channels stream: it runs wherever the address is, not only inside a channel
const registry = createRegistry({
  api,
  EventSource: (url) => new BrowserSource(url),
  fetch: (url) => fetch(url),
  onSignedOut: () => paintSignedOut(),
});

const act = createAct(
  api,
  () => store.getState().ch,
  (text, o) => toastFn(text, o),
);

/** Which credential this browser holds, from the boot's one whoami: the passkey banner an
 *  owner's link session shows reads it, and a paired device's session keeps it hidden. The two
 *  facts beside it (an authenticator enrolment, this device's own passkey) drive the first-run
 *  set-up page and the biometrics offer. */
const who = { kind: "" } as { kind: string; totp?: boolean; devicePasskey?: boolean };

/** The first-run gate: the link that just signed this browser in stands for the machine, so it
 *  may enrol the authenticator before the shell; a "Later" keeps it out of this browser until
 *  Settings opens the same set-up. */
const totpGate = { on: false };
const setTotpGate = (on: boolean): void => {
  totpGate.on = on;
  paint();
};

/** Navigates: the same hash re-routes, another one moves the address bar (app.js go). */
function go(h: string): void {
  if (location.hash === h) {
    void syncChannel();
    paint();
  } else {
    location.hash = h;
  }
}

/** The composer's state, outside the tree the way the rest of the glue is: the shell's New
 *  message button, the C key, and every page's Message and New task button open it. */
const compose: { open: boolean; options: ComposeOptions | undefined } = {
  open: false,
  options: undefined,
};

/** Opens the composer over the open channel, with the prefill the caller names. */
function openCompose(o?: ComposeOptions): void {
  if (!store.getState().ch) return;
  compose.open = true;
  compose.options = o;
  paint();
}

/** Closes the composer; a send closes it through the dialog's own onCancel. */
function closeCompose(): void {
  if (!compose.open) return;
  compose.open = false;
  compose.options = undefined;
  paint();
}

/** The palette's and the shortcuts dialog's state, the same way the composer's is. */
const palette = { open: false };
const help = { open: false };

function openPalette(): void {
  palette.open = true;
  paint();
}

function closePalette(): void {
  if (!palette.open) return;
  palette.open = false;
  paint();
}

function openHelp(): void {
  help.open = true;
  paint();
}

function closeHelp(): void {
  if (!help.open) return;
  help.open = false;
  paint();
}

/** What the store refills once a send landed: the Inbox now, the board for a new task. */
const onComposeSent: ComposeSent = (op) => {
  store.attChanged();
  if (op === "task_create") void store.loadBoard().catch(() => {});
};

/** Opens and closes channels as the hash moves: reset, the channel row and Inbox first, then the
 *  stream and the roster and board (app.js openChannel). A channel that cannot be read leaves
 *  the address on a page that says so. */
async function syncChannel(): Promise<void> {
  const route = parseHash(location.hash, (k, fb) => readPref(localStorage, k, fb));
  for (const [k, v] of Object.entries(route.prefs)) writePref(localStorage, k, v);
  // the session the Now tab names loads before anything channel-shaped runs (or returns early)
  registry.setOpenId(route.tab === "now" ? (route.sub[0] ?? null) : null);
  const ch = route.ch;
  failedCh = null;
  if (ch === store.getState().ch) return;
  closeCompose();
  closePalette();
  closeHelp();
  store.setChannel(ch);
  live.disconnect();
  if (!ch) return;
  try {
    await Promise.all([store.loadInfo(), store.loadAttention()]);
  } catch {
    // no channel row: the address names nothing that exists
    if (ch === store.getState().ch) {
      store.setChannel(null);
      failedCh = ch;
    }
    return;
  }
  live.connect();
  await Promise.all([store.loadSessions(), store.loadBoard().catch(() => {})]);
}

/** Catches the toast stack's push function for the glue that sits outside the tree. */
function ToastBridge(): null {
  toastFn = useToast();
  return null;
}

/** The destination the address bar names, for the pages that read their route (Knowledge's id). */
function routeOf(): ReturnType<typeof parseHash> {
  return parseHash(location.hash, (k, fb) => readPref(localStorage, k, fb));
}

/** One page per destination; the closures read the store fresh on every paint, so the pages stay
 *  pure props-in (main re-renders them on every change). */
const pages: Record<string, ComponentType> = {
  home: () => <Home />,
  overview: () => <Overview onCompose={() => openCompose({ mode: "msg" })} store={store} />,
  today: () => <Today />,
  inbox: () => (
    <Inbox
      state={store.getState()}
      api={api}
      ch={store.getState().ch ?? ""}
      now={Date.now()}
      toast={(text, o) => toastFn(text, o)}
      onCompose={(prefill) => openCompose(prefill)}
      onOpenTask={(id) =>
        go(
          `${location.hash.split("?")[0] || `#/channels/${store.getState().ch}/inbox`}?t=${encodeURIComponent(id)}`,
        )
      }
      onOpenSession={(name) =>
        go(
          `${location.hash.split("?")[0] || `#/channels/${store.getState().ch}/team`}?s=${encodeURIComponent(name)}`,
        )
      }
      store={store}
    />
  ),
  team: () => (
    <Team
      onCompose={(o) => openCompose(o)}
      onOlder={() => {
        void store.olderTimeline().then((n) => {
          if (n) paint();
        });
      }}
    />
  ),
  work: () => {
    const s = store.getState();
    const ch = s.ch ?? "";
    const base = location.hash.split("?")[0] || `#/channels/${ch}/work`;
    return (
      <Work
        ch={ch}
        view={routeOf().sub[0] as ViewId | undefined}
        sub={routeOf().sub}
        repoViews={s.info?.views ?? []}
        repoPath={typeof s.info?.config?.repo === "string" ? s.info.config.repo : undefined}
        board={s.board}
        byId={s.byId}
        sessions={s.sessions}
        attention={s.attention}
        orchestrator={typeof s.info?.config?.orchestrator === "string" ? s.info.config.orchestrator : null}
        now={Date.now()}
        api={api}
        toast={(text, o) => toastFn(text, o)}
        store={localStorage}
        activeTaskId={routeOf().task}
        onOpenTask={(id) => go(`${base}?t=${encodeURIComponent(id)}`)}
        onOpenSession={(name) => go(`#/channels/${ch}/team?s=${encodeURIComponent(name)}`)}
        onCompose={(opts) =>
          openCompose({ mode: "task", ...(opts.phase !== undefined ? { phase: opts.phase } : {}) })
        }
        onNavigate={(v) => go(`#/channels/${ch}/work/${v}`)}
        reloadBoard={() => store.loadBoard().catch(() => {})}
        touchAttention={() => store.attChanged()}
      />
    );
  },
  knowledge: () => {
    const id = routeOf().sub[0];
    return <Knowledge id={id ? Number(id) : undefined} store={store} />;
  },
  settings: () => (
    <Settings
      notify={{ on: () => live.notifyOn(), toggle: () => live.toggleNotify() }}
      push={push}
      store={store}
    />
  ),
};

function paintSignedOut(): void {
  // the pairing link's screen runs before any credential exists, so the boot's own 401s are
  // expected there and never sign the pairing out
  if (location.pathname === "/pair") return;
  if (signedOutPainted) return;
  signedOutPainted = true;
  live.disconnect();
  registry.disconnect();
  stopRadar();
  paint();
}

function page(): ComponentChildren {
  if (signedOutPainted) return <Landing />;
  if (totpGate.on)
    return (
      <TotpFirstRun
        onDone={() => {
          who.totp = true;
          setTotpGate(false);
        }}
        onLater={() => {
          writePref(localStorage, "totp-later", true);
          setTotpGate(false);
        }}
      />
    );
  if (location.hash === DEV_TOKENS_HASH)
    return <Tokens store={localStorage} query={darkQuery} root={document.documentElement} />;
  if (failedCh && parseHash(location.hash, (k, fb) => readPref(localStorage, k, fb)).ch === failedCh)
    return <NoChannel ch={failedCh} />;
  const s = store.getState();
  const base = pathOf(location.hash);
  return (
    <>
      <App
        pages={pages}
        onCompose={(prefill) => openCompose(prefill)}
        onPalette={openPalette}
        onHelp={openHelp}
        loadChannels={() => void store.loadChannels()}
      />
      <CmdPalette
        open={palette.open}
        onClose={closePalette}
        onCompose={(prefill) => openCompose(prefill)}
        onHelp={openHelp}
        notify={{ on: () => live.notifyOn(), toggle: () => live.toggleNotify() }}
        store={store}
      />
      <HelpDialog open={help.open} onClose={closeHelp} />
      <CmdKeys openPalette={openPalette} openHelp={openHelp} store={store} />
      {/* the task drawer rides every destination: the router's ?t=<id> opens it (app.js openTask) */}
      <TaskDrawer
        id={routeOf().task}
        ch={s.ch}
        api={api}
        now={Date.now()}
        board={s.board}
        byId={s.byId}
        sessions={s.sessions}
        views={s.info?.views ?? []}
        toast={(text, o) => toastFn(text, o)}
        copy={createCopy((text, o) => toastFn(text, o))}
        store={localStorage}
        live={store}
        onClose={() => go(base)}
        onOpenTask={(id) => go(`${base}?t=${encodeURIComponent(id)}`)}
        onOpenSession={(name) => go(`#/channels/${s.ch ?? ""}/team?s=${encodeURIComponent(name)}`)}
        reloadBoard={() => store.loadBoard().catch(() => {})}
        touchAttention={() => store.attChanged()}
      />
      {/* the session drawer rides beside it: the router's ?s=<name> opens it, and a task in the
          same address wins (the shell lets the task drawer win over the session drawer) */}
      <SessionDrawer
        name={routeOf().task ? null : routeOf().sess}
        ch={s.ch}
        api={api}
        now={Date.now()}
        sessions={s.sessions}
        board={s.board}
        byId={(id) => s.byId.get(id)}
        timeline={s.timeline}
        replies={s.replies}
        costOf={(n) => (s.extras?.ch === s.ch ? s.extras?.obs?.cost?.[n] : undefined)}
        toast={(text, o) => toastFn(text, o)}
        onSent={onComposeSent}
        onOpenTask={(id) => go(`${base}?t=${encodeURIComponent(id)}`)}
        onClose={() => go(base)}
      />
      <ComposeDialog
        open={compose.open}
        onClose={closeCompose}
        options={compose.options}
        ch={s.ch ?? ""}
        sessions={s.sessions?.sessions ?? []}
        board={s.board}
        byId={(id) => s.byId.get(id)}
        api={api}
        onSent={onComposeSent}
        toast={(text, o) => toastFn(text, o)}
        timeline={s.timeline}
      />
    </>
  );
}

/** Radar's controller (the Radar tab's views and the Settings › Radar section): started on the
 *  first address that needs it, its state changes repainting the shell (the alerts badge, the
 *  range, the views). Later radar addresses answer the same one. */
let radarWired = false;
function ensureRadarTab(): void {
  const route = parseHash(location.hash, (k, fb) => readPref(localStorage, k, fb));
  const section = settingsSectionOf(route.tab === "settings" ? route.sub[0] : undefined);
  if (route.tab !== "radar" && !(route.tab === "settings" && section === "radar")) return;
  const radar = ensureRadar();
  if (radarWired) return;
  radarWired = true;
  radar.subscribe(() => {
    updatedAt = Date.now();
    paint();
  });
}

function paintBody(): void {
  syncBadgeCount();
  ensureRadarTab();
  const value: HuddleContextValue = {
    state: store.getState(),
    registry,
    api,
    who,
    now: Date.now(),
    updatedAt,
    go,
    act,
    toast: (text, o) => toastFn(text, o),
    copy: createCopy((text, o) => toastFn(text, o)),
  };
  render(
    <ToastProvider>
      <ToastBridge />
      <HuddleProvider value={value}>{page()}</HuddleProvider>
    </ToastProvider>,
    root,
  );
}

/** One render on the stack at a time: a radar flush landing mid-render queues one trailing
 *  paint instead of re-entering preact's render, which would append a second whole app. */
const paint = paintGuard(paintBody);

// every store change moves the rail footer's "updated Xs ago"; the 15 s repaint tick does not
let updatedAt = Date.now();

// the PWA (pwa.ts): the app-shell worker on secure contexts, and the launcher badge holding the
// open needs-you count — the same count the Inbox carries, cleared on sign-out; both optional
// browsers features that fail quietly
registerServiceWorker(window);
let badgeCount = -1;
function syncBadgeCount(): void {
  const count = signedOutPainted ? 0 : store.inboxCount() + registry.needs();
  if (count === badgeCount) return;
  badgeCount = count;
  syncAppBadge(navigator, count);
}
store.subscribe(() => {
  updatedAt = Date.now();
  paint();
});
registry.subscribe(() => {
  updatedAt = Date.now();
  paint();
});
window.addEventListener("hashchange", () => {
  void syncChannel();
  paint();
});
setInterval(paint, 15_000); // relative times stay live (app.js's 15 s tick)

/** The shell's own reads: the registry's stream, the address's channel, and the one request
 *  that names who this browser is. */
function startShell(): void {
  registry.connect();
  void syncChannel().finally(paint);
  // the one boot request that names who this browser is; its answer repaints the shell's banner
  // and, for a link session over an un-enrolled hub, opens the first-run authenticator set-up
  void api
    .api("/api/whoami")
    .then((w) => {
      const kind = (w as { kind?: unknown }).kind;
      if (typeof kind === "string" && kind) who.kind = kind;
      const totp = (w as { totp?: unknown }).totp;
      who.totp = totp === true;
      const passkey = (w as { device_passkey?: unknown }).device_passkey;
      who.devicePasskey = passkey === true;
      if (who.kind === "owner" && who.totp === false && !readPref(localStorage, "totp-later", false)) {
        // the gate stands between the link sign-in and the shell: a slow answer arriving after
        // this browser already moved must not pull it back onto the set-up
        if (["", "#/", "#/channels"].includes(location.hash)) totpGate.on = true;
      }
    })
    .catch(() => {})
    .then(() => paint());
}

// the pairing link's screen owns its window before any credential exists (its ceremonies need
// none), so it starts at once; every other address asks first (boot.ts), so a signed-out visit
// paints the landing with no 401 in the console
if (location.pathname === "/pair") startShell();
else boot({ api, start: startShell, signedOut: paintSignedOut });
