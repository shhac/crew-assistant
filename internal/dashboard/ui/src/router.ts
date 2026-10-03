export type ProjectTab =
  "board" | "brief" | "team" | "config" | "activity" | "pm";
export const projectTabs: ProjectTab[] = [
  "board",
  "brief",
  "team",
  "config",
  "activity",
  "pm",
];

export type Route =
  | { page: "inbox"; decision?: string }
  | { page: "projects" }
  | {
      page: "project";
      id: string;
      tab: ProjectTab;
      request?: string;
      /** The seat whose panel is open over the request. */
      seat?: string;
    }
  | { page: "team" }
  | { page: "member"; id: string }
  | { page: "assistant"; id: string }
  | { page: "memory" }
  | { page: "settings"; section?: string };

const decode = (part: string) => {
  try {
    return decodeURIComponent(part);
  } catch {
    return "";
  }
};

/** parseRoute reads the address. Anything unknown, or an old page, is the inbox. */
export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  switch (parts[0]) {
    case "inbox":
      return parts[1]
        ? { page: "inbox", decision: decode(parts[1]) }
        : { page: "inbox" };
    case "projects": {
      const id = parts[1] && decode(parts[1]);
      if (!id) return { page: "projects" };
      if (parts[2] === "requests" && parts[3]) {
        const request = decode(parts[3]);
        const seat = parts[4] === "team" && parts[5] && decode(parts[5]);
        return seat
          ? { page: "project", id, tab: "board", request, seat }
          : { page: "project", id, tab: "board", request };
      }
      const tab = projectTabs.find((t) => t === parts[2]) ?? "board";
      return { page: "project", id, tab };
    }
    case "team": {
      if (parts[1] === "assistant") {
        const id = parts[2] && decode(parts[2]);
        return id ? { page: "assistant", id } : { page: "team" };
      }
      const id = parts[1] && decode(parts[1]);
      return id ? { page: "member", id } : { page: "team" };
    }
    case "memory":
      return { page: "memory" };
    case "settings":
      return { page: "settings", section: parts[1] };
  }
  return { page: "inbox" };
}

export function href(route: Route): string {
  switch (route.page) {
    case "inbox":
      return route.decision
        ? `#/inbox/${encodeURIComponent(route.decision)}`
        : "#/inbox";
    case "projects":
      return "#/projects";
    case "team":
      return "#/team";
    case "member":
      return memberHref(route.id);
    case "assistant":
      return assistantHref(route.id);
    case "memory":
      return "#/memory";
    case "settings":
      return route.section ? `#/settings/${route.section}` : "#/settings";
    case "project": {
      const base = `#/projects/${encodeURIComponent(route.id)}`;
      if (route.request) {
        const request = `${base}/requests/${encodeURIComponent(route.request)}`;
        return route.seat
          ? `${request}/team/${encodeURIComponent(route.seat)}`
          : request;
      }
      return route.tab === "board" ? base : `${base}/${route.tab}`;
    }
  }
}

export const projectHref = (id: string, tab: ProjectTab = "board") =>
  href({ page: "project", id, tab });

export const requestHref = (projectID: string, taskID: string) =>
  href({ page: "project", id: projectID, tab: "board", request: taskID });

/** A request with one team member's panel open over it. */
export const seatHref = (projectID: string, taskID: string, seat: string) =>
  href({ page: "project", id: projectID, tab: "board", request: taskID, seat });

export const memberHref = (id: string) => `#/team/${encodeURIComponent(id)}`;

export const assistantHref = (id: string) =>
  `#/team/assistant/${encodeURIComponent(id)}`;
