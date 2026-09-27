# Frontend test, build, and Playwright-without-MCP tooling

Use this reference before doing `services/ui/frontend` component-test/build
work, or when no Playwright MCP server is wired in and you need to drive
Playwright directly from Node. For narrow API-only or docs-only checks, skip
this file — `SKILL.md` Steps 6-7 cover the rest.

## Frontend component tooling

The dashboard frontend lives in `services/ui/frontend` (React 19 + Vite +
TypeScript, no UI framework). Use its own toolchain for component-level
evidence before spending a cluster deploy on a browser pass.

Installed and available for test/validation work:

| Tool | Use it for |
|---|---|
| `vitest` | test runner, jsdom environment, `src/test/setup.ts` |
| `@testing-library/react` | render components, query by role/label/test id |
| `@testing-library/user-event` | realistic typing, clicking, select, tab order — prefer over `fireEvent` |
| `@testing-library/jest-dom` | `toBeInTheDocument`, `toHaveTextContent`, `toHaveFocus`, … |
| `vitest-axe` + `axe-core` | assert `toHaveNoViolations()` on rendered trees |
| `tsc -b` | type contract check, runs as part of `npm run build` |

```bash
cd services/ui/frontend
npm test                 # vitest run
npx vitest run src/components/servers   # narrow while iterating
npx tsc -b               # types only, no bundle
npm run build            # tsc -b && vite build -> ../static
```

Rules that keep this evidence honest:

- `vite build` writes into `services/ui/static/`, which the Go service embeds
  as a content-hashed bundle (`assets/index-<hash>.js`) — never hardcode the
  filename, read it from the served `index.html`. A frontend change is not
  deployable until `npm run build` has run and the new hashes are committed.
  `emptyOutDir: true` wipes that directory first, so a build that silently
  drops an expected asset (an entry in `public/`, a route) is easy to miss —
  check the actual files under `services/ui/static/` after building, don't
  assume the previous build's file list still applies.
  `services/ui/static/legacy/` no longer exists (the legacy dashboard was
  removed) — don't expect it back.
- Disable the `color-contrast` axe rule in jsdom (no layout engine) and cover
  contrast in the browser pass instead. Every other rule should stay on.
- jsdom tests cannot prove responsive bounds or real network paths. Keep
  overflow, viewport, and request-URL assertions in the Playwright pass.
- Adding a test-only dependency is fine; check `npm audit --omit=dev` stays at
  zero and note any pre-existing dev-tree advisories rather than silently
  inheriting them.

## Playwright runner setup (no MCP browser server)

There is no Playwright MCP server wired into every session. When it is absent,
drive Playwright directly from Node and still produce the same evidence:

```bash
node -e "console.log(require('<path>/node_modules/playwright/package.json').version)"
ls ~/Library/Caches/ms-playwright/     # which browser builds actually exist
```

The common failure is a Playwright package whose pinned browser revision is not
in the cache ("Executable doesn't exist at .../chromium_headless_shell-<rev>").
Do not conclude browser automation is unavailable. Either run
`npx playwright install chromium`, or launch the cached build explicitly:

```js
import { chromium } from '<path>/node_modules/playwright/index.mjs';
const browser = await chromium.launch({
  executablePath: process.env.HOME +
    '/Library/Caches/ms-playwright/chromium-<rev>/chrome-mac-arm64/' +
    'Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing',
});
```

Script the before/after passes as one file parameterized by surface, so the same
actions, viewports, and assertions run against both deployments (see
`SKILL.md`'s "Before/after phase gate"). Collect per run:
`page.on('console')`, `page.on('pageerror')`, `page.on('response')` filtered
to `/api/` and `/auth/`, screenshots per step, and a
`document.documentElement.scrollWidth` vs `clientWidth` measurement at 390px.

Query by `data-testid` for QA hooks and by role/label for the accessibility
assertions. Generated ids (React `useId()`) are not stable across builds — never
select on them.
