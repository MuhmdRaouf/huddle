/* sw.js — the app-shell service worker (SPEC 7.1). Hand-written, no Workbox. It caches the
 * static shell and nothing else: live answers — the API, the MCP bridge, the SSE stream — are
 * always the network's, and so is every path it does not know. The cache name carries the
 * shell's revision (scripts/build.mjs stamps 887eb1c40883c0021496fc487a18182f504ddd29ea6697a20987dadee38a41f5 with a hash of the shell's bytes), so a new
 * build installs beside the old cache and activate deletes the old one. */
(function () {
  var REV = "887eb1c40883c0021496fc487a18182f504ddd29ea6697a20987dadee38a41f5";
  var CACHE = "huddle-shell-" + REV;
  var SHELL = [
    "/",
    "/index.html",
    "/static/theme-boot.js",
    "/static/app.css",
    "/static/app.js",
    "/manifest.webmanifest",
    "/icons/icon-192.png",
    "/icons/icon-512.png",
    "/icons/icon-maskable-512.png",
  ];

  self.addEventListener("install", function (event) {
    event.waitUntil(
      caches
        .open(CACHE)
        .then(function (cache) {
          return Promise.all(
            SHELL.map(function (path) {
              return fetch(path).then(function (response) {
                if (response.ok) return cache.put(path, response);
              });
            }),
          );
        })
        .then(function () {
          return self.skipWaiting();
        }),
    );
  });

  self.addEventListener("activate", function (event) {
    event.waitUntil(
      caches
        .keys()
        .then(function (names) {
          return Promise.all(
            names
              .filter(function (name) {
                return name.indexOf("huddle-shell-") === 0 && name !== CACHE;
              })
              .map(function (name) {
                return caches.delete(name);
              }),
          );
        })
        .then(function () {
          return self.clients.claim();
        }),
    );
  });

  /** A same-origin path the shell owns: one the page loads as part of itself. */
  function isShell(pathname) {
    for (var i = 0; i < SHELL.length; i++) if (SHELL[i] === pathname) return true;
    return (
      pathname.indexOf("/static/", 0) === 0 ||
      pathname.indexOf("/icons/", 0) === 0 ||
      pathname.indexOf("/fonts/", 0) === 0
    );
  }

  self.addEventListener("fetch", function (event) {
    var request = event.request;
    if (request.method !== "GET") return;
    var url = new URL(request.url);
    if (url.origin !== self.location.origin) return;
    if (url.pathname === "/api" || url.pathname.indexOf("/api/", 0) === 0) return;
    if (url.pathname === "/mcp" || url.pathname.indexOf("/mcp/", 0) === 0) return;
    if (request.mode === "navigate") {
      event.respondWith(
        fetch(request)
          .then(function (response) {
            // only the bare shell page is kept: a sign-in link (?code=) answers a one-time page
            // that must never stand in for the app offline
            if (response.ok && !response.redirected && url.pathname === "/" && url.search === "") {
              caches.open(CACHE).then(function (cache) {
                cache.put("/", response.clone());
              });
            }
            return response;
          })
          .catch(function () {
            return caches.open(CACHE).then(function (cache) {
              return cache.match("/");
            });
          }),
      );
      return;
    }
    if (!isShell(url.pathname)) return;
    event.respondWith(
      caches.open(CACHE).then(function (cache) {
        return cache.match(request).then(function (hit) {
          if (hit) return hit;
          return fetch(request).then(function (response) {
            if (response.ok) cache.put(request, response.clone());
            return response;
          });
        });
      }),
    );
  });

  /** The app's own address of one needs-you item: the session's screen, scrolled to the item.
   *  A session's own entry push names no item, and lands on the session's screen alone. */
  function itemPath(payload) {
    var path = "/#/now/" + encodeURIComponent(payload.session || "");
    return payload.item == null ? path : path + "?item=" + payload.item;
  }

  // The push (SPEC 7.6): the payload carries who, what kind and the tool name, so the
  // notification says no more than that — the command and the prompt text never reach the
  // browser at all. A permission item carries Allow, Deny and Open; the other kinds carry
  // Open. A session's own needs-you entry (kind "session") says Needs you and is tagged by
  // its session, so a re-entry replaces it instead of piling up.
  self.addEventListener("push", function (event) {
    var payload = null;
    try {
      payload = event.data && event.data.json();
    } catch (err) {
      payload = null;
    }
    if (!payload || !payload.kind) return;
    var actions = [{ action: "open", title: "Open" }];
    if (payload.kind === "permission") {
      actions = [
        { action: "allow", title: "Allow" },
        { action: "deny", title: "Deny" },
        { action: "open", title: "Open" },
      ];
    }
    var isSession = payload.kind === "session";
    event.waitUntil(
      self.registration.showNotification(isSession ? "Needs you" : payload.tool || payload.kind, {
        body: payload.session + (payload.machine ? " on " + payload.machine : ""),
        icon: "/icons/icon-192.png",
        badge: "/icons/icon-192.png",
        tag: isSession ? "huddle-session-" + payload.session : "huddle-item-" + payload.item,
        data: payload,
        actions: actions,
      }),
    );
  });

  /** Brings the app up at one path: the window already there when there is one, a new one otherwise. */
  function openAt(path) {
    return self.clients
      .matchAll({ type: "window", includeUncontrolled: true })
      .then(function (list) {
        for (var i = 0; i < list.length; i++) {
          if (list[i].url.indexOf(self.location.origin) === 0 && list[i].navigate) return list[i];
        }
        return null;
      })
      .then(function (client) {
        if (!client) return self.clients.openWindow(path);
        return client
          .focus()
          .then(function () {
            return client.navigate(path);
          })
          .catch(function () {
            return self.clients.openWindow(path);
          });
      });
  }

  // Allow and Open land in the app at the item, where deciding asks for the passkey; Deny
  // decides at once, the device's own cookie carrying it.
  self.addEventListener("notificationclick", function (event) {
    var payload = event.notification.data || {};
    event.notification.close();
    if (event.action === "deny" && payload.item) {
      var path = "/api/needs/" + payload.item + "/decide";
      event.waitUntil(
        fetch(path, {
          method: "POST",
          credentials: "include",
          headers: { "content-type": "application/json", "x-huddle-action": path },
          body: JSON.stringify({ decision: "deny" }),
        }).catch(function () {}),
      );
      return;
    }
    event.waitUntil(openAt(itemPath(payload)));
  });
})();
