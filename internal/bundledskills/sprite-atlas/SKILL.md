---
name: sprite-atlas
description: Generate and review consistent character animation strips for the production hand-off.
---

# Sprite animation for designers

Use this skill when a task needs raster character animation. You generate and visually review the art. The implementer prepares guides, slices, registers, cleans, composes, previews and integrates delivered assets with provenance. This skill grants no repository-write or processing authority. Use the existing production hand-off; ask the implementer for deterministic guides or previews when needed. Use your available image-generation tool, not a new model or service.

## Plan a row

Establish one canonical base image of the character first. Ground every animation on that reference and a layout guide specifying frame count, equal cell dimensions, spacing, padding and a common anchor. The guide never appears in the output. Generate each animation as one single 4–12-frame strip in a single image, never as separately generated frames. Name the motion and frame order in the exact prompt.

Lock identity across every frame and row: face, markings, palette, materials, proportions, scale and silhouette. Lock part continuity: the left wing stays the left wing; legs, eye, beak and tail remain attached and identifiable unless deliberately moved by the motion. Reject swapped, doubled, detached or redrawn parts.

Use a flat chroma-key background whose color is absent from the character. No shadows, floor marks, motion lines, sparkles, detached effects, text, borders or guide marks. Every pixel belongs to the sprite or removable background. Leave enough padding for the whole motion.

Idle is subtle: breathing, a blink or a tiny bob. A loop must not repeat its first frame at the end: no doubled seam frame. Generate one facing; the implementer mirrors at runtime only when asymmetry and motion make it safe.

## Review and hand off

Generated strips do not reliably follow requested slot geometry: never assume a neat grid. The implementer finds each figure through connected components of the alpha or chroma mask and requires exactly the expected figure count, with no figure touching the strip edge or another figure. Crop each figure's actual bounding box, never equal slots. The implementer applies one uniform scale and a shared anchor into fixed cells; every figure must fit with margin. Refuse overflow rather than clipping or cropping parts to fit. Ask for these checks and review their results; processing remains the implementer's work.

Always attach the original generated strip of every attempt, including failed attempts, through the production hand-off. For each failed strip call attach_file with generated pointing to its original generated file and rejected: true, leaving asset empty. The daemon retains its untouched bytes in rejected-variants.zip, including failed attempts before a successful retry. Attach only the successful original with generated and asset set to the wanted asset name, leaving rejected empty; each wanted asset accepts one attachment per turn. Do not attach a failed strip as the wanted asset. A processed atlas cannot replace the original strip. If both attempts fail, archive both originals before escalating.

Review the strip, then the implementer's contact sheet for identity and part continuity and motion preview for rhythm, registration and the loop seam. Do not claim visual approval from dimensions alone. Keep evidence lean: one contact sheet, one motion preview and a one-line QA note per row are enough. Return the source strip, exact prompt, canonical reference, guide details, frame count, facing, anchor and provenance through the existing production attachments. Preserve rejected variants and producing-turn evidence through that hand-off.

If a row fails identity after two failed attempts, stop. Report the failing parts and ask for direction through the existing escalation format; do not keep generating variations. Partial delivery and cancellation use the ordinary production lifecycle.

## Credit

These ideas were informed by OpenAI's hatch-pet skill (Apache-2.0):
https://github.com/openai/skills/tree/main/skills/.curated/hatch-pet
License: https://github.com/openai/skills/blob/main/skills/.curated/hatch-pet/LICENSE.txt
This is original guidance; no upstream code or assets are copied.
