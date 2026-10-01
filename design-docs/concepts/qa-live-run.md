# Checking that QA runs the app: a live-run checklist

Last reviewed: 2026-09-28. Pinned to: code-internal, on lib-agent-harness v0.13.0 (`Sandbox.Loopback`, `Options.Browser`, tool images).

QA on a code project can start the app from the project's **run recipe**, reach it on this machine only, use it through Claude's own browser, and keep what it saw with its verdict. The tests cover this with fake runners only; this checklist is the one run against a real project, done by the owner on the released build after landing.

## Before you start

- A code project whose app serves over http on this machine, and whose check (`make check` or similar) already passes.
- The app's dependencies already installed in your own repository, since QA's shell reaches nothing off this machine: setup can't download anything. List the ignored folders it needs (such as `node_modules`) under **Config → Workspace → Ignored folders to copy in**.
- Chrome running with the Claude in Chrome extension connected. It is your real Chrome, with your logins; QA is told to open only the app's address, in tabs of its own, and to close them.
- A start command whose server macOS lets accept connections. With the macOS application firewall on, a server binary it has not allowed may never start listening, and the app is then reported as never ready. On macOS 27 Homebrew's Python was held this way while Apple's `/usr/bin/python3` served at once; allow your dev server in **System Settings → Network → Firewall → Options**, or start it with an allowed binary.

## 1. Add the project's run recipe

**Config → Running the app → Add a recipe.**

- **Setup** (optional): runs once, offline, before the app starts, such as `npm run build`. Setup and start run in a writable copy of the change in QA's scratch folder, so a build can write its output; the change itself, and the check, stay read-only.
- **Start**: starts the app in the background, reading its port from `PORT`, such as `npm start`.
- **Address**: where it answers, on `127.0.0.1`, `localhost` or `[::1]`, with `{port}` as the address's port and nowhere else, such as `http://127.0.0.1:{port}/`. Each check is given a port of its own.
- **Ready when** (optional): a command that succeeds once the app is ready, such as `curl -sf http://127.0.0.1:{port}/health`; empty waits until the address answers.

The researcher or the PM may propose one instead: it comes as a decision, "… proposes how QA runs …", showing the recipe, and applies only if you choose **Use this recipe**. A request already under way keeps the recipe it started with, so queue a new one after adding it.

## 2. Seat Claude QA and switch its browser on

- Seat a QA member on Claude: **Team → Assign QA**. (The template's QA is on Codex, which can't run the app; see below.)
- Switch the browser on, either for the member (**Team page → the member → Edit → Browser → "QA uses the browser to try the app"**) before seating them, or for this project only (**project → Team → QA's browser → Edit**).
- Leave **Connected browser** empty to use the browser the extension connects by default, which is the normal case. Give a name only if several browsers are connected; QA selects it with `select_browser` first.

The setting can't be switched on for an engine whose sandboxed sessions don't admit a browser (Grok). Claude and Codex both can: Codex's runs through the ChatGPT app's bridge, which lib-agent-harness proves is confined by the session's sandbox before launch.

## 3. Run a request through QA

Queue a small, visible change, such as a label or a button, with a criterion QA can see in the page. Let it run through implementing and review to QA. While QA works, its card shows browser tool calls.

## 4. What a passing verdict looks like

On the request, under **Changes**, open the change QA checked. QA's check shows:

- its outcome, **Pass**, and a summary saying the check passed and the app did what the request asks;
- up to four **screenshots**, as thumbnails, each opening the full image; they are also listed under **Attachments** as "*QA*'s screenshot, checking draft *N*";
- **Page**, **Console** and **Network** findings in words, such as "No errors in the console." or "GET / answered 200.";
- if QA took more than four screenshots, a line saying how many more were not kept.

Afterwards nothing QA started is still running: `lsof -iTCP -sTCP:LISTEN` shows no app on the port it used.

A verdict that **revises** has a finding for each thing in the app that didn't work, with the same evidence beside it.

## Run on 2026-09-28

Checked on crew-assistant 0.28.0 and macOS 27 with a one-page static site served by `cd site && exec /usr/bin/python3 -m http.server "$PORT" --bind 127.0.0.1`, a Claude QA member with the browser on and no connected-browser name, and the request "Change the page heading to read 'Hello from QA'". QA's verdict was **Pass**: it served the page on the port it was given, read the heading through Chrome, attached one screenshot (JPEG, 13 KB) to the verdict, and recorded page, console ("only the expected 'demo page loaded'") and network findings. The reviewer's check cited the same screenshot, and nothing was left listening afterwards.

## What else you may see

- **QA on Codex**, or any engine the harness doesn't offer loopback for, runs the check only and says so: a finding "Running the app: The app was not run: …", quoting the harness's reason. It is never given a wider network instead.
- **No recipe**: QA runs the check exactly as before, with no evidence.
- **Screenshots not kept**: a request keeps at most 30 attachments or 50 MB. Once it is full, the check says its screenshots could not be kept.
- **The app never became ready**, or setup needed the network: QA judges the check alone and says why in a finding.
