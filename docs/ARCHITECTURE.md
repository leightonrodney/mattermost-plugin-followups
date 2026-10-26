# Architecture

## The shape of the problem

A reminder plugin has to answer one question efficiently, many times a minute:
*which reminders are due now?* The Mattermost plugin key-value store makes that
harder than it sounds, because it offers:

- `Get`, `Set` and `Delete` by key
- an atomic compare-and-set on a single key
- `ListKeys`, which pages through **every** key the plugin owns and filters
  client-side

and nothing else. No transactions, no secondary indexes, no range scans, no
queries. Two design consequences follow, and they shape everything below:

1. **Anything that must change together has to live under one key** and be
   written with compare-and-set. There is no other way to make a multi-value
   change atomic.
2. **The scheduler must never scan users.** `ListKeys` is O(all keys), so using
   it on every tick would make the plugin's cost grow with the size of the
   server rather than with the number of reminders actually due.

## Storage

Three kinds of record, all under a `v1:` prefix that carries the schema version.

### `v1:u:<userID>` — one aggregate per user

```json
{
  "schema_version": 1,
  "reminders": {
    "<reminderID>": { "id": "...", "user_id": "...", "message": "...", ... }
  }
}
```

All of a user's reminders live in one value. That gives three things at once:

- **Atomic everything.** Creating a reminder, enforcing the per-user quota,
  editing, cancelling and claiming for delivery are each a single
  compare-and-set. Two concurrent creations against a limit of five cannot both
  see room for one more, because the count is taken inside the same
  compare-and-set that writes the result.
- **Listing in one read.** The sidebar and `/remind list` cost one `Get`.
- **Isolation by construction.** The key is derived from the user ID, so there
  is no code path that reads one person's reminders while serving another. A
  reminder ID that belongs to somebody else is simply absent from the record
  being read, which is why the API cannot be used to probe for IDs.

The cost is a bound on size. A user's record is capped at 1 MiB, reminder text
at 500 characters, active reminders at the configured quota (100 by default) and
retained history at 100 entries. In practice a heavy user's record is a few tens
of kilobytes.

Fields on a reminder: `id`, `user_id`, `message`, `timezone`, `create_at`,
`update_at`, `due_at`, `recurrence`, `channel_id`, `post_id`, `team_id`,
`status`, `attempts`, `next_attempt_at`, `claimed_at`, `last_delivered_at`,
`occurrences`, `snoozed_from`, `schema_version`.

Note what is *not* stored: the text of the message a reminder was created from.
Only the IDs are kept, and the permalink is rebuilt on each read, after
re-checking that the user may still read that channel.

### `v1:d:<YYYYMMDDHH>:<shard>` — the due index

```json
[{"u": "<userID>", "r": "<reminderID>"}, ...]
```

One bucket per UTC hour, split across 8 shards chosen by hashing the user ID.
When a reminder is scheduled for some instant, an entry is added to the bucket
for that hour.

- **Sharding** spreads the compare-and-set contention of many people scheduling
  reminders for the same popular hour — 9am on a Monday — across eight keys
  instead of one.
- **Buckets expire** 90 days after their hour, so the scheduler never has to
  clean up after itself.
- **Indexing is idempotent.** Adding an entry that is already present is a
  no-op, so a retried write cannot give the scheduler two chances to deliver.

### `v1:cursor` — how far the scheduler has swept

```json
{"last_completed_hour": "2026-10-25T21:00:00Z"}
```

Persisted, so a restarted plugin resumes from the hour it last finished rather
than losing whatever came due while it was down.

## The scheduler

`cluster.Schedule` from the plugin SDK runs the job on a timer and holds a
cluster-wide lock for the duration of each run, so at most one node is inside
the sweep at a time. The interval is read from the configuration at the top of
each wait, which is why changing it does not need a restart.

Each run:

1. Reads the cursor. If there is none — a fresh install, or a cursor that was
   lost — it starts 48 hours back, which covers an outage without making a new
   installation read thousands of empty buckets.
2. If the cursor is older than the 90-day bucket retention, it jumps forward and
   logs a warning naming how many hours were skipped. Those entries no longer
   exist, so walking them would be pointless work.
3. Walks the hour buckets from the cursor to the current hour, at most 500 per
   run. A plugin that has been off for weeks catches up over several runs
   instead of holding the cluster lock for a long time.
4. For each entry, runs the delivery handshake below.
5. Advances the cursor past each *completed* hour — but only while every entry
   in it has settled. The current hour is never marked finished, because more
   entries can still land in it; it is simply re-read next run, and
   per-reminder claiming is what stops that re-read delivering anything twice.

   An hour holding anything unsettled is not finished either, and the cursor
   stops there for the rest of the run. This is not a refinement; it is load
   bearing. A worker that dies holding a claim leaves its reminder in
   `delivering`, and the only thing that ever brings the scheduler back to it
   is the cursor not having passed its bucket. Without that hold, a crash in
   the last five minutes of an hour stranded the reminder permanently — the
   claim went stale only after the hour had rolled over, by which point the
   bucket was behind the cursor and was never read again. The index retention
   window bounds how long the cursor can be held: once a bucket expires there
   is nothing left in it to be unsettled.

   A failed delivery does not rely on this. `Failed` re-indexes the reminder at
   its retry time, so it lands in a future bucket and is found by an ordinary
   sweep. `Claim` has no equivalent, which is why the stalled claim was the one
   path that could be lost.

Steady-state cost is 8 key reads per tick — one per shard of the current hour —
regardless of how many users the server has.

### Why the cursor is enough for restart recovery

Nothing is held in memory between runs. The reminder records, the index and the
cursor are all persisted, so a plugin that is restarted, redeployed or moved to
another node resumes exactly where it stopped. There is a test for this: it
rebuilds the plugin against the same storage and checks that the reminder is
still delivered.

## Delivery

Three steps, so that two workers — or one worker running twice after a crash —
cannot normally deliver the same occurrence twice:

1. **Claim.** A compare-and-set moves the reminder from `scheduled` to
   `delivering`, stamps `claimed_at` and increments `attempts`. Exactly one
   caller can win; everyone else is told to move on.
2. **Post.** A direct message from the Follow-ups bot.
3. **Settle.** A compare-and-set moves it out of `delivering`: to `delivered`
   for a one-off, or back to `scheduled` at the next occurrence for a repeating
   one. A failure instead schedules a backoff retry (1, then 5, then 15
   minutes), and after three attempts marks it `failed`.

A worker that dies between steps 1 and 3 leaves the reminder claimed. After a
five-minute claim timeout another worker reclaims it and tries again.

### The delivery guarantee, precisely

**At-least-once.** There are exactly two windows in which a reminder can be
delivered twice:

- A worker posts the message and then dies before recording it. Five minutes
  later another worker reclaims the reminder and posts it again.
- The cluster lock expires mid-run — it has a 15-second time-to-live and is
  refreshed every 7.5 seconds — and a second node starts sweeping the same
  hour. Per-reminder claiming closes this one in the normal case: the second
  node loses the compare-and-set and skips. It only becomes a duplicate if the
  first node has also stalled past its claim timeout.

**This is not exactly-once, and it is not claimed to be.** Exactly-once would
require the post and the state change to commit together. The plugin cannot do
that: posting goes through the Mattermost API and the state change goes to the
key-value store, with no transaction spanning both. The choice is therefore
between losing a reminder occasionally (mark first, post second) and repeating
one occasionally (post first, mark second). Repeating one is the better failure
for a reminder, so that is what this does.

## Recurrence

Two families, kept deliberately apart because they mean different things:

- **Wall-clock rules** — daily, weekly, monthly — pin an hour and minute in the
  user's timezone. The real elapsed interval stretches or shrinks across a
  daylight-saving transition, which is what "every weekday at 9am" means.
- **Interval rules** — every N minutes or hours — add a fixed duration to the
  previous firing, so they are unaffected by daylight saving.

Both anchor on the *previous* firing rather than on the moment the scheduler
happened to wake, so an every-three-days series keeps its own days even after
the scheduler has to catch up over a gap.

### Daylight saving

Two wall-clock times are awkward, and both are resolved explicitly:

- **A time that does not exist.** When the clocks jump from 02:00 to 03:00,
  02:30 never happens. Go's `time.Date` resolves it *backwards*, to the
  equivalent instant before the jump — which is earlier than the previous day's
  firing, so a naive implementation silently skips that day entirely. Instead
  the plugin detects the normalisation and rebuilds the instant from the offset
  on the far side of the jump. The reminder fires once, shortly after the
  clocks move, a whole number of days on from the last one.
- **A time that happens twice.** When the clocks fall back, 01:30 happens twice.
  The first of the two is used, so the reminder fires once rather than twice.

Both cases have tests, for a zone with both transitions.

### Short months and missed runs

- **Day 31 in a 30-day month** clamps to the last day, so "every month on the
  31st" still fires in February — on the 28th, or the 29th in a leap year.
- **Missed occurrences are skipped, not replayed.** A recurring reminder is a
  standing instruction, not a backlog. A daily reminder missed for two weeks
  fires once when the server returns. The arithmetic jumps whole intervals
  rather than looping, so a long outage costs a division, not one iteration per
  missed day.

## Security boundaries

| Boundary | How it is enforced |
|:--|:--|
| Authentication | Every API route sits behind middleware requiring the `Mattermost-User-ID` header, which the server sets only after authenticating and strips from client input. |
| Ownership | Every operation reads and writes only `v1:u:<requesting user>`. Another user's reminder ID returns the same "not found" as a nonexistent one. |
| Interactive buttons | The acting user comes from the authenticated request, never from the button's own context payload. |
| Channel access | Checked when a message link is stored, and again when a reminder is delivered or listed, so revoked access removes the permalink. |
| Input | Message length, recurrence rules, timezone names and timestamps are all validated server-side. Request bodies are capped at 64 KiB and reject unknown fields. |
| Output | Only errors explicitly marked as user-facing are shown to users. Everything else is logged and replaced with a generic message, so internal details cannot leak into a channel. |
| Logging | Reminder text and message contents are never logged, at any level. |
| Quotas | Per-user limits on active and recurring reminders, a minimum interval, a maximum horizon, and a hard 1 MiB ceiling per user record. |

## Request and data flow

```
/remind me every weekday at 9am to post the update
        │
        ▼
ExecuteCommand ──► parser ──► service.Create
                                 │
                                 ├─► store.Index   (v1:d:<hour>:<shard>)   written first:
                                 │                                          an orphan index
                                 │                                          entry is harmless,
                                 │                                          an unindexed
                                 │                                          reminder never fires
                                 └─► store.UpdateUser (v1:u:<userID>)  CAS, quota checked inside

        ... time passes ...

cluster.Schedule (one node at a time)
        │
        ▼
runScheduler ──► store.Cursor ──► store.DueIn(hour) ──► for each entry:
                                                           service.Claim    (CAS)
                                                           postReminder     (bot DM)
                                                           service.Delivered(CAS, re-index next)
                                      │
                                      └─► store.SetCursor
```

## Schema migration

Every record carries `schema_version`, and every key is prefixed `v1:`. A future
format introduces a `v2:` prefix and migrates lazily: a read that finds only a
`v1:` record converts it, writes the `v2:` record and continues. That keeps the
migration proportional to use rather than requiring a sweep of every user, which
the key-value store cannot do efficiently.

There is only one schema version today, so no migration code exists yet. The
version field exists so that adding one does not require a flag day.
