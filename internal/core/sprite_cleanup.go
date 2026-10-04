package core

import (
	"slices"
	"strings"
)

// LegacySpriteInstructions is the owner's complete stopgap instructions field.
// Match it literally, never by sprite-related words or partial paragraphs.
const LegacySpriteInstructions = `Sprite and animation work (lessons from the hatch-pet Codex skill at ~/.codex/skills/hatch-pet and from the robin animation):
- Generate each animation as one row strip of 4–12 frames in a single image, never as separately generated frames. Ground every strip on one canonical base image of the character, plus a layout guide for the frame count, spacing and padding (the guide never appears in the output).
- Lock identity: the same face, markings, palette, materials, proportions, scale and silhouette in every frame and row. Parts keep their identity frame to frame: the left wing stays the left wing, and the legs, eye, beak and tail stay put, unless the motion deliberately says otherwise. Reject strips that swap, double or redraw parts.
- Use a flat chroma-key background. No shadows, floor marks, motion lines, sparkles, detached effects, text, borders or guide marks: everything is either the sprite or removable background.
- Idle is subtle: breathing, a blink, a tiny bob. Loops must not repeat their first frame at the end. Generate one facing; mirroring is done at runtime when it's safe.
- You make the art. Deterministic work (slicing, registration, cleanup, atlas composition, contact sheets, motion previews) belongs to the implementer. Review identity on the contact sheet and the motion preview. Hand back the strip, its exact prompt and provenance, and a one-line QA note. Keep evidence lean.
- If a strip fails identity after two attempts, say so and stop rather than looping.`

// EffectiveSeatInstructions suppresses only an exact whole derived block for
// a member whose cleanup succeeded. Historical seats remain evidence.
func (v Snapshot) EffectiveSeatInstructions(r Role) string {
	if r.Member == "" || !slices.Contains(v.RetiredSpriteMembers, r.Member) {
		return r.Instructions
	}
	parts := strings.Split(r.Instructions, "\n\n")
	parts = slices.DeleteFunc(parts, func(part string) bool { return part == LegacySpriteInstructions })
	return strings.Join(parts, "\n\n")
}
