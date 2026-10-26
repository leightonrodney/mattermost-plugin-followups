package main

import (
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Whether a reminder reaches somebody's phone is the server's decision, taken
// from that person's notification settings. This plugin cannot make a push
// happen and does not try to.
//
// What it is responsible for is narrower and entirely checkable: delivering a
// real, persisted direct message from the bot, of a kind the server will
// consider for notification. Each assertion below corresponds to a way that
// could quietly stop being true while every other test in the suite still
// passed, because none of them look at how the post was made.

func TestDeliveryIsAPersistedPostNotAnEphemeralOne(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "reach my phone")

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	require.Len(t, tp.deliveredPosts(), 1)
	assert.Empty(t, tp.ephemeralPosts(),
		"an ephemeral post is never stored and never notifies, so it would never reach a phone")
}

func TestDeliveryGoesToADirectChannelFromTheBot(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "reach my phone")

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)

	assert.Equal(t, testBotID, posts[0].UserId, "the reminder must come from the plugin's bot")
	assert.NotEmpty(t, posts[0].ChannelId, "without a channel there is nothing to notify about")
	assert.Empty(t, posts[0].RootId, "a reminder is its own message, not a thread reply nobody follows")
}

func TestDeliveryCarriesNoPostTypeThatWouldSuppressNotification(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "reach my phone")

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)

	// Mattermost treats system_* posts as chrome rather than conversation, and
	// they do not notify. Giving the reminder such a type would be an easy and
	// invisible mistake.
	assert.False(t, strings.HasPrefix(posts[0].Type, model.PostSystemMessagePrefix),
		"a system post type would stop the reminder notifying, got %q", posts[0].Type)
}

func TestDeliveryHasTextToShowInTheNotification(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "water the plants")

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)

	// A push notification shows the message body. Moving the text wholly into
	// an attachment would leave a phone buzzing with a blank notification,
	// because attachments are not part of the push payload.
	assert.NotEmpty(t, strings.TrimSpace(posts[0].Message))
	assert.Contains(t, posts[0].Message, "water the plants",
		"the reminder's own words have to be in the message body, not only in an attachment")
}

func TestDeliveryKeepsTheButtonsOutOfTheMessageBody(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "water the plants")

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)

	attachments, ok := posts[0].GetProp(model.PostPropsAttachments).([]*model.MessageAttachment)
	require.True(t, ok, "the snooze buttons live in an attachment")
	require.Len(t, attachments, 1)
	assert.NotEmpty(t, attachments[0].Actions)

	// The body stays readable on its own, which is what a notification shows.
	assert.NotContains(t, posts[0].Message, "Snooze")
}
