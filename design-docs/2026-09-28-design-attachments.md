# Design attachments and the current design

Written 2026-09-28. Nothing to pin: code-internal, built on the notes thread and role tools as they stood that day.

## Where attachments lived

A task kept one list of attachment records, and each file under the state directory at `attachments/<task>/<id>` (directory 0700, file 0600, written to a temporary file and renamed). Every record said who added it, when, and what it came with: exactly one of an owner's note or a design the designer gave. There was no separate store for notes and for the task.

That gave three views of one store: a note showed its own files inline in the notes thread, each design in the design list showed its files, and the request view got a top-level Attachments area listing every file with what it came with ("With your note", "Design 2 · Current design", "Design 1 · Superseded").

One store was chosen over note attachments plus a separate task area because the size limits, serving, what roles may read, and QA's later screenshot evidence (CA-25) then share one path, and no file loses its context. The alternative, a free-standing task area, would have left a file with no answer to "which design was this?", which is the question that mattered across design rounds.

## Limits and types

5 MB a file, 10 files a note or a design, 30 files or 50 MB a task. A type was judged from the bytes and the name's extension, never from what the sender claimed: PNG, JPEG, GIF and WebP had to decode as that format and be at most 10,000 pixels a side; PDF had to start `%PDF-`; Markdown, plain text, JSON, CSV, SVG and HTML had to be UTF-8 with no NUL bytes. Names could not be paths, start with a dot or hold control characters, and were only ever shown; files were named by id. A set was checked whole before anything was written, and files written for a change that then failed were removed, so a note and its files were kept together or not at all.

Files were served behind the dashboard's sign-in with their judged type, `nosniff` and `Content-Security-Policy: sandbox; default-src 'none'`. Images and plain text were shown in the browser (SVG as an image, so thumbnails worked); HTML and PDF were downloaded.

## Consistency with the chat composer

The chat composer kept inlining UTF-8 text into the message and refusing images, since a chat message has no file transport. The notes form reused its drop, paste and pick flow, its removable chips, its exact-size refusal wording (`exactSize`, `sizeLabel`, `carriesFiles` were imported from `composerAssets.ts`, not copied), but sent files as a multipart form, since a note has a file transport. The UI's limits mirrored core's, as the composer's mirrored the chat byte limit.

## Who could attach

The owner, with a note. The designer, during its designing turn only, with a new `attach_file` role tool: text it wrote out (an SVG, HTML or Markdown mockup), or a file already in its workspace, resolved with `os.Root` so no symlink or `..` led out of it, and only read, never run. The designer's sandbox did not change: it still could not write to the workspace. A file could be attached only to the request still open; once answered, or once the task moved on, it was refused.

## The current design

The designer's input on a task was numbered: design 1, 2, and so on. Its answer said which design was current afterwards: `"this"`, `"design N"` to keep or bring back an earlier one, or nothing for advice only. The task recorded it (`CurrentDesign`) in the same change as the input, with an activity entry such as "Misha marked design 3 current". A design once current and then replaced stayed on the record as superseded.

Every role that got design context read the current design first as the target, then superseded designs as "not current and not the target", then advice. Roles could open every attachment: the task's attachment directory was added to their readable directories and their instructions listed each file's path with what it came with. QA's check prompt named the current design in one line, and `read_task` showed it with the superseded numbers and the attachment count. The dashboard badged the current design, muted superseded ones and marked advice.

## Left out

Deleting or editing attachments, attachments from roles other than the designer, the owner choosing the current design from the dashboard, server-side thumbnails or transcoding, and any change to the role sandbox.
