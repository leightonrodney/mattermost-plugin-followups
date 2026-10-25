package main

import (
	"time"

	"github.com/leightonrodney/mattermost-plugin-followups/server/store"
)

// maxBucketsPerRun bounds how many hour buckets one scheduler run will work
// through. A plugin that has been off for weeks catches up over several runs
// instead of blocking the cluster lock for a long time in one go.
const maxBucketsPerRun = 500

// initialLookback is how far back the scheduler sweeps when it has no cursor to
// resume from. It is a compromise: long enough to cover an outage or a lost
// cursor, short enough that a fresh install does not read thousands of empty
// buckets before it can do anything useful.
const initialLookback = 48 * time.Hour

// runScheduler is the background job. cluster.Schedule guarantees that at most
// one instance in the cluster is inside this function at a time.
//
// The work is driven by a persistent cursor over hourly index buckets rather
// than by a scan of users, so the cost of a run is proportional to the time
// that has passed since the last one, not to how many people use the server.
// The cursor is what makes restart recovery automatic: a plugin that comes back
// after an outage resumes from the hour it last finished.
func (p *Plugin) runScheduler() {
	p.runSchedulerAt(time.Now().UTC())
}

// runSchedulerAt is runScheduler with the current time supplied, so that the
// sweep can be exercised against a controlled clock.
func (p *Plugin) runSchedulerAt(now time.Time) {
	config := p.getConfiguration()
	if !config.EnableScheduler {
		p.logDebug("Scheduler is disabled; skipping run")
		return
	}

	now = now.UTC()
	nowHour := store.BucketHour(now)

	cursor, err := p.store.Cursor()
	if err != nil {
		p.client.Log.Error("Failed to read the scheduler cursor", "error", err.Error())
		return
	}

	if cursor.IsZero() {
		// No cursor yet: either this is a fresh install, or the stored one was
		// lost. Look back a bounded window rather than starting at the current
		// hour, so that reminders which came due while the plugin was off are
		// still delivered. On a fresh install those buckets are simply empty.
		//
		// The loop below then persists the cursor as soon as it passes a
		// completed hour, so this look-back happens once and not on every run.
		cursor = nowHour.Add(-initialLookback)
		p.client.Log.Info("Establishing the scheduler cursor",
			"from", cursor.Format(time.RFC3339))
	}

	// Index buckets expire, so a cursor older than the retention window points
	// at hours whose entries are already gone. Walking them would be pointless
	// work; say so plainly and move on.
	if oldest := nowHour.Add(-store.BucketRetention); cursor.Before(oldest) {
		p.client.Log.Warn(
			"Scheduler was behind by more than the index retention window; reminders due in that period were not delivered",
			"missed_hours", int(oldest.Sub(cursor).Hours()),
			"resuming_from", oldest.Format(time.RFC3339),
		)
		cursor = oldest.Add(-time.Hour)
	}

	delivered, claimed := 0, 0
	touched := map[string]struct{}{}

	for hour, processed := cursor.Add(time.Hour), 0; !hour.After(nowHour); hour = hour.Add(time.Hour) {
		if processed >= maxBucketsPerRun {
			p.logDebug("Scheduler hit its per-run bucket limit; continuing on the next run",
				"next_hour", hour.Format(time.RFC3339))
			break
		}

		entries, err := p.store.DueIn(hour)
		if err != nil {
			// Leaving the cursor where it is means this hour is retried rather
			// than skipped, which is the safe direction to fail in.
			p.client.Log.Error("Failed to read the schedule index", "hour", hour.Format(time.RFC3339), "error", err.Error())
			return
		}

		for _, entry := range entries {
			ok, err := p.deliverEntry(entry, now)
			if err != nil {
				p.client.Log.Error("Failed to deliver a reminder",
					"user_id", entry.UserID, "reminder_id", entry.ReminderID, "error", err.Error())
				continue
			}
			if ok {
				claimed++
				delivered++
				touched[entry.UserID] = struct{}{}
			}
		}

		processed++

		// The current hour is still accumulating entries, so it is never marked
		// finished; it is simply re-read on the next run. Per-reminder claiming
		// is what stops that re-read from delivering anything twice.
		if hour.Before(nowHour) {
			if err := p.store.SetCursor(hour); err != nil {
				p.client.Log.Error("Failed to advance the scheduler cursor", "error", err.Error())
				return
			}
		}
	}

	// Retention is applied to the users this run touched. Someone who neither
	// receives a reminder nor opens the list has a record that is not growing,
	// so there is nothing to trim; the list endpoint purges on their behalf
	// when they do look.
	for userID := range touched {
		if removed, err := p.service.Purge(userID, now); err != nil {
			p.client.Log.Warn("Failed to apply retention", "user_id", userID, "error", err.Error())
		} else if removed > 0 {
			p.logDebug("Purged finished reminders", "user_id", userID, "removed", removed)
		}
	}

	if delivered > 0 {
		p.logDebug("Scheduler run complete", "delivered", delivered, "claimed", claimed)
	}
}

// deliverEntry takes one index entry through the claim, post, settle sequence.
// It reports whether this call was the one that delivered the reminder.
func (p *Plugin) deliverEntry(entry store.DueEntry, now time.Time) (bool, error) {
	claimed, err := p.service.Claim(entry.UserID, entry.ReminderID, now)
	switch {
	case err == errSkip:
		// Already delivered, cancelled, not yet due, or claimed by a worker
		// that is still within its claim window. All ordinary.
		return false, nil
	case err != nil:
		return false, err
	}

	if postErr := p.postReminder(claimed); postErr != nil {
		// The reminder stays claimed only until the service records the
		// failure, which schedules a backoff retry or gives up.
		if _, err := p.service.Failed(entry.UserID, entry.ReminderID, now); err != nil {
			return false, err
		}
		return false, postErr
	}

	if _, err := p.service.Delivered(entry.UserID, entry.ReminderID, now); err != nil {
		// The message is already out. Reporting the bookkeeping failure lets
		// the operator see it; the claim times out and the reminder is retried,
		// which is the at-least-once edge documented in docs/ARCHITECTURE.md.
		return true, err
	}

	return true, nil
}
