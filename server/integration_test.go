//go:build integration

// These tests start a real Mattermost server in a container, install this
// plugin into it, and drive it over HTTP the way a client would. They need
// Docker, so they are kept behind a build tag and are not part of `make test`.
//
//	make test-integration
//
// Run them before a release: they are the only check that covers plugin
// installation, activation, command registration and real delivery, none of
// which the unit tests can reach.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiRequest sends an authenticated request to the plugin's own API.
func apiRequest(t *testing.T, th *testhelper.TestHelper, client *model.Client4, method, path string, body any) (int, []byte) {
	t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		payload = strings.NewReader(string(encoded))
	}

	url := fmt.Sprintf("%s/plugins/%s/api/v1%s", th.ServerURL, testhelper.PluginID(), path)
	request, err := http.NewRequestWithContext(t.Context(), method, url, payload)
	require.NoError(t, err)

	request.Header.Set("Authorization", "Bearer "+client.AuthToken)
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return response.StatusCode, responseBody
}

// configurePlugin overrides the plugin's settings on the running server.
func configurePlugin(t *testing.T, th *testhelper.TestHelper, settings map[string]any) {
	t.Helper()

	ctx := t.Context()
	config, _, err := th.AdminClient.GetConfig(ctx)
	require.NoError(t, err)

	if config.PluginSettings.Plugins == nil {
		config.PluginSettings.Plugins = map[string]map[string]any{}
	}
	config.PluginSettings.Plugins[testhelper.PluginID()] = settings

	_, _, err = th.AdminClient.UpdateConfig(ctx, config)
	require.NoError(t, err)
}

func TestPluginIsInstalledAndRespondsToItsAPI(t *testing.T) {
	th := testhelper.Setup(t)

	status, body := apiRequest(t, th, th.Client, http.MethodGet, "/config", nil)
	require.Equal(t, http.StatusOK, status, string(body))

	var config map[string]any
	require.NoError(t, json.Unmarshal(body, &config))
	assert.Contains(t, config, "max_reminders")
	assert.Contains(t, config, "timezone")
}

func TestUnauthenticatedRequestsAreRefused(t *testing.T) {
	th := testhelper.Setup(t)

	url := fmt.Sprintf("%s/plugins/%s/api/v1/reminders", th.ServerURL, testhelper.PluginID())
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestSlashCommandIsRegistered(t *testing.T) {
	th := testhelper.Setup(t)

	commands, _, err := th.Client.ListCommands(t.Context(), th.Team.Id, false)
	require.NoError(t, err)

	var triggers []string
	for _, command := range commands {
		triggers = append(triggers, command.Trigger)
	}
	assert.Contains(t, triggers, "remind")
}

func TestSlashCommandCreatesAndListsAReminder(t *testing.T) {
	th := testhelper.Setup(t)
	ctx := t.Context()

	response, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind me in 2 hours to check the build")
	require.NoError(t, err)
	assert.Contains(t, response.Text, "I'll remind you")

	listed, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind list")
	require.NoError(t, err)
	assert.Contains(t, listed.Text, "check the build")
}

func TestSlashCommandExplainsAnUnreadableTime(t *testing.T) {
	th := testhelper.Setup(t)

	response, _, err := th.Client.ExecuteCommand(t.Context(), th.Channel.Id, "/remind me to do a thing")
	require.NoError(t, err)
	assert.Contains(t, response.Text, "couldn't find a time")
}

func TestRemindersAreNotVisibleToOtherUsers(t *testing.T) {
	th := testhelper.Setup(t)

	status, body := apiRequest(t, th, th.Client, http.MethodPost, "/reminders",
		map[string]any{"text": "me in 2 hours to keep this private"})
	require.Equal(t, http.StatusCreated, status, string(body))

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &created))
	require.NotEmpty(t, created.ID)

	// A second, unrelated user on the same server.
	other := th.CreateUser()
	otherClient := model.NewAPIv4Client(th.ServerURL)
	_, _, err := otherClient.Login(t.Context(), other.Username, "Password1!")
	require.NoError(t, err)

	status, body = apiRequest(t, th, otherClient, http.MethodGet, "/reminders", nil)
	require.Equal(t, http.StatusOK, status)
	assert.NotContains(t, string(body), "keep this private")

	status, _ = apiRequest(t, th, otherClient, http.MethodPost, "/reminders/"+created.ID+"/cancel", nil)
	assert.Equal(t, http.StatusNotFound, status,
		"another user's reminder must be indistinguishable from one that does not exist")

	// The owner's reminder is untouched.
	status, body = apiRequest(t, th, th.Client, http.MethodGet, "/reminders", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(body), "keep this private")
	assert.Contains(t, string(body), `"status":"scheduled"`)
}

func TestADueReminderIsDelivered(t *testing.T) {
	th := testhelper.Setup(t)

	// Tighten the schedule so the test does not have to wait a whole minute for
	// the soonest reminder the defaults allow.
	configurePlugin(t, th, map[string]any{
		"EnableScheduler":          true,
		"SchedulerIntervalSeconds": 10,
		"MinIntervalSeconds":       30,
		"MaxRemindersPerUser":      100,
		"MaxHorizonDays":           730,
		"CompletedRetentionDays":   30,
	})

	dueAt := time.Now().Add(35 * time.Second).UnixMilli()
	status, body := apiRequest(t, th, th.Client, http.MethodPost, "/reminders", map[string]any{
		"message": "the integration reminder",
		"due_at":  dueAt,
	})
	require.Equal(t, http.StatusCreated, status, string(body))

	// Poll the direct message channel with the plugin's bot until the reminder
	// shows up, or until it is clear that it will not.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the reminder was never delivered")
		}
		time.Sleep(5 * time.Second)

		if found := findDeliveredReminder(t, th, "the integration reminder"); found {
			break
		}
	}

	// And the stored reminder has moved on from scheduled.
	status, body = apiRequest(t, th, th.Client, http.MethodGet, "/reminders", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(body), `"status":"delivered"`)
}

// findReminderPost returns the bot's direct message containing the given text,
// or nil if it has not arrived.
func findReminderPost(t *testing.T, th *testhelper.TestHelper, text string) *model.Post {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())

	bots, _, err := th.AdminClient.GetBots(ctx, 0, 100, "")
	if err != nil {
		return nil
	}

	for _, bot := range bots {
		if bot.Username != botUsername {
			continue
		}

		channel, _, err := th.Client.CreateDirectChannel(ctx, th.User.Id, bot.UserId)
		if err != nil {
			return nil
		}

		posts, _, err := th.Client.GetPostsForChannel(ctx, channel.Id, 0, 60, "", false, false)
		if err != nil {
			return nil
		}
		for _, post := range posts.Posts {
			if strings.Contains(post.Message, text) {
				return post
			}
		}
	}
	return nil
}

// findDeliveredReminder reports whether the reminder has arrived.
func findDeliveredReminder(t *testing.T, th *testhelper.TestHelper, text string) bool {
	t.Helper()
	return findReminderPost(t, th, text) != nil
}

// waitForReminder polls until the reminder arrives, or fails the test.
func waitForReminder(t *testing.T, th *testhelper.TestHelper, text string, within time.Duration) *model.Post {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if post := findReminderPost(t, th, text); post != nil {
			return post
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("the reminder %q never arrived within %s", text, within)
	return nil
}

// fastScheduler tightens the plugin's timings so a test does not have to wait
// out the defaults, which are tuned for people rather than for test suites.
func fastScheduler(t *testing.T, th *testhelper.TestHelper) {
	t.Helper()
	configurePlugin(t, th, map[string]any{
		"EnableScheduler":          true,
		"SchedulerIntervalSeconds": 10,
		"MinIntervalSeconds":       30,
		"MaxRemindersPerUser":      100,
		"MaxHorizonDays":           730,
		"CompletedRetentionDays":   30,
	})
}

// The checks below replace items that used to sit on the manual pre-release
// checklist in docs/RELEASE.md. Each one is a thing a person was expected to
// remember to try by hand, which is to say a thing that would eventually get
// skipped on a Friday.

// A pending reminder has to survive the plugin being switched off and on, which
// is what an upgrade looks like. Nothing is held in memory: the reminder, the
// hour index and the scheduler cursor are all in the key-value store, and this
// is the only test anywhere that proves it against a real server rather than by
// rebuilding the plugin struct in-process.
func TestAPendingReminderSurvivesAPluginRestart(t *testing.T) {
	th := testhelper.Setup(t)
	ctx := t.Context()
	fastScheduler(t, th)

	dueAt := time.Now().Add(40 * time.Second).UnixMilli()
	status, body := apiRequest(t, th, th.Client, http.MethodPost, "/reminders", map[string]any{
		"message": "survive the restart",
		"due_at":  dueAt,
	})
	require.Equal(t, http.StatusCreated, status, string(body))

	// Off before it is due, so it cannot have been delivered already.
	_, err := th.AdminClient.DisablePlugin(ctx, testhelper.PluginID())
	require.NoError(t, err)
	require.False(t, findDeliveredReminder(t, th, "survive the restart"),
		"it was not due yet, so nothing should have been sent")

	// Stay off until well past the due time.
	time.Sleep(60 * time.Second)
	require.False(t, findDeliveredReminder(t, th, "survive the restart"),
		"a disabled plugin must not deliver")

	_, err = th.AdminClient.EnablePlugin(ctx, testhelper.PluginID())
	require.NoError(t, err)

	waitForReminder(t, th, "survive the restart", 3*time.Minute)
}

// Pressing a snooze button on a delivered reminder, through the server's own
// interactive action endpoint rather than by calling the plugin directly.
func TestSnoozeButtonReschedulesOnARealServer(t *testing.T) {
	th := testhelper.Setup(t)
	ctx := t.Context()
	fastScheduler(t, th)

	status, body := apiRequest(t, th, th.Client, http.MethodPost, "/reminders", map[string]any{
		"message": "press my buttons",
		"due_at":  time.Now().Add(35 * time.Second).UnixMilli(),
	})
	require.Equal(t, http.StatusCreated, status, string(body))

	post := waitForReminder(t, th, "press my buttons", 3*time.Minute)

	// Read the action the user would click.
	attachments, ok := post.GetProp(model.PostPropsAttachments).([]any)
	require.True(t, ok, "the delivered post should carry an attachment with buttons")
	require.NotEmpty(t, attachments)

	first, ok := attachments[0].(map[string]any)
	require.True(t, ok)
	actions, ok := first["actions"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, actions, "there should be snooze buttons to press")

	action, ok := actions[0].(map[string]any)
	require.True(t, ok)
	actionID, ok := action["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, actionID)

	_, err := th.Client.DoPostAction(ctx, post.Id, actionID)
	require.NoError(t, err, "pressing the button should be accepted")

	// The reminder goes back to scheduled, and the post keeps its text.
	require.Eventually(t, func() bool {
		_, listed := apiRequest(t, th, th.Client, http.MethodGet, "/reminders", nil)
		return strings.Contains(string(listed), `"status":"scheduled"`)
	}, 30*time.Second, 2*time.Second, "snoozing should put the reminder back on the schedule")

	updated := findReminderPost(t, th, "press my buttons")
	require.NotNil(t, updated, "the reminder text must survive the update")
}

// An administrator changing a limit has to take effect without a restart.
func TestAdministratorLimitsTakeEffectImmediately(t *testing.T) {
	th := testhelper.Setup(t)

	configurePlugin(t, th, map[string]any{
		"EnableScheduler":     true,
		"MaxRemindersPerUser": 1,
		"MinIntervalSeconds":  60,
		"MaxHorizonDays":      730,
	})

	status, _ := apiRequest(t, th, th.Client, http.MethodPost, "/reminders",
		map[string]any{"text": "me in 2 hours to be the only one"})
	require.Equal(t, http.StatusCreated, status)

	status, body := apiRequest(t, th, th.Client, http.MethodPost, "/reminders",
		map[string]any{"text": "me in 3 hours to be refused"})
	require.Equal(t, http.StatusBadRequest, status, "the second should hit the limit of one")
	assert.Contains(t, string(body), "limit")

	// Raising the limit takes effect without restarting anything.
	configurePlugin(t, th, map[string]any{
		"EnableScheduler":     true,
		"MaxRemindersPerUser": 10,
		"MinIntervalSeconds":  60,
		"MaxHorizonDays":      730,
	})

	status, body = apiRequest(t, th, th.Client, http.MethodPost, "/reminders",
		map[string]any{"text": "me in 3 hours to be allowed now"})
	assert.Equal(t, http.StatusCreated, status, string(body))
}

// The whole command surface, which used to be four lines of the manual list.
func TestTheCommandSurfaceWorks(t *testing.T) {
	th := testhelper.Setup(t)
	ctx := t.Context()

	created, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind me in 2 hours to check the build")
	require.NoError(t, err)
	require.Contains(t, created.Text, "I'll remind you")

	// The confirmation carries the short ID the other subcommands take.
	id := shortIDFromResponse(t, created.Text)

	listed, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind list")
	require.NoError(t, err)
	assert.Contains(t, listed.Text, "check the build")
	assert.Contains(t, listed.Text, "Upcoming")

	helped, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind help")
	require.NoError(t, err)
	assert.Contains(t, helped.Text, "/remind me in 30 minutes")
	assert.Contains(t, helped.Text, "Actions")

	snoozed, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind snooze "+id+" 3 hours")
	require.NoError(t, err)
	assert.Contains(t, snoozed.Text, "Postponed")

	cancelled, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind cancel "+id)
	require.NoError(t, err)
	assert.Contains(t, cancelled.Text, "Cancelled")

	after, _, err := th.Client.ExecuteCommand(ctx, th.Channel.Id, "/remind list")
	require.NoError(t, err)
	assert.Contains(t, after.Text, "cancelled")
}

// shortIDFromResponse digs the reminder's short ID out of a confirmation.
func shortIDFromResponse(t *testing.T, text string) string {
	t.Helper()
	_, after, found := strings.Cut(text, "ID `")
	require.True(t, found, "the confirmation should name the reminder's ID: %s", text)
	id, _, found := strings.Cut(after, "`")
	require.True(t, found)
	require.NotEmpty(t, id)
	return id
}
