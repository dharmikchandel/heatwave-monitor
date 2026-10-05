import { describe, expect, mock, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { AuthContext, type AuthContextValue, type AuthStatus } from "@/lib/AuthContext";
import type { AccountUser, InboxItem } from "@/lib/auth";

// next/navigation needs a mounted app router; these tests render components on their own.
let pathname = "/account";
mock.module("next/navigation", () => ({
  usePathname: () => pathname,
  useRouter: () => ({ push() {}, replace() {}, refresh() {} }),
}));

const { AccountsUnavailable } = await import("./AccountForm");
const { default: LoginForm } = await import("./LoginForm");
const { default: RegisterForm } = await import("./RegisterForm");
const { default: RequireAuth } = await import("./RequireAuth");
const { default: UserMenu } = await import("./UserMenu");
const { InboxCard } = await import("./InboxView");
const { UsersTable } = await import("./AdminView");
const { WatchButtons } = await import("./WatchControls");

const member: AccountUser = { id: 2, email: "ada@example.org", displayName: "Ada", role: "user", disabled: false, createdAt: "2026-05-01T09:00:00Z" };
const admin: AccountUser = { ...member, id: 1, email: "root@example.org", displayName: "Root", role: "admin" };

const noop = async () => {};
function auth(status: AuthStatus, user: AccountUser | null = null): AuthContextValue {
  return {
    status,
    user,
    version: 0,
    signIn: async () => ({ ok: true, user: member }),
    signUp: async () => ({ ok: true, user: member }),
    signOut: noop,
    sessionEnded: () => {},
  };
}
const as = (status: AuthStatus, user: AccountUser | null, el: React.ReactElement, path = "/account") => {
  pathname = path;
  return renderToStaticMarkup(<AuthContext.Provider value={auth(status, user)}>{el}</AuthContext.Provider>);
};

describe("the sign-in and registration forms", () => {
  test("sign-in has labelled fields the browser and password managers understand", () => {
    const html = as("anonymous", null, <LoginForm />);
    expect(html).toContain(">Email<");
    expect(html).toContain(">Password<");
    expect(html).toContain('type="email"');
    expect(html).toContain('autoComplete="email"');
    expect(html).toContain('autoComplete="current-password"');
    expect(html).toContain('type="password"'); // hidden until the person chooses to show it
    expect(html).toContain('aria-label="Show password"');
    expect(html).toContain('aria-label="Sign in"'); // the form is named for screen readers
    expect(html).toContain('href="/register"');
    expect(html).not.toContain('role="alert"'); // no error before anything is tried
  });

  test("registration asks for a new password, says how long it must be, and links back to sign-in", () => {
    const html = as("anonymous", null, <RegisterForm />);
    expect(html).toContain('autoComplete="new-password"');
    expect(html).toContain("At least 10 characters");
    expect(html).toContain(">Name<");
    expect(html).toContain("(optional)");
    expect(html).toContain('href="/login"');
  });

  test("each field is tied to its label", () => {
    const html = as("anonymous", null, <LoginForm />);
    for (const id of html.match(/<label for="([^"]+)"/g)!.map((m) => m.slice(12, -1))) expect(html).toContain(`id="${id}"`);
  });

  test("where there is no backend, both explain that instead of showing a dead form", () => {
    for (const el of [<LoginForm key="l" />, <RegisterForm key="r" />]) {
      const html = as("unavailable", null, el);
      expect(html).toContain("Accounts are not available here");
      expect(html).not.toContain("type=\"password\"");
    }
    expect(renderToStaticMarkup(<AccountsUnavailable />)).toContain('href="/"');
  });
});

describe("RequireAuth", () => {
  const page = <RequireAuth what="your inbox">SECRET-CONTENT</RequireAuth>;
  const adminPage = (
    <RequireAuth what="administration" admin>
      SECRET-CONTENT
    </RequireAuth>
  );

  test("anonymous visitors are asked to sign in, and sent back afterwards", () => {
    const html = as("anonymous", null, page, "/inbox");
    expect(html).toContain("Sign in to see your inbox");
    expect(html).toContain('href="/login?next=%2Finbox"');
    expect(html).toContain('href="/register?next=%2Finbox"');
    expect(html).not.toContain("SECRET-CONTENT");
  });

  test("while it is still finding out who is looking, it shows nothing private", () => {
    const html = as("loading", null, page);
    expect(html).not.toContain("SECRET-CONTENT");
    expect(html).not.toContain("Sign in to see");
  });

  test("members see member pages; administrator pages tell them they lack access", () => {
    expect(as("signed-in", member, page)).toContain("SECRET-CONTENT");
    const html = as("signed-in", member, adminPage, "/admin");
    expect(html).toContain("Administrator access required");
    expect(html).toContain('role="alert"');
    expect(html).not.toContain("SECRET-CONTENT");
  });

  test("administrators see everything", () => {
    expect(as("signed-in", admin, adminPage, "/admin")).toContain("SECRET-CONTENT");
  });

  test("with no backend there is nothing to protect and nothing to show", () => {
    const html = as("unavailable", null, adminPage);
    expect(html).toContain("Accounts are not available here");
    expect(html).not.toContain("SECRET-CONTENT");
  });
});

describe("UserMenu", () => {
  test("anonymous visitors get a sign-in link that returns them to the page they were on", () => {
    expect(as("anonymous", null, <UserMenu />, "/alerts")).toContain('href="/login?next=%2Falerts"');
    expect(as("anonymous", null, <UserMenu />, "/login")).toContain('href="/login"'); // no loop back to the form itself
    expect(as("anonymous", null, <UserMenu />, "/register")).not.toContain("next=");
  });

  test("without accounts there is no account UI at all", () => {
    expect(as("unavailable", null, <UserMenu />)).toBe("");
  });

  test("a signed-in person sees their initial, the inbox bell and a closed menu", () => {
    const html = as("signed-in", member, <UserMenu />);
    expect(html).toContain('aria-label="Inbox"');
    expect(html).toContain('aria-label="Account menu for Ada"');
    expect(html).toContain(">A<");
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain('role="menu"');
  });

  test("it falls back to the email when there is no name", () => {
    const html = as("signed-in", { ...member, displayName: "" }, <UserMenu />);
    expect(html).toContain("Account menu for ada@example.org");
    expect(html).toContain(">A<");
  });
});

describe("WatchButtons", () => {
  const base = { saved: false, alertsOn: false, ready: true, busy: false, error: null, onToggleSaved: () => {}, onToggleAlerts: () => {} };

  test("a city that is neither saved nor followed offers both", () => {
    const html = renderToStaticMarkup(<WatchButtons {...base} />);
    expect(html).toContain("Save city");
    expect(html).toContain("Alerts off");
    expect(html.match(/aria-pressed="false"/g)).toHaveLength(2);
  });

  test("saved and followed cities say so, and expose the state to assistive technology", () => {
    const html = renderToStaticMarkup(<WatchButtons {...base} saved alertsOn />);
    expect(html).toContain(">Saved<");
    expect(html).toContain("Alerts on");
    expect(html.match(/aria-pressed="true"/g)).toHaveLength(2);
  });

  test("nothing can be pressed before the lists have loaded, or while a change is in flight", () => {
    expect(renderToStaticMarkup(<WatchButtons {...base} ready={false} />).match(/disabled=""/g)).toHaveLength(2);
    expect(renderToStaticMarkup(<WatchButtons {...base} busy />).match(/disabled=""/g)).toHaveLength(2);
    expect(renderToStaticMarkup(<WatchButtons {...base} />)).not.toContain('disabled=""');
  });

  test("a failure is announced", () => {
    const html = renderToStaticMarkup(<WatchButtons {...base} error="Your watchlist is full." />);
    expect(html).toContain('role="alert"');
    expect(html).toContain("Your watchlist is full.");
  });
});

describe("InboxCard", () => {
  const item: InboxItem = {
    id: 5,
    kind: "opened",
    level: "extreme-danger",
    createdAt: "2026-05-01T09:00:00Z",
    alertId: 3,
    locationId: 7,
    locationName: "Mumbai",
    headline: "Extreme Danger heat alert for Mumbai",
    summary: "Apparent temperature reaches 52 °C on two days.",
  };
  const now = new Date("2026-05-01T09:30:00Z");

  test("an unread alert shows what happened, how bad, when, and can be marked read", () => {
    const html = renderToStaticMarkup(<InboxCard item={item} now={now} />);
    expect(html).toContain("Extreme Danger heat alert for Mumbai");
    expect(html).toContain("Apparent temperature reaches 52 °C on two days.");
    expect(html).toContain("Heat alert opened · Extreme Danger");
    expect(html).toContain("30m ago");
    expect(html).toContain("Mark read");
    expect(html).toContain("(unread)");
    expect(html).toContain('href="/alerts"');
  });

  test("a read one has no mark-read button and no unread marker", () => {
    const html = renderToStaticMarkup(<InboxCard item={{ ...item, readAt: "2026-05-01T09:10:00Z" }} now={now} />);
    expect(html).not.toContain("Mark read");
    expect(html).not.toContain("(unread)");
  });

  test("an all-clear is worded as one, and coloured as relief rather than danger", () => {
    const html = renderToStaticMarkup(<InboxCard item={{ ...item, kind: "resolved", level: "normal" }} now={now} />);
    expect(html).toContain("All clear");
    expect(html).toContain("#10b981");
  });

  test("an escalation says so", () => {
    expect(renderToStaticMarkup(<InboxCard item={{ ...item, kind: "escalated" }} now={now} />)).toContain("Heat alert escalated");
  });

  test("it renders before the clock is known without a time", () => {
    expect(renderToStaticMarkup(<InboxCard item={item} now={null} />)).not.toContain("ago");
  });
});

describe("UsersTable", () => {
  const users: AccountUser[] = [admin, member, { ...member, id: 3, email: "bob@example.org", displayName: "Bob", disabled: true, lastLoginAt: "2026-05-01T07:30:00Z" }];
  const render = (meId: number | null, busyId: number | null = null) =>
    renderToStaticMarkup(<UsersTable users={users} meId={meId} now={new Date("2026-05-01T09:30:00Z")} busyId={busyId} onToggle={() => {}} />);

  test("lists every account with its role and state", () => {
    const html = render(1);
    for (const email of ["root@example.org", "ada@example.org", "bob@example.org"]) expect(html).toContain(email);
    expect(html).toContain("Administrator");
    expect(html).toContain(">Active<");
    expect(html).toContain(">Disabled<");
    expect(html).toContain("2h ago");
    expect(html).toContain('scope="col"');
  });

  test("offers disable for active accounts and enable for disabled ones, each named for its account", () => {
    const html = render(1);
    expect(html).toContain('aria-label="Disable ada@example.org"');
    expect(html).toContain('aria-label="Enable bob@example.org"');
  });

  test("an administrator cannot act on their own account", () => {
    const html = render(1);
    expect(html).toContain("This is you");
    expect(html).not.toContain('aria-label="Disable root@example.org"');
  });

  test("a row being changed cannot be pressed twice", () => {
    expect(render(1, 2)).toContain('disabled=""');
    expect(render(1)).not.toContain('disabled=""');
  });
});
