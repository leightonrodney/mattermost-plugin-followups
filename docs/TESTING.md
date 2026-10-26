# Testing

## Running the tests

```bash
make test              # server and webapp unit tests; needs only Go and npm
make test-ci           # the same, with JUnit output for CI
make check-style       # gofmt, go vet, golangci-lint, eslint, tsc, manifest check
make test-integration  # end-to-end against a real Mattermost server; needs Docker
make test-cluster      # two real nodes against one Postgres; needs Docker, slower
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

### Server — all passing under `-race`

| Package | What it covers |
|:--|:--|
| `server/reminder` | Date and time parsing, recurrence arithmetic, daylight saving, validation |
| `server/store` | Key-value records, the due index, the cursor, concurrency |
| `server` | Quotas, ownership, the delivery protocol, the scheduler, multiple nodes, notification eligibility, the HTTP API, interactive buttons, configuration |

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

**Multiple nodes** — several plugin instances, each with its own client,
store, service and configuration, sharing one key-value store. That is what a
cluster looks like to this plugin: separate process memory, one database. The
tests run the sweeps *concurrently on purpose*, which production avoids by
holding a cluster-wide lock — the point is to exercise the per-reminder
compare-and-set claiming that is the backstop when that lock's 15-second lease
lapses under a stalled node. Four nodes sweeping the same hour deliver once;
twenty reminders across four users are each delivered exactly once; a recurring
series advances one step, not three; the cursor is shared so a second node does
not re-sweep; and a node that claims a reminder then dies has it reclaimed by
another once the claim goes stale, rather than stranding it.

This needs no Enterprise licence, because `cluster.Mutex` depends on
`KVSetWithOptions` and nothing else — the coordination is compare-and-set
against the shared database, with no cluster bus involved.

**Delivery as a notification** — the plugin cannot make a push notification
happen; the server decides that from each person's settings. What it is
responsible for is checkable, and these assert it: delivery is a persisted post
and not an ephemeral one, it comes from the bot into a direct channel, it is not
a `system_*` post type, its text is in the message body rather than only in an
attachment, and the snooze buttons stay out of the body. Each of those could
stop being true without any other test noticing, because nothing else looks at
*how* the post is made. Switching delivery to `SendEphemeralPost` fails all five.

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

### Webapp — 99 tests across 7 suites, all passing

| Suite | Tests | What it covers |
|:--|:--|:--|
| `components/rhs.test.tsx` | 26 | The sidebar |
| `components/reminder_modal.test.tsx` | 25 | The dialog and the repeat-rule builder |
| `client.test.ts` | 8 | The API client |
| `time.test.ts` | 15 | Formatting and the datetime-local round trip |
| `store.test.ts` | 7 | Shared state |
| `theme.test.ts` | 4 | Theme compatibility |
| `components/accessibility.test.tsx` | 14 | axe-core over the dialog, sidebar and rows |

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

On top of that, `components/accessibility.test.tsx` runs **axe-core** (through
`jest-axe` 11.0.0) over the dialog, the sidebar and a reminder row in the states
that matter: a populated list, each tab in turn, a validation error showing, a
row with its full set of action buttons, and buttons disabled mid-action. The
full default ruleset is used — nothing is disabled and the ruleset is not
lowered — and it reports no violations.

One defect was found while adding those tests, and axe does not catch it. All
three sidebar tabs carried `aria-controls`, but only the selected tab's panel is
ever rendered, so two of them always pointed at an element that is not in the
document. `aria-valid-attr-value` passes that. The guard is a check in the same
file that walks `aria-controls`, `aria-labelledby`, `aria-describedby` and
`aria-owns` and asserts every id resolves; reverting the fix fails that test and
nothing else.

**What the axe tests do not cover**, which matters more than the count:

- **Colour contrast, not at all.** `jest-axe` disables axe's whole `cat.color`
  group in jsdom, and forcing it on only produces `incomplete` results. There is
  a second reason it could not work anyway: jest maps `*.scss` to
  `identity-obj-proxy`, so no stylesheet and no theme variable is ever loaded.
  The contrast figures in this document come from measurement in a real browser,
  not from these tests.
- **Anything axe files as `incomplete`** — focus visibility, overlap, scrollable
  regions — passes silently, because the matcher only fails on violations.
- **Page-level rules**, such as landmark structure, do not run at all when the
  context is a component rather than a document.
- **Keyboard behaviour**, which axe has no rule for. Two things are known to be
  unverified and are not merely untested: the tab list has no arrow-key
  navigation or roving tabindex, so all three tabs sit in the tab sequence
  rather than following the ARIA pattern; and the dialog has no focus trap and
  does not restore focus on close. Only initial focus is asserted.
- **A screen reader.** The suite now proves the markup exposes correct roles,
  names and relationships. It says nothing about what VoiceOver, NVDA or JAWS
  announce as focus moves, or whether live-region updates are spoken when they
  should be.

**Theme compatibility, measured** — on 26 October 2026 the rendered contrast
of every text element was measured in the browser against the Onyx dark theme,
flattening alpha onto the real background rather than trusting the declared
colour:

| Element | Ratio | WCAG AA (4.5 normal, 3.0 UI) |
|:--|--:|:--|
| Reminder text, tab labels, inputs, dialog fields | 13.57 | pass |
| Summary line | 8.11 | pass |
| Inactive tab | 7.57 | pass |
| Due time, repeat rule, character count | 6.25 | pass |
| Cancel (danger) | 5.21 | pass |
| **Primary button ("Add", "Save changes")** | **3.93** | fails AA for normal text, passes for UI components |

The primary button is worth explaining rather than hiding. It uses Mattermost's
own `--button-bg` with `--button-color`, and the product's own primary buttons
measure **exactly 3.93 in the same theme** — "Invite others to the workspace"
was sampled alongside it for comparison. Picking a different colour would make
the plugin's buttons fail to match every other button in the product, which is
the opposite of the goal. The contrast characteristic belongs to the Mattermost
theme, and that is where it would have to be fixed.

**Theme compatibility, statically** — the stylesheet is scanned directly: no hard-coded hex
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
- `make test-integration` — 7 integration tests against Mattermost 12.0.1 and
  again against 9.5, both Team Edition, all passing, including end-to-end
  reminder delivery

### Integration — 7 tests, all passing against a real server

Executed on 26 October 2026 against two versions of **Mattermost Team Edition**
— the community build — started in Docker by testcontainers:

| Version | Image | Result | `server` package |
|:--|:--|:--|--:|
| 12.0.1 (latest) | `mattermost-team-edition:latest` | 7/7 passing | 141s |
| 9.5 (declared minimum) | `mattermost-team-edition:9.5` | 7/7 passing | 108s |

Both ends of the supported range are therefore measured rather than inferred.
Versions in between — the 10.x and 11.x lines — have not been run.

**CI runs this on every push and pull request**, as a matrix over the same two
versions, so a change that breaks activation or delivery is caught in the pull
request rather than at release time. The two versions report independently
(`fail-fast: false`), because knowing whether a failure is specific to one of
them is most of the diagnosis.

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

### Two real nodes — passing against one Postgres

Run with `make test-cluster`, and in CI on every push. Two Mattermost servers
share one database and one filestore, each starts its own copy of the plugin,
and reminders are created through one node while both schedulers run on their
own timers. Executed on 26 October 2026 against Team Edition latest:

| Check | Result |
|:--|:--|
| Both nodes activate their own plugin process | passing |
| Six reminders due at once, each delivered exactly once | passing |
| A reminder created on one node arrives exactly once | passing |
| The server asks a push proxy to notify, carrying the reminder text | passing |

Be precise about what this adds over the in-process cluster tests, because the
two cover different halves:

- **This test** proves the real deployment shape works: separate server
  processes, compare-and-set against Postgres rather than an in-memory store,
  real timers, and no duplicate delivery. In the run above, node 1 held the
  cluster lock throughout and node 2 never swept — which is the lock behaving
  correctly, and it means this test did **not** exercise the per-reminder
  claim under contention.
- **The in-process tests** (`server/cluster_test.go`) cover exactly that: they
  deliberately bypass the lock and sweep on several instances at the same
  instant, because the lock has a time-to-live and a node that stalls past it
  keeps running without it. That window is what per-reminder claiming exists
  for.

Neither is a substitute for the other, and together they are still not a
licensed HA deployment.

**Still not verified:**

- **A licensed HA deployment.** Two real nodes are tested — see the section
  above — but with clustering itself switched off, because Mattermost gates it
  behind an Enterprise licence.

  The test for it is written and waiting: `TestLicensedHACluster` installs a
  licence, turns `ClusterSettings` on, restarts both nodes, waits for them to
  join over the gossip bus, refuses to continue unless the cluster actually
  formed, and then applies the same exactly-once assertion as the unlicensed
  run. Supply a licence to run it:

  ```bash
  cp your-licence.txt dev/cluster/license.txt   # gitignored
  make test-cluster                             # or MM_LICENSE_FILE=/path/to/it
  ```

  Without one it skips and says so. **Be precise about what that means:** the
  path up to the licence gate is exercised — the cluster starts, both nodes
  activate the plugin, the admin logs in, and an invalid licence is rejected in
  about thirty seconds with Mattermost's own "Invalid license file." Everything
  after a *valid* licence — enabling clustering, the gossip bus, membership —
  has never run. The expected result is no change, because the plugin
  coordinates through compare-and-set against the shared database and never
  touches the bus, but that is a belief and not a measurement.
- **Push on Android, and real delivery through Apple or Google.** The push
  *decision* is no longer a manual check. The cluster suite stands a recorder up
  in the test process, points `PushNotificationServer` at it, attaches a device
  to the session and lets a reminder come due; it then asserts a push arrived
  carrying the reminder's own text. A captured payload looks like this:

  ```
  POST /api/v1/send_push
  message:  ":alarm_clock: Reminder\npush through the proxy"
  platform: apple_rn     channel_name: @followups
  type:     message      category: CAN_REPLY      badge: 1
  ```

  That covers the decision, the routing and the contents — everything up to the
  proxy. What it cannot cover is what Apple or Google do next. That part was
  confirmed by hand once, on an iOS device on 26 October 2026, through
  Mattermost's test proxy with `PushNotificationContents: full`. Android has
  never been tried, and a real deployment uses its own proxy.

  One trap worth knowing if you write a test like this: the server's device-id
  allowlist accepts only the React Native platform prefixes — `apple_rn`,
  `apple_rnbeta`, `android_rn`. A plain `apple:` id is refused with "Invalid or
  missing device_id in request body".

  Note for anyone repeating this: the server **does not log successful pushes**
  at debug level. `notification_push.go` logs refusals as
  `"type":"push","status":"not_sent"` with a reason, but a success is logged at
  `MlvlNotificationTrace`, which is off by default. An empty log is not evidence
  that nothing was sent — the only reliable confirmation is the device itself.
  A `session_expired` reason means the device is attached to a stale session;
  log out and back in on the device to mint a fresh one.
- Screen readers. The component tests assert roles and accessible names, which
  is not the same as using one.

## A lesson about clocks

The scheduling tests used to seed their fake clock from `time.Now()`. That looks
harmless and is not: the scheduler works in hourly buckets, so whether a test
crosses an hour boundary depended on the time of day it ran.

A real bug lived in that gap for the life of the project. A worker dying in the
last five minutes of an hour stranded its reminder permanently, and the test
written to catch exactly that — `TestClusterOneNodeFailingDoesNotStrandAReminder`
— passed every local run and every CI run until one started at 05:28 UTC.

Tests now start from `testClockBase()`, a fixed five minutes past the hour, and
anything that cares about the boundary says so with `newTestPluginAt`. If you
write a scheduling test, decide where in the hour it sits and state it.

One more habit worth copying: when a test is meant to catch a specific bug,
break the fix and watch it fail. A retry-across-the-boundary test was written
alongside the fix and deleted again, because it passed whether or not the bug
was present — and a test that cannot fail is worse than no test, since it
advertises coverage that is not there.

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
