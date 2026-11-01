// Shared source for the /llms.txt and /llms-full.txt endpoints.
//
// These are rendered as ordinary Astro static endpoints (src/pages/*.txt.ts),
// so the files exist under `astro dev` AND `astro build` alike - unlike a
// build-only integration whose astro:build:done hook never runs in the dev
// server and 404s there. Every doc page's raw Markdown is read at build time
// via import.meta.glob so both endpoints render from one place.

export const LLMS_TITLE = "tamesu";
export const LLMS_DESCRIPTION =
  "tamesu is an agentic testing framework and CLI written in Go. Tests are YAML suites (or Go) with " +
  "targets of type browser, rest, livekit (realtime voice over WebRTC) and sip (phone calls through a " +
  "LiveKit SIP service), personas, scenarios and assertions (judge, contains, regex, json_path, " +
  "tool_called, metric, ...). Every model call goes through Genkit and every secret through mamori. " +
  "The CLI has init, validate, list, run, doctor and version; the rest and livekit (text mode) targets run today.";
// No absolute URL here: every link in these files is built with absoluteUrl
// from Astro's own `site` and `base`, and a domain written a second time here
// is one that goes stale the day the site moves.
export const LLMS_NOTES = "Auto-generated from the tamesu documentation.";

// Every doc page, raw (frontmatter included), keyed by project-absolute path.
const rawPages = import.meta.glob("/src/pages/docs/**/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

export interface DocPage {
  /** Route-relative path, e.g. "/docs/attach". */
  path: string;
  title: string;
  /** Markdown body with the frontmatter block stripped. */
  body: string;
}

const FRONTMATTER = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/;

function routeFor(fileKey: string): string {
  let p = fileKey.replace(/^\/src\/pages/, "").replace(/\.md$/, "");
  if (p.endsWith("/index")) p = p.slice(0, -"/index".length);
  return p || "/";
}

function titleFrom(frontmatter: string, body: string, route: string): string {
  const fm = frontmatter.match(/^title:\s*(.+?)\s*$/m);
  if (fm) return fm[1].replace(/^["']|["']$/g, "");
  const h1 = body.match(/^#\s+(.+?)\s*$/m);
  if (h1) return h1[1];
  return route.split("/").pop() || route;
}

// Pages that lead the index; everything else follows, ordered by title.
const ORDER = [
  "/docs",
  "/docs/install",
  "/docs/quickstart",
  "/docs/concepts",
  "/docs/suites",
  "/docs/personas-and-scenarios",
  "/docs/assertions",
  "/docs/judges",
  "/docs/metrics",
  "/docs/configuration",
  "/docs/targets/browser",
  "/docs/targets/rest",
  "/docs/targets/livekit",
  "/docs/targets/sip",
  "/docs/sip-setup",
  "/docs/cli",
  "/docs/reports",
  "/docs/ci",
  "/docs/ai",
  "/docs/go-api",
  "/docs/security",
  "/docs/contributing",
  "/docs/roadmap",
];

export function collectDocs(): DocPage[] {
  const pages: DocPage[] = Object.entries(rawPages).map(([key, raw]) => {
    const route = routeFor(key);
    const m = raw.match(FRONTMATTER);
    const frontmatter = m ? m[1] : "";
    const body = (m ? raw.slice(m[0].length) : raw).trim();
    return { path: route, title: titleFrom(frontmatter, body, route), body };
  });
  pages.sort((a, b) => {
    const ia = ORDER.indexOf(a.path);
    const ib = ORDER.indexOf(b.path);
    if (ia !== -1 || ib !== -1) {
      return (ia === -1 ? Infinity : ia) - (ib === -1 ? Infinity : ib);
    }
    return a.title.localeCompare(b.title);
  });
  return pages;
}

// Absolute URL honouring the configured base and site; falls back to a
// root-relative path when the site is not configured.
export function absoluteUrl(route: string, site: URL | undefined): string {
  const base = import.meta.env.BASE_URL || "/";
  const joined = (base.replace(/\/$/, "") + route).replace(/\/{2,}/g, "/");
  return site ? new URL(joined, site).href : joined;
}
