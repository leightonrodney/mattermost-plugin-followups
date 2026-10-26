package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
)

// The scheduler works in hourly buckets and keeps a cursor over them. Anything
// left unfinished when an hour ended used to be abandoned there: the cursor
// moved past, nothing read that bucket again, and the reminder was never
// delivered.
//
// Only one path could reach that state. Claim marks a reminder as delivering
// and nothing more, so a worker that dies holding a claim leaves the entry
// where it was. A failed delivery is different — Failed re-indexes it at its
// retry time, so it lands in a future bucket and is found by an ordinary sweep.
// That asymmetry is why this file has one test and not two: a retry-across-the-
// boundary test was written, and then deleted once it turned out to pass
// whether or not the bug was present.
//
// The test starts at 58 minutes past the hour, because that is the only place
// it fails. Until this was found the test clock came from time.Now(), so the
// boundary was crossed only when the suite happened to run in the last few
// minutes of an hour — which is why CI caught it once, at 05:28 UTC, and no
// local run ever did.

// hourBoundaryBase is late enough in the hour that the next scheduler sweep
// lands in the following one.
func hourBoundaryBase() time.Time {
	return time.Now().UTC().Truncate(time.Hour).Add(58 * time.Minute)
}

func TestAStalledClaimIsReclaimedAcrossAnHourBoundary(t *testing.T) {
	base := hourBoundaryBase()
	tp := newTestPluginAt(t, base)

	rec := tp.createDue(t, testUserID, time.Minute, "must not be stranded")

	// A node claims it at the turn of the hour and dies before posting.
	at := base.Add(2 * time.Minute)
	dying := tp.newPeer()
	claimed, err := dying.service.Claim(testUserID, rec.ID, at)
	require.NoError(t, err)
	require.Equal(t, reminder.StatusDelivering, claimed.Status)

	healthy := tp.newPeer()

	// While the claim is fresh nobody may touch it — and the cursor must not
	// be allowed past the hour that still holds it.
	healthy.runSchedulerAt(at.Add(time.Minute))
	require.Empty(t, tp.deliveredPosts(), "a live claim must be respected")

	// Once stale it has to be found again, although its hour is over.
	healthy.runSchedulerAt(at.Add(reminder.ClaimTimeout + time.Minute))
	assert.Len(t, tp.deliveredPosts(), 1,
		"a claim stalled across an hour boundary must still be reclaimed")

	after, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusDelivered, after.Status)
}
