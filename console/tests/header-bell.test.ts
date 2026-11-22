import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, test } from "node:test";
import { createRawSnippet } from "svelte";
import { render } from "svelte/server";
import type { Notification } from "../src/lib/api-types.ts";
import {
  headerNav,
  mobileOrgItems,
  type NavOrg,
  type NavProject,
} from "../src/lib/header-nav.ts";
import {
  bellLabel,
  readBody,
  readNotificationList,
} from "../src/lib/notifications.ts";
import {
  bellUnread,
  canMarkAll,
  createBell,
  initialBellState,
  MARK_ERROR,
  type BellState,
} from "../src/lib/notifications-bell.ts";
import type { Handle } from "@sveltejs/kit";
import { handle } from "../src/hooks.server.ts";
import {
  parseNotification,
  parseNotificationList,
  parseReadBody,
} from "../src/lib/server/notifications.ts";
import { parseMe, parseProject } from "../src/lib/server/shape.ts";
import NotificationsBell from "../src/lib/components/NotificationsBell.svelte";
import NotificationsList from "../src/lib/components/NotificationsList.svelte";
import RootLayout from "../src/routes/+layout.svelte";
import { GET, POST } from "../src/routes/notifications/+server.ts";
import {
  envelope,
  fakeEvent,
  installFetch,
  restoreEnv,
  TOKEN,
  useApi,
  USER,
} from "./harness.ts";
import { setPageUrl, setRouteId } from "./stubs/app-state.ts";

// spec-header-org-project-bell §3 and §6; api-v1 v1.37.

const A = "00000000-0000-4000-8000-0000000000a1";
const B = "00000000-0000-4000-8000-0000000000b1";
const OTHER = "00000000-0000-4000-8000-0000000000c1";
const P1 = "00000000-0000-4000-8000-000000000101";
const P2 = "00000000-0000-4000-8000-000000000102";
const P3 = "00000000-0000-4000-8000-000000000103";
const P9 = "00000000-0000-4000-8000-000000000109";

const ORGS: NavOrg[] = [
  { id: B, name: "Zeta Org", role: "member" },
  { id: A, name: "Alpha Org", role: "owner" },
];
const PROJECTS: NavProject[] = [
  { id: P2, name: "web", org_id: A, org_name: "Alpha Org" },
  { id: P1, name: "api", org_id: A, org_name: "Alpha Org" },
  { id: P3, name: "docs", org_id: B, org_name: "Zeta Org" },
  { id: P9, name: "shared", org_id: OTHER, org_name: "Partner Org" },
];

const nav = (
  path: string,
  orgs = ORGS,
  projects = PROJECTS,
  pageOrgId: string | null = null,
) => headerNav({ path, orgs, projects, pageOrgId });

describe("header model: current org and project", () => {
  test("org owner on a project page: its org with settings, its projects sorted, New project", () => {
    const model = nav(`/projects/${P2}`);
    assert.deepEqual(model.org, { id: A, name: "Alpha Org", menu: true });
    assert.equal(model.projectLabel, "web");
    assert.deepEqual(
      model.projects.map((p) => [p.name, p.current]),
      [
        ["api", false],
        ["web", true],
      ],
    );
    assert.deepEqual(model.orgPage, {
      label: "Organization settings",
      href: `/orgs/${A}?tab=settings`,
    });
    assert.equal(model.canCreateProject, true);
  });

  test('org member: "Organization" link and no New project', () => {
    const model = nav(`/projects/${P3}/`);
    assert.deepEqual(model.org, { id: B, name: "Zeta Org", menu: true });
    assert.deepEqual(model.orgPage, {
      label: "Organization",
      href: `/orgs/${B}`,
    });
    assert.equal(model.canCreateProject, false);
    assert.deepEqual(
      model.projects.map((p) => p.name),
      ["docs"],
    );
  });

  test("the org menu lists orgs by name, checks the current one, members land on Projects", () => {
    const model = nav(`/projects/${P3}`);
    assert.deepEqual(model.orgs, [
      { id: A, name: "Alpha Org", href: `/orgs/${A}`, current: false },
      {
        id: B,
        name: "Zeta Org",
        href: `/orgs/${B}?tab=projects`,
        current: true,
      },
    ]);
  });

  test("project-only member: the org name as plain text, no org page link, projects of that org", () => {
    const model = nav(`/projects/${P9}`);
    assert.deepEqual(model.org, {
      id: OTHER,
      name: "Partner Org",
      menu: false,
    });
    assert.equal(model.orgPage, null);
    assert.equal(model.canCreateProject, false);
    assert.deepEqual(
      model.projects.map((p) => p.name),
      ["shared"],
    );
  });

  test("an older API without org_name: project part only", () => {
    const model = nav(
      `/projects/${P9}`,
      ORGS,
      PROJECTS.map((p) => ({ ...p, org_name: null })),
    );
    assert.equal(model.org, null);
    assert.equal(model.projectLabel, "shared");
  });

  test('/projects: the first org by name and "All projects"', () => {
    const model = nav("/projects");
    assert.equal(model.org?.name, "Alpha Org");
    assert.equal(model.projectLabel, "All projects");
    assert.equal(model.projectId, null);
    assert.equal(
      model.projects.some((p) => p.current),
      false,
    );
  });

  test('org page: that org and "Select a project"', () => {
    const model = nav(`/orgs/${B}`);
    assert.equal(model.org?.id, B);
    assert.equal(model.projectLabel, "Select a project");
  });

  test("host page: the org its data names", () => {
    const model = nav(`/hosts/${P1}`, ORGS, PROJECTS, B);
    assert.equal(model.org?.id, B);
    assert.equal(model.projectLabel, "Select a project");
  });

  test("org admin (not owner): settings link and New project", () => {
    const orgs: NavOrg[] = [{ id: A, name: "Alpha Org", role: "admin" }];
    const model = nav(`/projects/${P1}`, orgs);
    assert.deepEqual(model.org, { id: A, name: "Alpha Org", menu: true });
    assert.deepEqual(model.orgPage, {
      label: "Organization settings",
      href: `/orgs/${A}?tab=settings`,
    });
    assert.equal(model.canCreateProject, true);
    assert.equal(model.orgs[0].href, `/orgs/${A}`);
  });

  test('/projects/<unknown id>: no current project, the first org, "Select a project"', () => {
    const model = nav("/projects/00000000-0000-4000-8000-00000000dead");
    assert.equal(model.projectId, null);
    assert.equal(model.projectLabel, "Select a project");
    assert.equal(model.org?.id, A);
    assert.equal(
      model.projects.some((p) => p.current),
      false,
    );
  });

  test("/orgs/<id> of an org the caller is not in: no org part, no org page, no New project", () => {
    const model = nav(`/orgs/${OTHER}`);
    assert.equal(model.org, null);
    assert.equal(model.orgPage, null);
    assert.equal(model.canCreateProject, false);
    assert.equal(
      model.orgs.some((o) => o.current),
      false,
    );
    assert.equal(model.projectLabel, "Select a project");
  });

  test("host page with no org in its data: the first org by name", () => {
    const model = nav(`/hosts/${P1}`, ORGS, PROJECTS, null);
    assert.equal(model.org?.id, A);
    assert.equal(model.projectLabel, "Select a project");
  });

  test("phone menu of a project-only member: no org items (so no All projects), the projects only", () => {
    const model = nav(`/projects/${P9}`, [], PROJECTS);
    assert.equal(mobileOrgItems(model), false);
    assert.equal(model.orgPage, null);
    assert.deepEqual(model.org, {
      id: OTHER,
      name: "Partner Org",
      menu: false,
    });
    assert.deepEqual(
      model.projects.map((p) => [p.name, p.current]),
      [["shared", true]],
    );
    // An org member gets the org items first.
    assert.equal(mobileOrgItems(nav(`/projects/${P1}`)), true);
  });

  test("no orgs at all: no org part, every project listed", () => {
    const model = nav("/projects", [], PROJECTS);
    assert.equal(model.org, null);
    assert.equal(model.projects.length, 4);
  });
});

describe("header render", () => {
  const children = createRawSnippet(() => ({ render: () => "<p>child</p>" }));
  const user = { id: USER.id, name: USER.name, avatarUrl: null };
  const page = (path: string, route: string) => {
    setPageUrl(`http://console.example${path}`);
    setRouteId(route);
    return render(RootLayout, {
      props: {
        children,
        data: {
          user,
          orgs: ORGS,
          projects: PROJECTS,
          theme: "system" as const,
          timeZone: "UTC",
          whatsNew: { version: "0.2.39" },
        },
      },
    }).body;
  };

  test('triggers carry "Organization: <name>" and "Project: <name>"; no Projects link or old switcher', () => {
    const html = page(`/projects/${P2}`, "/projects/[id]");
    assert.match(html, /aria-label="Organization: Alpha Org"/);
    assert.match(html, /aria-label="Project: web"/);
    assert.equal(html.includes("Switch organization"), false);
    assert.doesNotMatch(html, /aria-label="Primary"/);
  });

  test("a project-only member sees the org name as text, not a menu", () => {
    const html = page(`/projects/${P9}`, "/projects/[id]");
    assert.match(
      html,
      /data-org-text[^>]*>\s*<span class="sr-only">Organization:<\/span>\s*Partner Org/,
    );
    assert.equal(
      html.includes('aria-label="Organization: Partner Org"'),
      false,
    );
  });

  test("below sm only the project part shows (the org part and desktop project trigger are hidden)", () => {
    const source = readFileSync(
      new URL("../src/lib/components/HeaderNav.svelte", import.meta.url),
      "utf8",
    );
    assert.match(source, /aria-label=\{orgTriggerName\(nav\.org\.name\)\}/);
    assert.match(
      source,
      /class=\{cn\(trigger, 'hidden sm:inline-flex'\)\} aria-label=\{orgTriggerName/,
    );
    assert.match(source, /id=\{PROJECT_MOBILE_ID\} class=\{mobileTrigger\}/);
    assert.match(
      source,
      /const mobileTrigger = cn\(trigger, '[^']*\bsm:hidden\b/,
    );
    // The phone menu starts with the org name and the org items.
    const mobile = source.slice(source.indexOf("id={PROJECT_MOBILE_ID}"));
    assert.ok(
      mobile.indexOf("{@render orgItems(PROJECT_MOBILE_ID)}") <
        mobile.indexOf("{@render projectItems(PROJECT_MOBILE_ID)}"),
    );
    assert.match(
      source,
      /<NewProject[\s\S]*action="\/projects\?\/create"[\s\S]*showTrigger=\{false\}/,
    );
    assert.match(source, /\{#if nav\.canCreateProject\}[\s\S]*New project/);
  });

  test("the bell sits before the theme switch and the account menu", () => {
    const html = page("/projects", "/projects");
    const bell = html.indexOf('aria-label="Notifications"');
    assert.ok(bell > 0);
    const toggle = html.indexOf('data-slot="toggle-group"');
    assert.ok(toggle > 0, "theme switch missing");
    assert.ok(bell < toggle);
    assert.ok(bell < html.indexOf(USER.name));
  });

  test("below sm: logo, project trigger and bell share one no-wrap row; only the nav shrinks", () => {
    const html = page(`/projects/${P2}`, "/projects/[id]");
    const row =
      /<div data-header-main="" class="([^"]*)"/.exec(html)?.[1] ?? "";
    assert.match(row, /\bflex-nowrap\b/);
    assert.match(row, /\bsm:contents\b/);
    const main = html.slice(
      html.indexOf("data-header-main"),
      html.indexOf("</nav>"),
    );
    assert.match(
      main,
      /<a class="shrink-0" href="\/projects" aria-label="Deploybot">/,
    );
    assert.match(
      html,
      /<nav class="flex min-w-0 [^"]*" aria-label="Organization and project">/,
    );
    assert.match(html, /<div class="ml-auto shrink-0 sm:order-1">/);
    // The phone project trigger: at most 40vw below sm, 12rem from sm, 44 px tall below sm.
    const mobile =
      /<button[^>]*id="header-project-mobile"[^>]*>/.exec(html)?.[0] ?? "";
    const cls = /class="([^"]*)"/.exec(mobile)?.[1] ?? "";
    for (const want of [
      "max-w-[40vw]",
      "sm:max-w-48",
      "sm:hidden",
      "max-sm:h-11",
      "min-w-0",
    ])
      assert.ok(cls.split(" ").includes(want), `${want} in ${cls}`);
    assert.equal(cls.split(" ").includes("max-w-48"), false, cls);
    const desktop =
      /<button[^>]*id="header-project"[^>]*>/.exec(html)?.[0] ?? "";
    assert.match(desktop, /class="[^"]*\bmax-w-48\b[^"]*\bmax-sm:h-11\b/);
    assert.match(
      html,
      /<button[^>]*class="[^"]*max-sm:size-11[^"]*"[^>]*aria-label="Notifications"|<button[^>]*aria-label="Notifications"[^>]*class="[^"]*max-sm:size-11/,
    );
  });

  test("the theme switch keeps sm:ml-auto when signed out (no bell), not when signed in", () => {
    setPageUrl("http://console.example/signin");
    setRouteId("/signin");
    const out = render(RootLayout, {
      props: {
        children,
        data: {
          user: null,
          orgs: [],
          projects: [],
          theme: "system" as const,
          timeZone: "UTC",
          whatsNew: null,
        },
      },
    }).body;
    assert.match(
      out,
      /<div class="sm:order-1 sm:ml-auto">\s*(<!--[^>]*-->\s*)*<form/,
    );
    assert.equal(
      out.includes("data-popover-trigger"),
      false,
      "no bell when signed out",
    );
    const signedIn = page("/projects", "/projects");
    assert.equal(signedIn.includes("sm:order-1 sm:ml-auto"), false);
  });

  test('menu items: the current org and project carry sr-only "(current)", the check icon is hidden', () => {
    const source = readFileSync(
      new URL("../src/lib/components/HeaderNav.svelte", import.meta.url),
      "utf8",
    );
    assert.match(
      source,
      /<Check aria-hidden="true" class=\{cn\('size-4', org\.current/,
    );
    assert.match(
      source,
      /\{#if org\.current\}<span class="sr-only"> \(current\)<\/span>\{\/if\}/,
    );
    assert.match(
      source,
      /<Check aria-hidden="true" class=\{cn\('size-4', project\.current/,
    );
    assert.match(
      source,
      /\{#if project\.current\}<span class="sr-only"> \(current\)<\/span>\{\/if\}/,
    );
  });

  test("phone menu: org and project items are labelled groups", () => {
    const source = readFileSync(
      new URL("../src/lib/components/HeaderNav.svelte", import.meta.url),
      "utf8",
    );
    const mobile = source.slice(source.indexOf("id={PROJECT_MOBILE_ID}"));
    assert.match(
      mobile,
      /\{#if mobileOrgItems\(nav\)\}\s*<DropdownMenu\.Group data-menu-group="orgs">\s*<DropdownMenu\.GroupHeading>Organizations<\/DropdownMenu\.GroupHeading>\s*\{@render orgItems\(PROJECT_MOBILE_ID\)\}/,
    );
    assert.match(
      mobile,
      /<DropdownMenu\.Group data-menu-group="projects">\s*<DropdownMenu\.GroupHeading>[^<]*Projects[^<]*<\/DropdownMenu\.GroupHeading>\s*\{@render projectItems\(PROJECT_MOBILE_ID\)\}/,
    );
  });
});

describe("breadcrumbs move to the header", () => {
  test("the org and project pages have no PageHeader breadcrumbs", () => {
    for (const file of [
      "src/routes/orgs/[id]/+page.svelte",
      "src/routes/projects/[id]/+page.svelte",
    ]) {
      const source = readFileSync(
        new URL(`../${file}`, import.meta.url),
        "utf8",
      );
      assert.equal(source.includes("breadcrumbs="), false, file);
    }
    // The host page keeps its own.
    const host = readFileSync(
      new URL("../src/routes/hosts/[id]/+page.svelte", import.meta.url),
      "utf8",
    );
    assert.match(host, /breadcrumbs=\{crumbs\}/);
  });
});

describe("/v1/me org_name (api-v1 v1.37 item 4)", () => {
  const raw = {
    id: P9,
    name: "shared",
    slug: "shared",
    role: "viewer",
    created_at: "2026-11-19T00:00:00Z",
    org_id: OTHER,
    via: "member",
  };
  test("parsed when present; missing, empty or odd is null, and the project stays", () => {
    assert.equal(
      parseProject({ ...raw, org_name: "Partner Org" })?.org_name,
      "Partner Org",
    );
    assert.equal(parseProject(raw)?.org_name, null);
    assert.equal(parseProject({ ...raw, org_name: "" })?.org_name, null);
    assert.equal(parseProject({ ...raw, org_name: 42 })?.org_name, null);
    const me = parseMe({
      user: USER,
      projects: [{ ...raw, org_name: "Partner Org" }],
      orgs: [],
    });
    assert.equal(me?.projects[0].org_name, "Partner Org");
  });
});

const ITEMS: Notification[] = [
  {
    key: "release:0.2.40",
    kind: "release",
    title: "Deploybot v0.2.40 is out",
    detail: "A bell.",
    href: "/whats-new#v0.2.40",
    at: "2026-11-19T00:00:00Z",
    read: false,
  },
  {
    key: `host_update:${P1}:0.2.10`,
    kind: "host_update",
    title: "Update available on pi1",
    detail: "bot-agent 0.2.10 (this host: 0.2.9)",
    href: `/hosts/${P1}`,
    at: "2026-11-18T00:00:00Z",
    read: true,
  },
];

describe("bell", () => {
  test("accessible name from the unread count", () => {
    assert.equal(bellLabel(2), "Notifications, 2 unread");
    assert.equal(bellLabel(0), "Notifications");
    assert.equal(bellLabel(null), "Notifications");
  });

  test("the dot shows only while something is unread and the list loaded", () => {
    const html = (initial: {
      list: { items: Notification[]; unread: number } | null;
      failed: boolean;
    }) => render(NotificationsBell, { props: { initial } }).body;
    const unread = html({ list: { items: ITEMS, unread: 1 }, failed: false });
    assert.match(unread, /aria-label="Notifications, 1 unread"/);
    assert.equal(unread.includes("data-bell-dot"), true);
    const none = html({
      list: { items: ITEMS.map((i) => ({ ...i, read: true })), unread: 0 },
      failed: false,
    });
    assert.match(none, /aria-label="Notifications"/);
    assert.equal(none.includes("data-bell-dot"), false);
    const failed = html({ list: null, failed: true });
    assert.equal(failed.includes("data-bell-dot"), false);
    assert.match(failed, /aria-label="Notifications"/);
  });

  test("rows: newest first as sent, unread marked, read rows muted, icon by kind", () => {
    const html = render(NotificationsList, { props: { items: ITEMS } }).body;
    assert.ok(
      html.indexOf("Deploybot v0.2.40 is out") <
        html.indexOf("Update available on pi1"),
    );
    assert.match(
      html,
      /href="\/whats-new#v0\.2\.40"[^>]*data-read="false"[^>]*class="[^"]*text-foreground/,
    );
    assert.match(
      html,
      /href="\/hosts\/[^"]+"[^>]*data-read="true"[^>]*class="[^"]*text-muted-foreground/,
    );
    assert.equal(
      (html.match(/<span class="sr-only">Unread:<\/span>/g) ?? []).length,
      1,
    );
    assert.match(html, /lucide-sparkles/);
    assert.match(html, /lucide-server-cog/);
    assert.match(html, /bot-agent 0\.2\.10 \(this host: 0\.2\.9\)/);
    assert.match(html, /<time datetime="2026-11-19T00:00:00Z"/);
  });

  test("error and empty states", () => {
    assert.match(
      render(NotificationsList, { props: { items: null, failed: true } }).body,
      /Notifications could not be loaded\./,
    );
    assert.match(
      render(NotificationsList, { props: { items: [] } }).body,
      /Nothing new\./,
    );
  });

  test("the popover has an accessible name from its title", () => {
    const source = readFileSync(
      new URL(
        "../src/lib/components/NotificationsBell.svelte",
        import.meta.url,
      ),
      "utf8",
    );
    assert.match(source, /<Popover\.Content[^>]*aria-labelledby=\{TITLE_ID\}/);
    assert.match(
      source,
      /<Popover\.Title id=\{TITLE_ID\} tabindex=\{-1\} bind:ref=\{titleEl\}>Notifications<\/Popover\.Title>/,
    );
    assert.doesNotMatch(source, /BOT_API_URL|\/v1\//);
  });

  test("Mark all read keeps focus in the popover when it disables itself (browser check 2026-11-19)", () => {
    const source = readFileSync(
      new URL(
        "../src/lib/components/NotificationsBell.svelte",
        import.meta.url,
      ),
      "utf8",
    );
    assert.match(
      source,
      /onclick=\{\(event\) => void markAll\(event\.currentTarget\)\}>Mark all read/,
    );
    assert.match(
      source,
      /await controller\.markAll\(\);\s*if \(button\.matches\(':disabled'\) && \(document\.activeElement === button \|\| document\.activeElement === document\.body\)\) titleEl\?\.focus\(\);/,
    );
  });

  test("rows carry the full title and detail as a tooltip", () => {
    const html = render(NotificationsList, { props: { items: ITEMS } }).body;
    assert.match(html, /title="Deploybot v0\.2\.40 is out"/);
    assert.match(html, /title="bot-agent 0\.2\.10 \(this host: 0\.2\.9\)"/);
  });

  test("a failed refresh shows the error line above the last list that loaded", () => {
    const html = render(NotificationsList, {
      props: { items: ITEMS, failed: true },
    }).body;
    assert.ok(html.indexOf("Notifications could not be loaded.") >= 0);
    assert.ok(
      html.indexOf("Notifications could not be loaded.") <
        html.indexOf("Deploybot v0.2.40 is out"),
    );
    assert.equal(html.includes("Loading…"), false);
  });
});

/** A stub fetch for the bell: each call is recorded and answered by `answer`. */
function bellHarness(
  answer: (call: {
    method: string;
    body: string | null;
  }) => Response | Promise<Response>,
) {
  const calls: { url: string; method: string; body: string | null }[] = [];
  const events: string[] = [];
  let state: BellState = initialBellState();
  const bell = createBell({
    fetch: async (input, init) => {
      const call = {
        url: input,
        method: init?.method ?? "GET",
        body: typeof init?.body === "string" ? init.body : null,
      };
      calls.push(call);
      events.push(`${call.method} ${call.body ?? ""}`.trim());
      return answer(call);
    },
    goto: async (href) => {
      events.push(`goto ${href}`);
    },
    update: (next) => {
      state = next;
    },
  });
  return { bell, calls, events, state: () => state };
}

const listResponse = (items: Notification[]) =>
  new Response(
    JSON.stringify({ items, unread: items.filter((i) => !i.read).length }),
    { status: 200 },
  );
const noContent = () => new Response(null, { status: 204 });

describe("bell behaviour (stub fetch and goto)", () => {
  test("the list is fetched after mount (start) and again on open, never on close", async () => {
    const h = bellHarness(() => listResponse(ITEMS));
    assert.equal(h.calls.length, 0);
    await h.bell.start();
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].url, "/notifications");
    assert.equal(h.state().list?.unread, 1);
    await h.bell.openChanged(true);
    assert.equal(h.calls.length, 2);
    await h.bell.openChanged(false);
    assert.equal(h.calls.length, 2);
    assert.equal(
      h.calls.every((c) => c.method === "GET"),
      true,
      "opening marks nothing",
    );
    const source = readFileSync(
      new URL(
        "../src/lib/components/NotificationsBell.svelte",
        import.meta.url,
      ),
      "utf8",
    );
    assert.match(source, /onMount\(\(\) => \{\s*void controller\.start\(\);/);
    assert.match(
      source,
      /onOpenChange=\{\(next\) => void controller\.openChanged\(next\)\}/,
    );
  });

  test("an initial failed fetch renders the error line and no dot", async () => {
    const h = bellHarness(() => new Response("{}", { status: 503 }));
    await h.bell.start();
    assert.deepEqual(h.state(), {
      list: null,
      loadFailed: true,
      marking: false,
      markFailed: false,
    });
    assert.equal(bellUnread(h.state()), null);
    const html = render(NotificationsList, {
      props: {
        items: h.state().list?.items ?? null,
        failed: h.state().loadFailed,
      },
    }).body;
    assert.match(html, /Notifications could not be loaded\./);
    const bellHtml = render(NotificationsBell, {
      props: { initial: { list: null, failed: true } },
    }).body;
    assert.equal(bellHtml.includes("data-bell-dot"), false);
  });

  test("a failed refresh keeps the last good list", async () => {
    let fail = false;
    const h = bellHarness(() =>
      fail ? Promise.reject(new Error("down")) : listResponse(ITEMS),
    );
    await h.bell.start();
    fail = true;
    await h.bell.refresh();
    assert.equal(h.state().loadFailed, true);
    assert.deepEqual(h.state().list?.items, ITEMS);
    assert.equal(bellUnread(h.state()), 1);
    fail = false;
    await h.bell.refresh();
    assert.equal(h.state().loadFailed, false);
  });

  test("a stale response is ignored", async () => {
    const resolvers: ((r: Response) => void)[] = [];
    const h = bellHarness(
      () => new Promise<Response>((resolve) => resolvers.push(resolve)),
    );
    const first = h.bell.refresh();
    const second = h.bell.refresh();
    resolvers[1](listResponse([ITEMS[1]]));
    await second;
    resolvers[0](listResponse(ITEMS));
    await first;
    assert.deepEqual(h.state().list?.items, [ITEMS[1]]);
  });

  test("a row click POSTs the read mark before goto, then refreshes", async () => {
    const h = bellHarness((call) =>
      call.method === "POST" ? noContent() : listResponse(ITEMS),
    );
    let closed = false;
    await h.bell.openItem(ITEMS[0], () => (closed = true));
    assert.equal(closed, true);
    assert.deepEqual(h.events.slice(0, 2), [
      'POST {"keys":["release:0.2.40"]}',
      "goto /whats-new#v0.2.40",
    ]);
    // A read row navigates without a POST.
    const read = bellHarness(() => listResponse(ITEMS));
    await read.bell.openItem(ITEMS[1], () => {});
    assert.equal(read.events[0], `goto /hosts/${P1}`);
  });

  test("a failed row mark still navigates and sets the inline error", async () => {
    const h = bellHarness((call) =>
      call.method === "POST"
        ? new Response(null, { status: 503 })
        : listResponse(ITEMS),
    );
    await h.bell.openItem(ITEMS[0], () => {});
    assert.equal(h.state().markFailed, true);
    assert.equal(h.events[1], "goto /whats-new#v0.2.40");
    assert.equal(MARK_ERROR, "Could not mark as read.");
    // Opening again clears it.
    await h.bell.openChanged(true);
    assert.equal(h.state().markFailed, false);
  });

  test("Mark all read POSTs {all:true} and refreshes; it is off while running and when nothing is unread", async () => {
    let release: (() => void) | null = null;
    let allRead = false;
    const h = bellHarness((call) => {
      if (call.method === "POST") {
        return new Promise<Response>((resolve) => {
          release = () => {
            allRead = true;
            resolve(noContent());
          };
        });
      }
      return listResponse(
        allRead ? ITEMS.map((i) => ({ ...i, read: true })) : ITEMS,
      );
    });
    await h.bell.start();
    assert.equal(canMarkAll(h.state()), true);
    const running = h.bell.markAll();
    assert.equal(h.state().marking, true);
    assert.equal(canMarkAll(h.state()), false);
    await h.bell.markAll(); // ignored while running
    assert.equal(h.calls.filter((c) => c.method === "POST").length, 1);
    (release as unknown as () => void)();
    await running;
    assert.deepEqual(h.events.slice(1, 3), ['POST {"all":true}', "GET"]);
    assert.equal(h.state().list?.unread, 0);
    assert.equal(canMarkAll(h.state()), false);
    const before = h.calls.length;
    await h.bell.markAll();
    assert.equal(h.calls.length, before, "nothing unread: no POST");
    const source = readFileSync(
      new URL(
        "../src/lib/components/NotificationsBell.svelte",
        import.meta.url,
      ),
      "utf8",
    );
    assert.match(source, /disabled=\{!canMarkAll\(bell\)\}/);
    assert.match(source, /\{#if bell\.markFailed\}[\s\S]*\{MARK_ERROR\}/);
  });

  test("a failed Mark all read shows the inline error and does not refresh", async () => {
    const h = bellHarness((call) =>
      call.method === "POST"
        ? Promise.reject(new Error("down"))
        : listResponse(ITEMS),
    );
    await h.bell.start();
    await h.bell.markAll();
    assert.equal(h.state().markFailed, true);
    assert.equal(h.state().marking, false);
    assert.equal(h.calls.length, 2);
    assert.equal(readBody(["k"]), '{"keys":["k"]}');
    assert.equal(readNotificationList({ message: "x" }), null);
  });
});

describe("hooks: /notifications signed out", () => {
  test("a signed-out caller gets JSON 401 (no-store), not the sign-in redirect", async () => {
    useApi("https://api.test");
    const calls = installFetch(() => new Response("nope", { status: 401 }));
    try {
      const { event } = fakeEvent({
        url: "http://console.example/notifications",
        session: TOKEN,
      });
      const response = await handle({
        event,
        resolve: async () => new Response("should-not-run"),
      } as Parameters<Handle>[0]);
      assert.equal(response.status, 401);
      assert.equal(response.headers.get("cache-control"), "no-store");
      assert.match(
        response.headers.get("content-type") ?? "",
        /application\/json/,
      );
      assert.deepEqual(await response.json(), { message: "Sign in required." });
      assert.equal(calls.length, 1, "only the session check");
      // Without a session cookie too.
      const bare = fakeEvent({ url: "http://console.example/notifications" });
      const noCookie = await handle({
        event: bare.event,
        resolve: async () => new Response("no"),
      } as Parameters<Handle>[0]);
      assert.equal(noCookie.status, 401);
    } finally {
      restoreEnv();
    }
  });
});

describe("notification parsing (api-v1 v1.37 item 1)", () => {
  test("a bad item is left out and unread is recounted", () => {
    const list = parseNotificationList({
      items: [
        ITEMS[0],
        { ...ITEMS[1], href: "https://evil.example/" },
        { ...ITEMS[1], href: "//evil" },
        { ...ITEMS[1], kind: "other" },
        ITEMS[1],
      ],
      unread: 99,
    });
    assert.deepEqual(list, { items: [ITEMS[0], ITEMS[1]], unread: 1 });
    assert.equal(parseNotification({ ...ITEMS[0], read: "no" }), null);
    assert.equal(parseNotificationList({}), null);
  });

  test("read body: keys (1–50, 1–200 chars) or all, never both", () => {
    assert.deepEqual(parseReadBody({ keys: ["a"] }), { keys: ["a"] });
    assert.deepEqual(parseReadBody({ all: true }), { all: true });
    assert.equal(parseReadBody({ keys: ["a"], all: true }), null);
    assert.equal(parseReadBody({}), null);
    assert.equal(parseReadBody({ keys: [] }), null);
    assert.equal(parseReadBody({ keys: [""] }), null);
    assert.equal(parseReadBody({ keys: ["x".repeat(201)] }), null);
    assert.equal(
      parseReadBody({ keys: Array.from({ length: 51 }, (_, i) => `k${i}`) }),
      null,
    );
    assert.equal(parseReadBody({ all: false }), null);
  });
});

describe("/notifications console route", () => {
  const API = "https://api.test";
  const URL_ = "http://console.example/notifications";

  function event(input: {
    user?: boolean;
    method?: string;
    body?: string;
    origin?: string;
    type?: string;
  }) {
    const fake = fakeEvent({
      url: URL_,
      session: TOKEN,
      user: input.user === false ? null : USER,
    });
    const headers = new Headers();
    if (input.origin !== undefined) headers.set("origin", input.origin);
    if (input.type !== undefined) headers.set("content-type", input.type);
    (fake.event as { request: Request }).request = new Request(URL_, {
      method: input.method ?? "GET",
      headers,
      body: input.body,
    });
    return fake.event as never;
  }

  test("signed out: 401 JSON, no API call", async () => {
    useApi(API);
    const calls = installFetch(() => envelope({ items: [], unread: 0 }));
    try {
      const get = await GET(event({ user: false }));
      assert.equal(get.status, 401);
      const post = await POST(
        event({
          user: false,
          method: "POST",
          body: '{"all":true}',
          origin: "http://console.example",
          type: "application/json",
        }),
      );
      assert.equal(post.status, 401);
      assert.equal(calls.length, 0);
    } finally {
      restoreEnv();
    }
  });

  test("GET proxies /v1/me/notifications with the session bearer, no-store", async () => {
    useApi(API);
    const calls = installFetch(() => envelope({ items: ITEMS, unread: 1 }));
    try {
      const response = await GET(event({}));
      assert.equal(response.status, 200);
      assert.equal(response.headers.get("cache-control"), "no-store");
      assert.deepEqual(await response.json(), { items: ITEMS, unread: 1 });
      assert.equal(calls[0].url.pathname, "/v1/me/notifications");
      assert.equal(calls[0].authorization, `Bearer ${TOKEN}`);
    } finally {
      restoreEnv();
    }
  });

  test("GET: an API failure is a JSON error the bell shows as its error line", async () => {
    useApi(API);
    installFetch(() => envelope(null, 503, "unavailable", "down"));
    try {
      const response = await GET(event({}));
      assert.equal(response.status, 503);
      assert.equal(response.headers.get("cache-control"), "no-store");
      assert.deepEqual(await response.json(), {
        message: "Notifications could not be loaded.",
      });
    } finally {
      restoreEnv();
    }
  });

  test("POST: same origin and JSON only, a valid body, then proxied to the API", async () => {
    useApi(API);
    // The API answers the read mark with the usual envelope (api-v1 v1.37 item 2); the console route answers 204.
    const calls = installFetch(() => envelope(null));
    try {
      const ok = {
        method: "POST",
        origin: "http://console.example",
        type: "application/json",
      };
      assert.equal(
        (
          await POST(
            event({
              ...ok,
              origin: "http://evil.example",
              body: '{"all":true}',
            }),
          )
        ).status,
        403,
      );
      assert.equal(
        (await POST(event({ ...ok, type: "text/plain", body: '{"all":true}' })))
          .status,
        415,
      );
      assert.equal(
        (await POST(event({ ...ok, body: '{"keys":["a"],"all":true}' })))
          .status,
        400,
      );
      assert.equal((await POST(event({ ...ok, body: "nope" }))).status, 400);
      assert.equal(calls.length, 0);
      const response = await POST(
        event({ ...ok, body: '{"keys":["release:0.2.40"]}' }),
      );
      assert.equal(response.status, 204);
      assert.equal(response.headers.get("cache-control"), "no-store");
      assert.equal(calls[0].method, "POST");
      assert.equal(calls[0].url.pathname, "/v1/me/notifications/read");
      assert.equal(calls[0].body, '{"keys":["release:0.2.40"]}');
      const all = await POST(event({ ...ok, body: '{"all":true}' }));
      assert.equal(all.status, 204);
      assert.equal(calls[1].body, '{"all":true}');
    } finally {
      restoreEnv();
    }
  });
});
