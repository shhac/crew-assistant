import type { Member, Role } from "./api";
import { Avatar } from "./Avatar";
import { kindsLabel } from "./members";
import { ProviderIcon } from "./ProviderIcon";

/** The team-page identity, with the pinned seat's role and optional stage detail. */
export function MemberIdentity({
  role,
  member,
  size = 24,
  detail,
  headingId,
}: {
  role: Role;
  member?: Member;
  size?: number;
  detail?: string;
  headingId?: string;
}) {
  const name = (
    <>
      {role.name} <ProviderIcon engine={member?.engine ?? role.engine} />
    </>
  );
  const Container = headingId ? "div" : "span";
  return (
    <Container className="task-member-identity">
      {member && <Avatar of={member} size={size} />}
      <Container className="member-card-text">
        {headingId ? (
          <h2 className="member-name">
            <span id={headingId}>{role.name}</span>{" "}
            <ProviderIcon engine={member?.engine ?? role.engine} />
          </h2>
        ) : (
          <span className="member-name">{name}</span>
        )}
        <span className="soft small">
          {[detail ?? kindsLabel(role.kinds), member?.model ?? role.model]
            .filter(Boolean)
            .join(" · ")}
        </span>
      </Container>
    </Container>
  );
}
