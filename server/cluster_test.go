package main

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
)

// Several plugin instances sharing one key-value store is what a Mattermost
// cluster looks like from this plugin's point of view. Each node has its own
// process memory; the database is the only thing between them.
//
// In production cluster.Schedule holds a cluster-wide lock, so two nodes should
// not sweep at once. These tests deliberately ignore that and run the sweeps
// concurrently anyway, because the lock has a 15-second time-to-live and a node
// that stalls past it loses the lock while still running. Per-reminder
// compare-and-set claiming is the backstop for exactly that window, and it is
// the thing worth testing.
//
// Worth knowing why this needs no Enterprise licence: cluster.Mutex depends on
// KVSetWithOptions and nothing else. The coordination is pure compare-and-set
// against the shared database, with no cluster bus involved.

// runPeersConcurrently sweeps on every node at the same instant and waits for
// them all.
func runPeersConcurrently(peers []*Plugin, now time.Time) {
	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Go(func() {
			peer.runSchedulerAt(now)
		})
	}
	wg.Wait()
}

func TestClusterDeliversAReminderOnlyOnce(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "exactly once please")

	peers := []*Plugin{tp.Plugin, tp.newPeer(), tp.newPeer(), tp.newPeer()}
	runPeersConcurrently(peers, tp.clock.Now().Add(31*time.Minute))

	assert.Len(t, tp.deliveredPosts(), 1,
		"four nodes swept the same hour at the same moment; only one may deliver")
}

func TestClusterDeliversEveryReminderExactlyOnce(t *testing.T) {
	tp := newTestPlugin(t)

	// Several reminders across several users, so the nodes genuinely contend
	// rather than each happening to pick a different one.
	const users, perUser = 4, 5
	for u := range users {
		for i := range perUser {
			_, err := tp.service.Create(userIDForIndex(u), createInput{
				Message:  "reminder",
				DueAt:    tp.clock.Now().Add(time.Duration(30+i) * time.Minute),
				Timezone: "UTC",
			})
			require.NoError(t, err)
		}
	}

	peers := []*Plugin{tp.Plugin, tp.newPeer(), tp.newPeer()}
	runPeersConcurrently(peers, tp.clock.Now().Add(2*time.Hour))

	assert.Len(t, tp.deliveredPosts(), users*perUser,
		"every reminder must be delivered, and none of them twice")
}

func TestClusterAdvancesARecurringSeriesOnce(t *testing.T) {
	tp := newTestPlugin(t)

	rec, err := tp.service.Create(testUserID, createInput{
		Message:    "stand up",
		DueAt:      tp.clock.Now().Add(30 * time.Minute),
		Timezone:   "UTC",
		Recurrence: &reminder.Recurrence{Type: reminder.RecurrenceInterval, EverySeconds: 3600},
	})
	require.NoError(t, err)

	peers := []*Plugin{tp.Plugin, tp.newPeer(), tp.newPeer()}
	runPeersConcurrently(peers, tp.clock.Now().Add(31*time.Minute))

	require.Len(t, tp.deliveredPosts(), 1, "one occurrence, one message")

	after, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, after.Occurrences, "the series must advance exactly one step")
	assert.Equal(t, reminder.StatusScheduled, after.Status)
}

func TestClusterSharesTheCursor(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "swept once")

	first := tp.newPeer()
	at := tp.clock.Now().Add(31 * time.Minute)
	first.runSchedulerAt(at)
	require.Len(t, tp.deliveredPosts(), 1)

	// A different node sweeping afterwards reads the cursor the first one
	// advanced, so it has nothing left to do rather than starting over.
	second := tp.newPeer()
	second.runSchedulerAt(at)

	assert.Len(t, tp.deliveredPosts(), 1,
		"the cursor lives in the shared store, so a second node must not re-sweep")
}

func TestClusterOneNodeFailingDoesNotStrandAReminder(t *testing.T) {
	tp := newTestPlugin(t)
	rec := tp.createDue(t, testUserID, 30*time.Minute, "picked up by somebody")

	// One node claims the reminder and then dies before delivering it, which
	// leaves it stuck in delivering.
	at := tp.clock.Now().Add(31 * time.Minute)
	dying := tp.newPeer()
	claimed, err := dying.service.Claim(testUserID, rec.ID, at)
	require.NoError(t, err)
	require.Equal(t, reminder.StatusDelivering, claimed.Status)

	// While the claim is still fresh, nobody else touches it.
	healthy := tp.newPeer()
	healthy.runSchedulerAt(at.Add(time.Minute))
	assert.Empty(t, tp.deliveredPosts(), "a live claim must be respected")

	// Once it has gone stale, another node picks it up.
	healthy.runSchedulerAt(at.Add(reminder.ClaimTimeout + time.Minute))
	assert.Len(t, tp.deliveredPosts(), 1, "a stalled claim must be reclaimed, not stranded")
}

// userIDForIndex builds distinct 26-character user IDs for the contention test.
func userIDForIndex(i int) string {
	return fmt.Sprintf("clusteruser%02d00000000000000", i)
}
