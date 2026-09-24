export type ProjectTab = "board" | "brief" | "team" | "landing" | "activity";
export const projectTabs: ProjectTab[] = [
  "board",
  "brief",
  "team",
  "landing",
  "activity",
];

export type Route =
  | { page: "inbox" }
  | { page: "projects" }
  | { page: "project"; id: string; tab: ProjectTab; request?: string }
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
    case "projects": {
      const id = parts[1] && decode(parts[1]);
      if (!id) return { page: "projects" };
      if (parts[2] === "requests" && parts[3])
        return { page: "project", id, tab: "board", request: decode(parts[3]) };
      const tab = projectTabs.find((t) => t === parts[2]) ?? "board";
      return { page: "project", id, tab };
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
      return "#/inbox";
    case "projects":
      return "#/projects";
    case "memory":
      return "#/memory";
    case "settings":
      return route.section ? `#/settings/${route.section}` : "#/settings";
    case "project": {
      const base = `#/projects/${encodeURIComponent(route.id)}`;
      if (route.request)
        return `${base}/requests/${encodeURIComponent(route.request)}`;
      return route.tab === "board" ? base : `${base}/${route.tab}`;
    }
  }
}

export const projectHref = (id: string, tab: ProjectTab = "board") =>
  href({ page: "project", id, tab });

export const requestHref = (projectID: string, taskID: string) =>
  href({ page: "project", id: projectID, tab: "board", request: taskID });
