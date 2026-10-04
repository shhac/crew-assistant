---
name: sprite-atlas-pipeline
description: Deterministically extract, validate and integrate designer-delivered animation strips with provenance.
---

# Sprite pipeline for implementers

Use this skill for delivered sprite strips. The designer generates the art and gives visual approval. Request new art or visual corrections through the existing production hand-off; do not generate or redraw it yourself. This guidance grants no additional authority or processing executables.

Prepare a layout guide with 4–12 equal fixed cells, explicit dimensions, spacing and padding, and one common anchor (usually the character's foot or body baseline). Agree the guide and canonical base with the designer before generation. Keep guide marks out of the generated output.

Retain the untouched source and provenance.json, including exact prompts, references, settings, generator, hashes and producing turns. Record deterministic processing steps and parameters beside integrated assets. Preserve rejected variants as production evidence rather than silently substituting them.

Generated strips do not reliably follow requested slot geometry: never assume a neat grid. Identify each figure through connected components of the alpha or chroma mask. Require exactly the expected figure count, with no figure touching the strip edge or another figure. Crop each figure's actual bounding box, never equal slots. Apply one uniform scale and a shared anchor into fixed cells; every figure must fit with margin. Refuse overflow rather than clipping or cropping parts to fit. Validate image dimensions and frame order before composition. Clean only removable chroma-key background with a recorded tolerance; inspect edge pixels at native size. Never erase matching character colors or use cleanup to redraw parts.

Always attach the original generated strip of every attempt, including failed attempts, through the production hand-off. The designer calls attach_file with generated and rejected: true (asset empty) for each failed original, then generated and the wanted asset name (rejected empty) for the successful original. Failed strips retain their untouched bytes in the daemon's rejected-variants.zip; each wanted asset accepts one attachment per turn. Require both the successful source attachment and the archive of failed originals, and retain them alongside provenance. A processed atlas cannot replace the original strip. If both attempts fail, require both archived originals before escalation; processing remains your work.

Validate cell size, padding, alpha, anchor and frame order. Compose the atlas deterministically with explicit row names, frame counts, durations and cell coordinates. Generate a contact sheet showing every frame and a motion preview at intended playback speed, including several loops. Ask the designer to review identity, part continuity and the seam. A loop has no doubled seam frame. Idle remains subtle. Use one facing and runtime mirroring only when safe for asymmetry and motion.

Keep evidence lean: one contact sheet, one motion preview and a one-line QA note per row. Integrate approved assets using the project's existing runtime and asset conventions, retaining provenance. Preserve partial completion and cancellation through the existing production lifecycle. If the designer reports two failed attempts, return for direction instead of requesting an unbounded generation loop.

## Credit

The canonical-reference, layout-guide and lean-review ideas were informed by OpenAI's hatch-pet skill (Apache-2.0):
https://github.com/openai/skills/tree/main/skills/.curated/hatch-pet
License: https://github.com/openai/skills/blob/main/skills/.curated/hatch-pet/LICENSE.txt
This is original guidance; no upstream code or assets are copied.
