// Network first for the app shell, with the cache only as an offline fallback
// so the app still opens without reception. API calls are never touched.
const CACHE = 'shell-v1';

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (
    e.request.method !== 'GET' ||
    url.origin !== location.origin ||
    url.pathname.startsWith('/api/') ||
    url.pathname === '/login'
  ) {
    return;
  }
  e.respondWith(networkFirst(e.request));
});

async function networkFirst(request) {
  const cache = await caches.open(CACHE);
  const network = fetch(request).then((res) => {
    if (res.ok && !res.redirected) cache.put(request, res.clone());
    return res;
  });
  try {
    // Don't let a weak connection keep the app from opening.
    return await Promise.race([
      network,
      new Promise((_, reject) => setTimeout(reject, 4000)),
    ]);
  } catch {
    return (await cache.match(request)) || network;
  }
}
