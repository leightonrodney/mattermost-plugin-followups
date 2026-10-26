# Changelog

All notable changes to this project are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project uses [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

## [0.1.0] - 2026-10-25

First release.

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
  passes all seven of its tests, including one that waits for a reminder to come
  due and confirms it arrives as a direct message. Run against both ends of the
  supported range: 12.0.1 and 9.5, Team Edition.
- A reminder arriving as a push notification on a locked iPhone, showing its own
  text rather than a generic placeholder.

### Known gaps

- Not run on a multi-node cluster, nor against the 10.x and 11.x lines in
  between. See [docs/TESTING.md](docs/TESTING.md).
- Mobile push has been confirmed on iOS but not Android, and through
  Mattermost's test push proxy rather than a production one.

[Unreleased]: https://github.com/leightonrodney/mattermost-plugin-followups/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/leightonrodney/mattermost-plugin-followups/releases/tag/v0.1.0
