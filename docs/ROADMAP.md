# Recommended improvements for v0.2.0

Ordered by what would most improve the plugin, not by how easy each is. Nothing
here is required for v0.1.0 to be useful; these are the things that were
consciously left out.

## Verification first

These are not features, and they come before any of them. v0.1.0 ships with
gaps in what has actually been *run*, and closing them is the highest-value
work available.

1. **Run the integration suite.** `server/integration_test.go` is written and
   compiles, but has never been executed; it needs Docker. Until it runs, the
   claims that the plugin installs, activates, registers its command and
   delivers a real reminder rest on unit tests alone.
2. **Test against a real cluster.** The design uses `cluster.Schedule` plus
   per-reminder compare-and-set claiming precisely for the multi-node case, and
   the concurrency is covered in-process, but two real nodes have never run it.
   Worth doing before anyone deploys it at scale.
3. **Test against the oldest supported server.** `min_server_version` is 9.5.0,
   derived from when each API became available rather than from testing. Pin the
   integration suite's image to that version and confirm.
4. **Confirm mobile delivery.** Reminders are ordinary direct messages, so push
   should work, but it has not been seen on a device.
5. **Capture screenshots.** Needed for the Marketplace listing, and the README
   is weaker without them.

## Features

### Reminders for other people and for channels

The most requested thing the archived community plugin did that this does not:
`/remind @alice in an hour to review the PR`, and `/remind ~channel every Monday
at 9am to post the standup`.

It is deliberately absent from v0.1.0 because it changes the security model
completely. Today every read and write is scoped to the requesting user, which
is what makes cross-user exposure structurally impossible. Reminding someone
else means writing into their data, which needs: a rule about who may remind
whom, the reminder showing who set it, a way to decline or mute, and a channel
reminder being attributed and manageable by more than one person. That is a
design task before it is an implementation task.

### Time zone handling for people who travel

Reminders are computed in the timezone the reminder was created in. Someone who
changes their Mattermost timezone keeps their old reminders on the old zone,
which is defensible but surprising. Offer the choice at creation ("9am wherever
I am" against "9am in London"), and prompt when a timezone change would move
existing reminders.

### An end date for recurring reminders

`every Monday at 10am until 15 December`, or `every day for 10 days`. The
recurrence model has room for it; the parser and the UI do not.

### Natural-language editing

`/remind reschedule <id> to tomorrow at 2pm`. Today rescheduling means the
sidebar or cancelling and re-creating.

### Snooze presets the user chooses

The four presets on a delivered reminder are fixed. They should be a per-user
preference, via `registerUserSettings`.

### App Bar icon

`registerAppBarComponent` would give the sidebar a permanent home in the App Bar
rather than only the channel header. It needs an icon served as a URL rather
than a React element, which means publishing an asset from `public/`. Small, and
it makes the plugin feel more native.

### Localisation

Everything is in English. `webapp/i18n/` and `registerTranslations` are already
wired up by the starter template but unused, and the server's command responses
would need `pluginapi/i18n`. Worth doing before the Marketplace, where the
audience is not all English-speaking.

## Technical work

### Lazy migration machinery

Records carry `schema_version` and keys carry a `v1:` prefix, so the design is
ready for a migration, but no migration code exists. Write it — with a test —
before the first schema change rather than during it.

### Cleaning up after deleted users

This SDK version exposes no hook for user deletion, so a deleted user's record
stays until an administrator removes it. `store.DeleteUser` exists and is
unused. Either find a hook, or add an administrator command to prune records for
users the server no longer has.

### Narrowing the duplicate-delivery window

Delivery is at-least-once, and the window is a worker dying between posting and
recording. It could be narrowed by writing an intent record before posting and
checking for a matching recent post before re-delivering. It cannot be closed —
there is no transaction spanning the post and the key-value write — so this is
about making an uncommon event rarer, not about reaching exactly-once. Measure
first; it may not be worth the complexity.

### Metrics

`registerSiteStatisticsHandler` could surface reminders scheduled, delivered and
failed in the System Console. Useful to an administrator deciding whether the
plugin is behaving.

### Bundle size

37 MB, which is five Go binaries at roughly 20 MB each. Most of it is the
Mattermost SDK, and symbols are already stripped. Dropping platforms nobody uses
would help, but guessing wrong is worse than the download. Revisit if anyone
complains.

### A shared format for reminder rendering

The slash command, the delivered post and the sidebar each format a reminder
separately. They agree today because the recurrence description comes from one
place on the server, but the date formatting does not. Worth consolidating
before a fourth consumer appears.
