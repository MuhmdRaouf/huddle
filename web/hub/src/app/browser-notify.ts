// browser-notify.ts — the page's desktop notifications over the Notification constructor: every
// banner carries the full-colour icon beside the body and tag, and a click raises the page,
// closes the banner and hands back to the caller.

import type { Notifications } from "../live.ts";

export function browserNotifications(): Notifications {
  return {
    supported: () => "Notification" in window,
    permission: () => Notification.permission,
    requestPermission: () => Notification.requestPermission(),
    show: (title, body, tag, onClick) => {
      const n = new Notification(title, { body, tag, icon: "/icons/icon-192.png" });
      n.onclick = () => {
        window.focus();
        n.close();
        onClick();
      };
    },
  };
}
