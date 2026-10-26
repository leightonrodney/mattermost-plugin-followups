# Testing

## Running the tests

```bash
make test              # server and webapp unit tests; needs only Go and npm
make test-ci           # the same, with JUnit output for CI
make check-style       # gofmt, go vet, golangci-lint, eslint, tsc, manifest check
make test-integration  # end-to-end against a real Mattermost server; needs Docker
```

Individually:

```bash
go test -race ./server/...                  # server
go test -race -run TestNextMonthly ./...    # one test
cd webapp && npm test                       # webapp
cd webapp && npm test -- --watch            # webapp, watching
```

The race detector is not optional for the server. The scheduler, the
compare-and-set loops and the delivery claim protocol are the parts most worth
testing, and they are all concurrent; CI runs them with `-race` for the same
reason.

## What is covered, and what was actually run

Everything in this section was executed on 25 October 2026 on macOS (arm64) with
Go 1.27.3 and Node 24.19.0, unless stated otherwise.

### Server — 85 tests, 180 including subtests, all passing under `-race`

| Package | Tests | What it covers |
|:--|:--|:--|
| `server/reminder` | 21 (91 with subtests) | Date and time parsing, recurrence arithmetic, daylight saving, validation |
| `server/store` | 18 | Key-value records, the due index, the cursor, concurrency |
| `server` | 46 (71 with subtests) | Quotas, ownership, the delivery protocol, the scheduler, the HTTP API, interactive buttons, configuration |

Specifically:

**Parsing and time** — every documented `/remind` form; relative offsets,
glued units (`30m`), articles (`an hour`, `half an hour`); named days, weekdays,
`next` forms; ISO, month-name and day-month dates; 12- and 24-hour clock times,
`noon`, `midnight`; text before or after the time, with and without `to`;
rejection of ambiguous slash dates, impossible dates (`2026-02-30`, `Feb 30`),
impossible clock times (`25:00`, `10:75`) and zero intervals; multibyte text;
length limits; and that the same phrase in two timezones produces two different
instants.

**Recurrence** — daily, weekly with multiple weekdays, every-other-week,
monthly, and fixed intervals; February clamping for "the 31st", including a leap
year; that a series anchored on its own days keeps them after a missed run; that
a fortnight of missed occurrences produces one firing rather than fourteen; and
the human-readable descriptions.

**Daylight saving**, in a zone with both transitions in the test year
(America/New_York, 2026):
- "every day at 9am" stays at 9am across both transitions, with 23 and 25 real
  hours elapsing respectively;
- a wall-clock time that the spring-forward jump skips (02:30) still fires
  exactly once that day, strictly after the previous day's firing, and the
  series returns to 02:30 afterwards;
- a wall-clock time that happens twice on fall-back fires once;
- a fixed interval is unaffected by either.

**Storage and concurrency** — round-trips; that one user's record is invisible
in another's; that 20 concurrent writers to one user's record all survive, with
no lost update; that 25 concurrent writers to a single index shard all land;
that re-indexing is idempotent; that the index reads every shard; that an
oversized record is refused; that a corrupt cursor is treated as absent rather
than fatal; that an expired bucket is not written.

**Quotas and ownership** — that 20 simultaneous creations against a limit of 5
yield exactly 5, because the check runs inside the compare-and-set; that each of
get, cancel, delete, snooze, complete, reschedule and retext refuses another
user's reminder with the same error as a nonexistent one, and leaves the owner's
reminder untouched.

**Delivery protocol** — that exactly one of ten concurrent workers claims an
occurrence; that a claim within its window is not stolen and one past its
timeout is reclaimed; that a cancelled reminder is never delivered; that a
delivered one-off does not fire again; that a repeating one is advanced and
re-indexed; that failures back off, retry, and stop after the attempt limit.

**Scheduler** — delivering a due reminder; not delivering one that is not due;
delivering only once across five runs of the same sweep; delivering only once
across four *concurrent* sweeps; catching up across several hour buckets;
recovering after a simulated restart, by rebuilding the plugin against the same
storage; honouring the disabled setting and delivering late once re-enabled;
advancing a recurring series; retrying a failed post.

**HTTP API** — that every route refuses an unauthenticated request; create,
list, snooze, complete; that another user gets 404 from cancel, snooze,
complete, delete and update, and sees nothing in their own list; rejection of
unparseable phrases, missing times, unknown fields and malformed bodies;
security headers; the parse preview.

**Interactive buttons** — that a crafted payload naming another user as
`user_id` cannot move that user's reminder, because the acting user comes from
the authenticated header; that the owner's own click works; that the "tomorrow"
preset resolves in the user's timezone; that a malformed payload is refused.

**Permalinks** — that a delivered reminder includes a link to its source
message, and that the link is withheld, with an explanation, when the user can
no longer read that channel.

**Configuration** — that out-of-range values are clamped, and that the defaults
in `defaultConfiguration()` match the defaults declared in `plugin.json`, so a
fresh install behaves the way the settings page says it will.

### Webapp — 85 tests across 6 suites, all passing

| Suite | Tests | What it covers |
|:--|:--|:--|
| `components/rhs.test.tsx` | 26 | The sidebar |
| `components/reminder_modal.test.tsx` | 25 | The dialog and the repeat-rule builder |
| `client.test.ts` | 8 | The API client |
| `time.test.ts` | 15 | Formatting and the datetime-local round trip |
| `store.test.ts` | 7 | Shared state |
| `theme.test.ts` | 4 | Theme compatibility |

Specifically:

**Creation and editing** — that the dialog refuses an empty message and a past
time; that it sends the right payload; that the repeat selection is translated
correctly for each option; that presets fill the date field; that editing
prefills from an existing reminder and recovers the right repeat option; that a
message-linked reminder carries its post and channel IDs.

**Listing** — loading, empty and populated states; the three tabs and what each
filters; repeat text; permalinks present and absent; the overdue marker; a
banner when an administrator has turned delivery off.

**Actions** — snooze, cancel, edit and delete, each calling through and
refreshing; that a failure shows the server's own message and leaves the row in
place.

**Form validation and errors** — client-side validation before any request, and
the server's refusal shown verbatim when there is one.

**Accessibility** — the dialog exposes `role="dialog"` and `aria-modal`; every
field is reachable by its label; focus lands in the message box on open; Escape
closes; the tab list uses `role="tablist"`/`role="tab"`/`role="tabpanel"` with
`aria-selected` and `aria-controls`; errors live in an `aria-live` region. Every
assertion in the component tests finds controls **by role and accessible name**
rather than by class or test ID, so a change that breaks the accessible name
breaks the tests.

**Theme compatibility** — the stylesheet is scanned directly: no hard-coded hex
colour, no `rgb()`/`rgba()` literal except neutral black overlays for shadows
and the dialog backdrop, the Mattermost theme custom properties are used, and
every class is namespaced to the plugin. This is checked on the source rather
than on a render because a hard-coded colour is invisible in a jsdom test under
one theme and only shows up as an unreadable sidebar under another.

### Build

Executed here:

- `go build ./server/...` and `go vet ./server/...` — clean
- `golangci-lint run ./...` (v2.15.0, 19 linters) — **0 issues**
- `tsc` — clean
- `eslint` — clean
- `webpack --mode=production` — bundle built, 93.5 KiB
- `make dist` — bundle built, 37 MB, all five platform binaries present
- `build/check-bundle.sh` — manifest, icon, webapp bundle, all five server
  binaries present, and the bundled manifest version matches the build
- `make test-integration` — 7 integration tests against Mattermost 12.0.1 Team
  Edition, all passing, including end-to-end reminder delivery

### Integration — 7 tests, all passing against a real server

Executed on 26 October 2026 against **Mattermost 12.0.1 Team Edition**
(`mattermost/mattermost-team-edition:latest`), started in Docker by
testcontainers. The whole `server` package, unit and integration together, took
141 seconds.

It covers:

- that the plugin installs into a real Mattermost server and activates;
- that its API answers an authenticated request and refuses an unauthenticated
  one;
- that `/remind` is registered and creates and lists a reminder;
- that an unreadable time is explained rather than accepted;
- that a second user cannot see or cancel the first user's reminder, and that
  the owner's reminder is untouched by the attempt;
- that a reminder set 35 seconds ahead is actually delivered as a direct message
  from the bot, and that its stored status becomes `delivered`.

The one that matters most is `TestADueReminderIsDelivered`, which took 56
seconds because it waits for a real scheduler tick. Everything else only proves
the plugin *starts*; this is the only test anywhere that watches a reminder
actually come due and arrive as a direct message from the bot on a real server.

To run it:

```bash
# Docker must be running
make test-integration
```

By default the suite pulls `mattermost/mattermost-enterprise-edition:latest`.
`MM_TEST_IMAGE` overrides that, which is how to check the declared
`min_server_version` rather than merely asserting it:

```bash
MM_TEST_IMAGE=mattermost/mattermost-team-edition:9.5 make test-integration
MM_TEST_IMAGE=mattermost/mattermost-team-edition:11.7 make test-integration
```

Worth running across 9.5 (the declared minimum), the current ESR and latest
before a release.

It takes several minutes: it pulls a Mattermost image, resets the database
between tests, and the delivery test waits for a real scheduler tick.

**Still not verified:**

- **The declared `min_server_version` of 9.5.0.** The suite has only been run
  against latest. Pin the image to 9.5 and run it again before a release.
- Behaviour on a multi-node cluster. The design uses `cluster.Schedule` and
  per-reminder compare-and-set claiming for exactly this case, and the
  concurrency is tested in-process, but it has not been run against two real
  nodes.
- The mobile apps. Reminders are ordinary direct messages, so they should
  follow the normal push path, but this has not been confirmed on a device.
- Screen readers. The component tests assert roles and accessible names, which
  is not the same as using one.

## Writing tests

- **Server.** `pluginapi.MemoryStore` gives the real compare-and-set semantics
  without a server, so the store and service tests exercise the actual
  concurrency rules rather than a stub that always agrees with them.
  `plugintest.API` backed by an in-memory key-value map with correct
  compare-and-set covers the plugin level, including the scheduler and the HTTP
  API.
- **Time.** The service and the scheduler both take an injectable clock. No test
  sleeps waiting for wall-clock time.
- **Webapp.** React Testing Library with queries by role and accessible name.
  `@testing-library/dom` is pinned in `package.json` so that React Testing
  Library and `user-event` share one copy — with two copies they keep separate
  configuration objects, `user-event` never sees the act wrapper, and every
  interaction reports "not wrapped in act". The suite currently runs with **no**
  React warnings; keep it that way.
