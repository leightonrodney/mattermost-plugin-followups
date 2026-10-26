# Recommended improvements for v0.2.0

Ordered by what would most improve the plugin, not by how easy each is. Nothing
here is required for v0.1.0 to be useful; these are the things that were
consciously left out.

## Verification first

These are not features, and they come before any of them. Most of the gaps
v0.1.0 started with have since been closed; what is left is listed honestly
below, because an unverified claim is worse than an absent feature.

1. ~~**Run the integration suite.**~~ Done. It runs in CI on every push, against
   Mattermost 9.5 and latest, and covers install, activation, command
   registration, a real scheduler tick delivering a reminder, and a
   disable/wait/re-enable cycle that only passes if the schedule survived in
   the database.
2. **Test against a licensed HA cluster.** Mostly done. Two real Mattermost
   servers against one Postgres now run in CI, each with its own plugin
   process, and reminders due at the same moment arrive exactly once;
   contention on the per-reminder claim is covered in-process, where the
   cluster lock can be bypassed deliberately. What remains is a licensed
   deployment with a cluster bus, which needs an Enterprise licence. The plugin
   does not use the bus, so the expected result is "no change" — which is
   worth confirming rather than assuming before anyone runs it at scale.
3. ~~**Test against the oldest supported server.**~~ Done. `min_server_version`
   is 9.5.0, and the integration suite runs against that image in CI alongside
   latest, so the claim is tested rather than derived from API availability.
4. ~~**Confirm mobile delivery.**~~ Done on iOS, 26 October 2026, and the
   server-side half is now automated: a recorder stands in for the push proxy
   and the suite asserts the push carries the reminder's text. Android is still
   untried, as is delivery through a production proxy.
5. ~~**Capture screenshots.**~~ Done — see `docs/images/`. Taken from a real
   installation on Mattermost 12.0.1 Team Edition.

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
