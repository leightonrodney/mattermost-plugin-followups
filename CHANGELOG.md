# Changelog

All notable changes to this project are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project uses [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

## [1.0.0] - 2026-10-26

First stable release.

An earlier 0.1.0 was published and then withdrawn: two defects were found while
verifying it, one of them serious enough that no one should be running it. Both
are fixed here. Nothing was released in between.


### Added

- **`/remind` slash command** with a deterministic, documented grammar for
  times: relative offsets (`in 30 minutes`, `in an hour`, `in 2 weeks`), named
  days (`today`, `tonight`, `tomorrow`), weekdays (`Friday at 2pm`,
  `next Tuesday`), dates (`2026-11-03 at 14:30`, `Nov 3`, `3 November`) and
  clock times (`at 9am`, `at noon`). No external service is used.
- **Recurring reminders**: `every day`, `every weekday`, `every weekend`,
  `every Monday and Friday`, `every other Monday`, `every 3 days`,
  `every month on the 15th`, `every 2 hours`.
- **Reminder management** from the command: `list`, `snooze`, `cancel`,
  `delete`, `help`.
- **"Remind me" on any message**, from the post's **Actions** menu. The reminder keeps
  a link to the message; the message text is not copied into storage.
- **A sidebar**, opened from the channel header, with Upcoming, Repeating and
  Done tabs, a quick-add box that takes the same phrases as the command, and
  edit, snooze and cancel on every row.
- **Delivery as a direct message** from a dedicated Follow-ups bot, so reminders
  follow existing notification settings and reach mobile clients.
- **Snooze and dismiss buttons** on delivered reminders, including a "tomorrow
  morning" preset resolved in the user's own timezone.
- **Administrator settings** for enabling delivery, the scheduler interval,
  per-user limits on reminders and recurring reminders, the minimum scheduling
  interval, the maximum horizon, retention of finished reminders, and debug
  logging. All take effect immediately except the scheduler interval, which
  applies from the next tick.
- **A plugin HTTP API** for the webapp, with every route behind authentication.

### Notes on behaviour

- **Daylight saving.** Wall-clock rules keep their hour across a transition;
  interval rules keep their elapsed length. A wall-clock time that a
  spring-forward jump skips still fires once that day rather than being dropped,
  and one that occurs twice on fall-back fires once.
- **Short months.** A monthly reminder on the 31st fires on the last day of
  months that do not have one.
- **Missed occurrences are skipped, not replayed.** A daily reminder missed for
  a fortnight fires once when the server comes back.
- **Delivery is at-least-once.** A worker that dies between posting a reminder
  and recording it will deliver that occurrence again after a five-minute claim
  timeout. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- **Restart recovery** is driven by a persisted cursor over hourly index
  buckets. A plugin disabled for more than 90 days loses the index for that
  window; those reminders remain visible as overdue and can be rescheduled.

### Verified

- The integration suite installs the plugin into a real Mattermost server and
  passes all of its tests, including one that waits for a reminder to come due
  and confirms it arrives as a direct message. Run on every push against both
  ends of the supported range: 12.0.1 and 9.5, Team Edition.
- **Two real nodes against one Postgres**, each running its own plugin process,
  with reminders due at the same moment arriving exactly once. Runs in CI.
- **The push request itself**, against a recorder standing in for the push
  proxy: the server asks for a push and the payload carries the reminder's own
  text. Runs in CI.
- A reminder arriving as a push notification on a locked iPhone, showing its own
  text rather than a generic placeholder. Confirmed by hand, once.
- axe-core over the dialog, the sidebar and a reminder row, with no violations.
- **A licensed two-node cluster**, once, on Enterprise Edition 12.0.1: the
  licence installed, clustering came on, the two nodes joined one cluster, and
  reminders due at the same moment still arrived exactly once. The trial licence
  behind that expires, so CI runs the unlicensed equivalent and skips this.

### Known gaps

- **Not tested at scale.** A two-node licensed cluster *is* tested — Enterprise
  Edition 12.0.1 with clustering enabled, both nodes joining over the gossip
  bus, delivery unchanged — and the unlicensed two-node equivalent runs in CI.
  What no test here reaches is more than two nodes, or the 60k concurrent
  connections the Marketplace asks about; there is no public tool for that.
- **Not run against the 10.x and 11.x lines**, only 9.5 and latest.
- **Push is confirmed on iOS only**, by hand, through Mattermost's test proxy.
  Android is untried, and real delivery through Apple or Google is outside what
  any test here can reach.
- **No screen reader.** The markup is checked; what a screen reader announces is
  not. See [docs/TESTING.md](docs/TESTING.md).

### Fixed before this release

- **A reminder could be stranded and never delivered.** If the worker handling a
  reminder died between claiming it and posting it, the claim went stale after
  five minutes — and if that moment fell after the hour rolled over, the
  scheduler's cursor had already moved past the bucket and nothing read it
  again. A crash in roughly the last five minutes of any hour lost that
  reminder for good. The scheduler now holds its cursor on any hour still
  holding an unsettled reminder. This was the one defect that broke a documented
  guarantee, and it was found by CI rather than by review: the test written for
  exactly this case seeded its clock from the wall clock, so it only crossed an
  hour boundary when the suite happened to run in the last minutes of an hour.
- **The sidebar's tabs referred to panels that were not there.** All three
  carried `aria-controls`, but only the selected tab's panel is rendered, so two
  always pointed at an absent element id. axe-core does not flag this; a
  reference check added alongside the fix does.

[Unreleased]: https://github.com/leightonrodney/mattermost-plugin-followups/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/leightonrodney/mattermost-plugin-followups/releases/tag/v1.0.0
