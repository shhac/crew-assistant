import type { Project, Repository, State, Team } from "./api";

/** The repository a project works in, if any. */
export function projectRepository(
  project: Project,
  repositories: Repository[] = [],
): Repository | undefined {
  const id = project.scope?.repositories?.[0]?.id;
  return id ? repositories.find((r) => r.id === id) : undefined;
}

/** The team that staffs a project, if any. */
export function projectTeam(
  project: Project,
  teams: Team[] = [],
): Team | undefined {
  return project.team ? teams.find((t) => t.id === project.team) : undefined;
}

/** The projects that work in a repository. */
export function repositoryUsers(state: Pick<State, "projects">, id: string) {
  return state.projects.filter((p) =>
    p.scope?.repositories?.some((r) => r.id === id),
  );
}

/** The projects a team staffs. */
export function teamUsers(state: Pick<State, "projects">, id: string) {
  return state.projects.filter((p) => p.team === id);
}

/**
 * What a change to a shared repository or team reaches, beside this
 * project: the other projects' titles, or none.
 */
export function sharedWith(
  state: Pick<State, "projects"> | undefined,
  project: Project,
  what: "repository" | "team",
): string[] {
  if (!state) return [];
  const id =
    what === "team" ? project.team : project.scope?.repositories?.[0]?.id;
  if (!id) return [];
  const users =
    what === "team" ? teamUsers(state, id) : repositoryUsers(state, id);
  return users.filter((p) => p.id !== project.id).map((p) => p.title);
}

/** Paths typed as a list, one per comma or line. */
export const pathList = (text: string) =>
  text
    .split(/[,\n]/)
    .map((p) => p.trim())
    .filter(Boolean);

/** Code areas typed one per line, as name: path, path. */
export function parseAreas(text: string) {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [name, ...rest] = line.split(":");
      return { name: name.trim(), paths: pathList(rest.join(":")) };
    });
}

/** Code areas as they are typed. */
export const areasText = (r?: Pick<Repository, "areas">) =>
  (r?.areas ?? []).map((a) => `${a.name}: ${a.paths.join(", ")}`).join("\n");
