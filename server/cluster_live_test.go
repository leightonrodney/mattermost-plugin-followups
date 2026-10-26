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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
func enableDebugLogging(t *testing.T) {
	t.Helper()

	patchConfig(t, fmt.Sprintf(
		`{"PluginSettings":{"Plugins":{%q:{"enabledebuglogging":true}}}}`, pluginID))
}

// patchConfig merges a partial configuration into the running servers. Both
// nodes read one configuration out of the database, so patching either is
// enough.
//
// It goes through `config patch` rather than `config set`, because a plugin's
// settings live in a map and mmctl refuses to address into one: `config set`
// answers "path object type is not supported".
func patchConfig(t *testing.T, partial string) {
	t.Helper()

	local := filepath.Join(t.TempDir(), "patch.json")
	require.NoError(t, os.WriteFile(local, []byte(partial), 0o644)) //nolint:gosec

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

	// Last, because it changes the server's push configuration.
	t.Run("asks the push proxy to notify", asksThePushProxyToNotify)
}

func deliversEachReminderExactlyOnce(t *testing.T) {
	assertEachReminderArrivesOnce(t, "clusteruser")
}

// assertEachReminderArrivesOnce is the claim the whole scheduling design rests
// on, written once so the unlicensed and licensed clusters are held to exactly
// the same standard.
func assertEachReminderArrivesOnce(t *testing.T, username string) {
	t.Helper()

	client := newUser(t, username)
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

// asksThePushProxyToNotify is the part of mobile delivery that can be checked
// without a phone.
//
// Until now the only evidence that a reminder reaches a device was someone
// looking at one, and that evidence was nearly misread: Mattermost does not log
// successful pushes at debug level, so an empty server log looked like proof
// that nothing was sent when the opposite was true. See docs/TESTING.md.
//
// What this does instead is stand a recorder up in the test process, point the
// server's push notification server at it, attach a device to the session, and
// let a reminder come due. If the server decides a reminder warrants a push, it
// posts one here and the payload can be inspected. That covers everything up to
// the proxy: the decision, the routing and the contents. It cannot cover what
// Apple or Google then do, which is the part that still needs a device.
func asksThePushProxyToNotify(t *testing.T) {
	type recorded struct {
		path string
		body map[string]any
	}

	var (
		mu       sync.Mutex
		captured []recorded
	)

	// All interfaces on purpose: the containers reach this through the host
	// gateway, so binding loopback would make it unreachable from them.
	listener, err := net.Listen("tcp", "0.0.0.0:0") //nolint:gosec // reached from containers
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port

	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)

			mu.Lock()
			captured = append(captured, recorded{path: r.URL.Path, body: body})
			mu.Unlock()

			// What a real push proxy answers. Anything else and the server
			// treats the send as failed.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"OK"}`))
		}),
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	// host.docker.internal is the container's route back to this process; the
	// compose file maps it explicitly so this also works on Linux.
	patchConfig(t, fmt.Sprintf(`{"EmailSettings":{
		"SendPushNotifications":true,
		"PushNotificationServer":"http://host.docker.internal:%d",
		"PushNotificationContents":"full"
	}}`, port))

	// Both nodes have to come back for that to take effect. The configuration
	// lives in the shared database, but with clustering off there is no cluster
	// bus to tell the other node it changed, so it goes on serving the copy it
	// read at boot. Skip this and the test passes or fails depending on which
	// node happens to hold the scheduler lock — the one that did not get the
	// news refuses the push with reason "push_disabled".
	compose(t, "restart", "mattermost1", "mattermost2")
	waitForServer(t, "node 1", node1URL)
	waitForServer(t, "node 2", node2URL)

	client := newUser(t, "pushuser")
	me, _, err := client.GetMe(context.Background(), "")
	require.NoError(t, err)

	// The platform prefix has to be one the server allows, and the allowlist is
	// narrower than the push payloads suggest: only the React Native platforms
	// (apple_rn, apple_rnbeta, android_rn) are accepted. A plain "apple:" id is
	// refused with "Invalid or missing device_id in request body". The token
	// after the prefix is never used, because nothing past the recorder sees it.
	_, err = client.AttachDeviceProps(context.Background(), map[string]string{
		"device_id":      "apple_rn:followups-test-device-token",
		"mobile_version": "2.0.0",
	})
	require.NoError(t, err, "should be able to attach a device to the session")

	const text = "push through the proxy"
	createReminder(t, client, node1URL, "me in 70 seconds to "+text)

	deadline := time.Now().Add(deliveryWait)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := len(captured) > 0
		mu.Unlock()
		if found {
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Whether the reminder arrived at all decides what a failure here means, so
	// find out before asserting rather than leaving the next reader to guess.
	arrived := botMessages(t, client, me.Id)

	mu.Lock()
	defer mu.Unlock()

	require.NotEmptyf(t, captured,
		"no push was requested. The reminder itself %s, so this is a failure of %s. "+
			"Check the node logs for \"type\":\"push\" and its reason.",
		map[bool]string{true: "did arrive", false: "never arrived either"}[len(arrived) > 0],
		map[bool]string{true: "the push decision", false: "delivery, not push"}[len(arrived) > 0])

	for _, push := range captured {
		t.Logf("push to %s: %v", push.path, push.body)
	}

	// With PushNotificationContents set to full the body carries the message,
	// which is what makes a reminder useful on a lock screen. Checking the text
	// rather than merely that something was sent is the difference between
	// "a push happened" and "the right push happened".
	var carriesTheReminder bool
	for _, push := range captured {
		if message, ok := push.body["message"].(string); ok && strings.Contains(message, text) {
			carriesTheReminder = true
			break
		}
	}
	assert.Truef(t, carriesTheReminder,
		"a push was sent but none carried the reminder's own text %q", text)
}

// licenceFile is where this test looks for an Enterprise licence, in order.
// Nothing is bundled and nothing is committed: .gitignore covers both the
// default path and *.mattermost-license.
func licenceFile() string {
	if fromEnv := os.Getenv("MM_LICENSE_FILE"); fromEnv != "" {
		return fromEnv
	}
	return "../dev/cluster/license.txt"
}

// loginAdmin returns a client for the system admin created at cluster start.
func loginAdmin(t *testing.T) *model.Client4 {
	t.Helper()

	client := model.NewAPIv4Client(node1URL)
	_, _, err := client.Login(context.Background(), "clusteradmin", "Password1!")
	require.NoError(t, err, "the admin account created at cluster start should log in")

	return client
}

// TestLicensedHACluster is the one check this project cannot run for itself,
// because Mattermost gates clustering behind an Enterprise licence.
//
// Everything needed to run it is here. Supply a licence — as MM_LICENSE_FILE,
// or at dev/cluster/license.txt — and it installs it, turns ClusterSettings on,
// restarts both nodes, waits for them to find each other over the gossip bus,
// and then holds the plugin to exactly the same exactly-once standard as the
// unlicensed cluster. Supply nothing and it skips with an explanation.
//
// The expected result is "no change", because this plugin coordinates through
// compare-and-set against the shared database and never touches the cluster
// bus. That is a reasonable belief rather than a measurement, which is the
// whole reason to leave this here ready to run.
func TestLicensedHACluster(t *testing.T) {
	path := licenceFile()

	licence, err := os.ReadFile(path) //nolint:gosec // path is the operator's own
	if err != nil {
		t.Skipf("no Enterprise licence, so clustering cannot be switched on.\n"+
			"  Put one at %s, or set MM_LICENSE_FILE, and run this again.\n"+
			"  Without it the two-node test above still runs; it just has no cluster bus.",
			path)
	}
	require.NotEmpty(t, licence, "the licence file at %s is empty", path)

	startCluster(t)

	admin := loginAdmin(t)
	_, err = admin.UploadLicenseFile(context.Background(), licence)
	require.NoError(t, err, "the licence should be accepted; an expired one is refused here")

	// ReadOnlyConfig is deliberately off: Mattermost locks configuration when
	// clustering is on, and the tests still need to change plugin settings.
	patchConfig(t, `{"ClusterSettings":{
		"Enable":true,
		"ClusterName":"followups-test",
		"UseIPAddress":true,
		"ReadOnlyConfig":false
	}}`)

	// Clustering is read at startup, so both nodes have to come back for it.
	compose(t, "restart", "mattermost1", "mattermost2")
	waitForServer(t, "node 1", node1URL)
	waitForServer(t, "node 2", node2URL)

	// Prove the cluster actually formed. Without this the test would quietly
	// degrade into the unlicensed one and still pass, which is the failure mode
	// worth guarding against rather than the delivery itself.
	admin = loginAdmin(t)

	var members []*model.ClusterInfo
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		members, _, err = admin.GetClusterStatus(context.Background())
		if err == nil && len(members) >= 2 {
			break
		}
		time.Sleep(5 * time.Second)
	}
	require.NoError(t, err, "should be able to read cluster status with a licence installed")
	require.GreaterOrEqualf(t, len(members), 2,
		"the two nodes never joined one cluster, so this is not testing what it claims; saw %d", len(members))

	for _, member := range members {
		t.Logf("cluster member: hostname=%s ip=%s version=%s config_hash=%s",
			member.Hostname, member.IPAddress, member.Version, member.ConfigHash)
	}

	requireActivatedOnBothNodes(t)
	assertEachReminderArrivesOnce(t, "licenseduser")
}
