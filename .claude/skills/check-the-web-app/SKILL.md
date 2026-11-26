---
name: check-the-web-app
description: Use when changing the React/TypeScript frontend, or when asked to run the browser, accessibility or Lighthouse checks locally.
---

# Checking the web app

The UI is built with Node (build time only) and embedded in the Go binary. Node 22 or newer is needed.

```sh
cd web
npm ci
npm run typecheck && npm run lint && npm run format:check && npm test
npm run build            # writes internal/webui/dist, which go build embeds
npm run budget           # first-load and total bundle size limits
```

Heavy parts (diff viewer, Markdown, terminal) are loaded only when needed; keep them out of the first-load bundle.

## Browser checks against a running instance

`npm run e2e` (Playwright flows and axe accessibility, light and dark) and `npm run lighthouse` (accessibility, best practices, SEO, performance) need a running instance. CI runs them against the one the installer creates. Locally:

```sh
go build -o /tmp/syx ./cmd/syntaxis
DOCKER_HOST=tcp://127.0.0.1:1 SYNTAXIS_MASTER_KEY=$(/tmp/syx keygen) SYNTAXIS_DATA_DIR=$(mktemp -d) \
  SYNTAXIS_LISTEN=127.0.0.1:28080 SYNTAXIS_ADMIN_LISTEN=127.0.0.1:28081 /tmp/syx serve &
cd web && npx playwright install chromium
# the screen tests sign in: give the admin password of a bootstrap admin (SYNTAXIS_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
# and SYNTAXIS_BOOTSTRAP_ADMIN_PASSWORD when starting the server) or the installer's setup token (E2E_SETUP_TOKEN)
E2E_ADMIN_PASSWORD='the bootstrap password' BASE_URL=http://127.0.0.1:28080 ADMIN_URL=http://127.0.0.1:28081 npm run e2e
CHROME_PATH=$(node -e "console.log(require('@playwright/test').chromium.executablePath())") BASE_URL=http://127.0.0.1:28080/ npm run lighthouse
```

The unreachable `DOCKER_HOST` matters on a machine that already runs Syntaxis: a second server would otherwise tidy away the first one's sandboxes.

Serious or critical accessibility problems fail the build. Interactive controls need names, form fields labels, and colours must pass contrast in both themes.
