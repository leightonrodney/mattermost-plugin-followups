//go:build cluster

// Two real Mattermost servers, one Postgres, one plugin bundle, two plugin
// processes. This is the test the in-process cluster tests cannot be: there,
// "a node" is a Plugin value sharing a MemoryStore, and the clock is injected.
// Here each node is its own server process spawning its own plugin binary, the
// compare-and-set that stops double delivery goes to Postgres, and the
// schedulers fire on their own timers.
//
//	make test-cluster
//
// It needs Docker and takes a few minutes, so it is behind its own build tag
// and is not part of `make test` or `make test-integration`.
//
// Worth being precise about what this does and does not prove. Mattermost gates
// clustering behind an Enterprise licence, so there is no cluster bus here.
// That matters less than it sounds for this plugin: cluster.Schedule and
// cluster.Mutex are built on KVSetWithOptions alone, so the coordination under
// test is the real thing. What a licensed cluster would add is node lifecycle
// and inter-node messaging, neither of which this plugin uses. See
// docs/TESTING.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pluginID  = "com.github.leightonrodney.followups"
	node1URL  = "http://localhost:8071"
	node2URL  = "http://localhost:8072"
	composeAt = "../dev/cluster/docker-compose.yml"

	// The plugin refuses to schedule anything sooner than its minimum interval,
	// which defaults to 60 seconds, so a reminder cannot be made due in five.
	dueIn        = 70 * time.Second
	deliveryWait = 4 * time.Minute
)

// dockerCmd builds a docker invocation. Every argument in this file is a
// constant or a value the test itself chose, so the one suppression gosec needs
// lives here instead of being repeated at each call site.
func dockerCmd(args ...string) *exec.Cmd {
	return exec.Command("docker", args...) //nolint:gosec // fixed args, test harness
}

// compose runs a docker compose command against the cluster file.
func compose(t *testing.T, args ...string) string {
	t.Helper()

	full := append([]string{"compose", "-f", composeAt}, args...)
	cmd := dockerCmd(full...)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "docker %s\n%s", strings.Join(full, " "), out)

	return string(out)
}

// dockerExec runs a command inside one of the node containers.
func dockerExec(t *testing.T, container string, args ...string) string {
	t.Helper()

	cmd := dockerCmd(append([]string{"exec", container}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "docker exec %s %s\n%s", container, strings.Join(args, " "), out)

	return string(out)
}

// mmctl administers a node over its local socket, which needs no token.
func mmctl(t *testing.T, container string, args ...string) string {
	t.Helper()
	return dockerExec(t, container, append([]string{"/mattermost/bin/mmctl", "--local"}, args...)...)
}

// waitForServer polls a node's ping endpoint until it answers.
func waitForServer(t *testing.T, name, url string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		response, err := http.Get(url + "/api/v4/system/ping") //nolint:noctx
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				t.Logf("%s is up at %s", name, url)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s never answered at %s", name, url)
}

// bundlePath finds the bundle that `make dist` produced.
func bundlePath(t *testing.T) string {
	t.Helper()

	matches, err := filepath.Glob("../dist/*.tar.gz")
	require.NoError(t, err)
	require.NotEmptyf(t, matches, "no bundle in dist/ — run `make dist` first")

	return matches[0]
}

// startCluster brings the whole thing up: database, first node, plugin, second
// node. The second node starts last on purpose, so that it finds the plugin in
// the shared filestore on boot rather than having to be told about it — without
// a cluster bus, nothing would tell it.
func startCluster(t *testing.T) {
	t.Helper()

	t.Cleanup(func() {
		// Logs first: once the containers are gone, so is any explanation.
		if t.Failed() {
			for _, c := range []string{"followups-cluster-mattermost1-1", "followups-cluster-mattermost2-1"} {
				out, _ := dockerCmd("logs", "--tail", "60", c).CombinedOutput()
				t.Logf("---- %s ----\n%s", c, out)
			}
		}
		compose(t, "down", "-v")
	})

	compose(t, "down", "-v")
	compose(t, "up", "-d", "postgres", "mattermost1")
	waitForServer(t, "node 1", node1URL)

	bundle := bundlePath(t)
	raw, err := os.ReadFile(bundle) //nolint:gosec
	require.NoError(t, err)

	tmp := filepath.Join(t.TempDir(), filepath.Base(bundle))
	require.NoError(t, os.WriteFile(tmp, raw, 0o644)) //nolint:gosec

	cp := dockerCmd("cp", tmp, "followups-cluster-mattermost1-1:/tmp/plugin.tar.gz")
	out, err := cp.CombinedOutput()
	require.NoErrorf(t, err, "docker cp failed: %s", out)

	mmctl(t, "followups-cluster-mattermost1-1", "plugin", "add", "/tmp/plugin.tar.gz")
	mmctl(t, "followups-cluster-mattermost1-1", "plugin", "enable", pluginID)

	// Only now does the second node start, so it reads the plugin out of the
	// shared filestore and the enabled state out of the shared config.
	compose(t, "up", "-d", "mattermost2")
	waitForServer(t, "node 2", node2URL)

	requireActivatedOnBothNodes(t)

	// Mattermost makes the first account on a fresh server a system admin.
	// Creating one up front means every account the tests then make is an
	// ordinary member, which is what a real user is.
	mmctl(t, "followups-cluster-mattermost1-1", "user", "create",
		"--email", "admin@example.com", "--username", "clusteradmin",
		"--password", "Password1!", "--system-admin")

	enableDebugLogging(t)
}

// requireActivatedOnBothNodes is what stops this test being vacuous. Exactly
// once delivery is trivially satisfied if only one node is actually running the
// plugin, so prove two plugin processes really started before trusting anything
// that follows.
func requireActivatedOnBothNodes(t *testing.T) {
	t.Helper()

	for _, container := range []string{
		"followups-cluster-mattermost1-1",
		"followups-cluster-mattermost2-1",
	} {
		var logs string
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			out, _ := dockerCmd("logs", container).CombinedOutput()
			logs = string(out)
			if strings.Contains(logs, "Follow-ups activated") {
				break
			}
			time.Sleep(2 * time.Second)
		}
		require.Containsf(t, logs, "Follow-ups activated",
			"%s never activated the plugin, so this test would prove nothing", container)
	}
	t.Log("both nodes are running their own copy of the plugin")
}

// enableDebugLogging turns on the plugin's own debug logging, so that a
// scheduler run which delivers something says so. Without it the per-node
// delivery counts this test prints are always zero and mean nothing.
//
// It goes through `config patch` rather than `config set`, because a plugin's
// settings live in a map and mmctl refuses to address into one: `config set`
// answers "path object type is not supported".
func enableDebugLogging(t *testing.T) {
	t.Helper()

	patch := fmt.Sprintf(
		`{"PluginSettings":{"Plugins":{%q:{"enabledebuglogging":true}}}}`, pluginID)

	local := filepath.Join(t.TempDir(), "patch.json")
	require.NoError(t, os.WriteFile(local, []byte(patch), 0o644)) //nolint:gosec

	out, err := dockerCmd("cp", local,
		"followups-cluster-mattermost1-1:/tmp/patch.json").CombinedOutput()
	require.NoErrorf(t, err, "docker cp: %s", out)

	mmctl(t, "followups-cluster-mattermost1-1", "config", "patch", "/tmp/patch.json")
}

// newUser creates an account on node 1 and logs it in there.
func newUser(t *testing.T, name string) *model.Client4 {
	t.Helper()

	email := name + "@example.com"
	mmctl(t, "followups-cluster-mattermost1-1", "user", "create",
		"--email", email, "--username", name, "--password", "Password1!")

	client := model.NewAPIv4Client(node1URL)
	_, _, err := client.Login(context.Background(), name, "Password1!")
	require.NoError(t, err)

	return client
}

// createReminder asks the plugin, through whichever node the client points at,
// to schedule a reminder from natural language.
func createReminder(t *testing.T, client *model.Client4, baseURL, text string) string {
	t.Helper()

	body, err := json.Marshal(map[string]any{"text": text})
	require.NoError(t, err)

	url := fmt.Sprintf("%s/plugins/%s/api/v1/reminders", baseURL, pluginID)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(string(body)))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+client.AuthToken)
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusCreated, response.StatusCode, "create failed: %s", raw)

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(raw, &created))

	return created.ID
}

// botMessages returns everything the Follow-ups bot has sent this user.
//
// Two things here are deliberate, both learned the hard way. The bot is looked
// up by username rather than through /api/v4/bots, because listing bots needs
// read_bots and an ordinary member does not have it — the first account on a
// fresh server is a system admin, so a test that only ever used that account
// would never find out. And every failure below is fatal rather than an empty
// result: returning nil on a 403 makes a permission problem look exactly like
// a delivery problem, and costs the length of the timeout to misdiagnose.
func botMessages(t *testing.T, client *model.Client4, userID string) []string {
	t.Helper()

	bot, _, err := client.GetUserByUsername(context.Background(), "followups", "")
	require.NoError(t, err, "the Follow-ups bot account should exist on the server")

	channel, _, err := client.CreateDirectChannel(context.Background(), userID, bot.Id)
	require.NoError(t, err, "should be able to open a direct channel with the bot")

	posts, _, err := client.GetPostsForChannel(context.Background(), channel.Id, 0, 200, "", false, false)
	require.NoError(t, err, "should be able to read the direct channel")

	var messages []string
	for _, post := range posts.Posts {
		if post.UserId == bot.Id && post.Message != "" {
			messages = append(messages, post.Message)
		}
	}

	return messages
}

func TestTwoRealNodes(t *testing.T) {
	startCluster(t)

	t.Run("delivers each reminder exactly once", deliversEachReminderExactlyOnce)
	t.Run("shares one scheduler cursor", sharesOneSchedulerCursor)
}

func deliversEachReminderExactlyOnce(t *testing.T) {
	client := newUser(t, "clusteruser")
	me, _, err := client.GetMe(context.Background(), "")
	require.NoError(t, err)

	// Distinct text per reminder, so a duplicate is visible as a repeat rather
	// than having to be inferred from a count.
	const count = 6
	wanted := map[string]int{}
	for i := range count {
		text := fmt.Sprintf("cluster reminder number %d", i)
		wanted[text] = 0
		createReminder(t, client, node1URL,
			fmt.Sprintf("me in 70 seconds to %s", text))
	}

	t.Logf("%d reminders created, due in about %s; waiting up to %s", count, dueIn, deliveryWait)

	deadline := time.Now().Add(deliveryWait)
	var messages []string
	for time.Now().Before(deadline) {
		messages = botMessages(t, client, me.Id)
		if len(messages) >= count {
			// Keep going a little past the last arrival: a duplicate would
			// show up after the count is first satisfied, not before it.
			time.Sleep(45 * time.Second)
			messages = botMessages(t, client, me.Id)
			break
		}
		time.Sleep(5 * time.Second)
	}

	for _, message := range messages {
		for text := range wanted {
			if strings.Contains(message, text) {
				wanted[text]++
			}
		}
	}

	for text, delivered := range wanted {
		assert.Equalf(t, 1, delivered,
			"%q was delivered %d times across two nodes; it must be exactly one", text, delivered)
	}

	// Which node did the delivering is reported rather than asserted. The
	// cluster lock has a time-to-live and either node may hold it for a run, so
	// requiring a particular split would be requiring a race to come out a
	// certain way.
	for _, container := range []string{
		"followups-cluster-mattermost1-1",
		"followups-cluster-mattermost2-1",
	} {
		out, _ := dockerCmd("logs", container).CombinedOutput()
		t.Logf("%s: %d scheduler runs reported a delivery", container,
			strings.Count(string(out), "Scheduler run complete"))
	}
}

func sharesOneSchedulerCursor(t *testing.T) {
	// Both nodes establish a cursor at activation. If they were not reading and
	// writing the same key in the same database, each would keep its own and
	// every reminder would be swept twice — which is the failure the previous
	// test would catch, but this says plainly where it would come from.
	first := dockerExec(t, "followups-cluster-mattermost1-1",
		"/mattermost/bin/mmctl", "--local", "plugin", "list")
	second := dockerExec(t, "followups-cluster-mattermost2-1",
		"/mattermost/bin/mmctl", "--local", "plugin", "list")

	require.Contains(t, first, pluginID)
	require.Contains(t, second, pluginID)

	client := newUser(t, "cursoruser")
	me, _, err := client.GetMe(context.Background(), "")
	require.NoError(t, err)

	createReminder(t, client, node1URL, "me in 70 seconds to check the cursor")

	// Created through node 1, and it must arrive whichever node sweeps for it.
	deadline := time.Now().Add(deliveryWait)
	var messages []string
	for time.Now().Before(deadline) {
		if messages = botMessages(t, client, me.Id); len(messages) > 0 {
			break
		}
		time.Sleep(5 * time.Second)
	}

	require.Lenf(t, messages, 1,
		"one reminder, created on one node, must arrive exactly once: got %v", messages)
	assert.Contains(t, messages[0], "check the cursor")
}
