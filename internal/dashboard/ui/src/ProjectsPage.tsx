import { ProjectRow } from "./OverviewPage";
import { ProjectDetail } from "./ProjectDetail";
import { Empty, Icon, PageHeading } from "./ui";
import type { State } from "./api";

export function Projects({
  state,
  selected,
  onSelect,
  onNew,
  refresh,
}: {
  state: State;
  selected: string | null;
  onSelect: (id: string | null) => void;
  onNew: () => void;
  refresh: () => Promise<void>;
}) {
  const project = state.projects.find((p) => p.id === selected);
  if (project)
    return (
      <ProjectDetail
        project={project}
        state={state}
        onBack={() => onSelect(null)}
        refresh={refresh}
      />
    );
  return (
    <section>
      <PageHeading
        eyebrow="OUTCOMES, WITH OWNERSHIP"
        title="Projects"
        description="What you're moving forward, and what done looks like."
        action={
          <button className="button primary" onClick={onNew}>
            <Icon name="Plus" size={16} />
            Add project
          </button>
        }
      />
      {state.projects.length ? (
        <div className="project-list">
          {state.projects.map((p) => (
            <ProjectRow
              key={p.id}
              project={p}
              state={state}
              onSelect={() => onSelect(p.id)}
            />
          ))}
        </div>
      ) : (
        <Empty
          icon="Projects"
          title="One outcome is a good start"
          action={
            <button className="button primary" onClick={onNew}>
              Add project <Icon name="Arrow" size={15} />
            </button>
          }
        >
          Give the work a name and say what it is for. Then ask for what you
          need, and approve it when it's right.
        </Empty>
      )}
    </section>
  );
}
