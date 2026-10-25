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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		payload = bytes.NewReader(encoded)
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

// findDeliveredReminder reports whether the plugin's bot has sent the user a
// direct message containing the given text.
func findDeliveredReminder(t *testing.T, th *testhelper.TestHelper, text string) bool {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())

	bots, _, err := th.AdminClient.GetBots(ctx, 0, 100, "")
	if err != nil {
		return false
	}

	for _, bot := range bots {
		if bot.Username != botUsername {
			continue
		}

		channel, _, err := th.Client.CreateDirectChannel(ctx, th.User.Id, bot.UserId)
		if err != nil {
			return false
		}

		posts, _, err := th.Client.GetPostsForChannel(ctx, channel.Id, 0, 60, "", false, false)
		if err != nil {
			return false
		}
		for _, post := range posts.Posts {
			if bytes.Contains([]byte(post.Message), []byte(text)) {
				return true
			}
		}
	}
	return false
}
