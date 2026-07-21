# Automatic config reload

Replace the captured startup config pointer with a `ConfigStore`:

- On every config-dependent call it re-hashes `config.json` and
  parses/publishes a new immutable snapshot only when the content hash
  changed (an invalid hash is parsed once and cached). No filesystem
  watchers; atomic rename-based saves are caught naturally.
- Hot vs startup-only is one line: everything under `games` is hot;
  top-level settings (`apiKey`, `toolNormalization`, `portRanges`,
  `timeouts`, `stripOutputSchema`) are read at startup and documented as
  restart-required.
- Invalid config keeps the last valid snapshot: new starts refused with
  the exact error and path; read-only calls succeed and surface
  `configError`; stop/kill/status of active launches work from runtime
  snapshots. Fixing the file clears the condition on the next call.
- Startup with an invalid config file fails with the error (no fallback to
  an empty config — that would silently drop an `apiKey`).
- Each start pins exactly one snapshot. `tools/list` never changes from
  config edits.
- No reload tool. The loop after an edit: edit config → `games_show <id>`
  → the new state, revision, warnings, or exact error is right there.
  Structured results of list/show/status include `currentConfigRevision`
  (the editable file), the game's `activeConfigRevision` (pinned at
  launch) whenever a launch is active and the two differ, and any
  `configError` — an agent must be able to tell "what is running" from
  "what an edit would launch" without guessing which one a single field
  means.
