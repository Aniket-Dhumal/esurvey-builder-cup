/**
 * Service worker for the farmer app.
 *
 * # What it is for
 *
 * A farmer opens this app standing in a field with no signal. Without a service
 * worker the browser shows its offline page and the survey never starts — the
 * photographs, the queue and the sync engine are all irrelevant if the app
 * cannot load.
 *
 * # What it deliberately does not cache
 *
 * API responses. Reports, farmer records and survey events are scoped to the
 * signed-in officer or farmer, and a cache keyed only by URL would hand one
 * user's records to the next person to open the app on a shared handset. What
 * the app needs offline it keeps in IndexedDB, where it is written on purpose
 * and cleared on sign-out.
 */

const VERSION = "v1";
const SHELL = `esurvey-shell-${VERSION}`;

// The bare minimum to boot. Next.js hashes its build assets, so those are
// picked up at runtime rather than listed here — a stale hardcoded list would
// pin the app to a build that no longer exists.
const SHELL_URLS = ["/", "/my", "/manifest.webmanifest"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(SHELL)
      .then((cache) => cache.addAll(SHELL_URLS))
      // A missing entry must not stop the worker installing; the app still
      // works online, and the next visit will fill the cache.
      .catch(() => undefined)
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((k) => k !== SHELL).map((k) => caches.delete(k))),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;

  const url = new URL(req.url);

  // Never the API. See the note at the top.
  if (url.pathname.startsWith("/api/")) return;
  if (url.origin !== self.location.origin) return;

  // Navigations: network first so a fresh build is picked up, falling back to
  // whatever was cached so the app opens with no signal at all.
  if (req.mode === "navigate") {
    event.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone();
          caches.open(SHELL).then((c) => c.put(req, copy)).catch(() => {});
          return res;
        })
        .catch(async () => {
          const cached = (await caches.match(req)) || (await caches.match("/my"));
          return cached || Response.error();
        }),
    );
    return;
  }

  // Build assets are content-hashed, so a cache hit is always correct and a
  // miss is worth storing.
  event.respondWith(
    caches.match(req).then((hit) => {
      if (hit) return hit;
      return fetch(req)
        .then((res) => {
          if (res.ok && res.type === "basic") {
            const copy = res.clone();
            caches.open(SHELL).then((c) => c.put(req, copy)).catch(() => {});
          }
          return res;
        })
        .catch(() => hit || Response.error());
    }),
  );
});

// The page asks for a sync pass when it comes back online; the worker only
// needs to wake the clients, because the queue and the credentials live there.
self.addEventListener("message", (event) => {
  if (event.data === "sync-now") {
    self.clients.matchAll().then((clients) =>
      clients.forEach((c) => c.postMessage("sync-now")),
    );
  }
});
