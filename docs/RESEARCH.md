# API research

This records what was checked before implementation, and what was decided as a
result. It was written on **25 October 2026** against
`github.com/mattermost/mattermost/server/public v0.5.0`.

Where a claim concerns an API signature, it was verified by reading the SDK
source in the module cache rather than the documentation, because the
documentation lags the module.

## Sources

- [Mattermost plugin documentation](https://developers.mattermost.com/integrate/plugins/)
- [Server-side plugin reference](https://developers.mattermost.com/integrate/plugins/server/)
- [Webapp plugin reference](https://developers.mattermost.com/integrate/plugins/webapp/)
- [mattermost-plugin-starter-template](https://github.com/mattermost/mattermost-plugin-starter-template) — cloned and read
- `github.com/mattermost/mattermost/server/public@v0.5.0` — the SDK, read from the module cache
- [`webapp/channels/src/plugins/registry.ts`](https://github.com/mattermost/mattermost/blob/master/webapp/channels/src/plugins/registry.ts) — the real webapp registry
- [`webapp/channels/src/types/store/plugins.ts`](https://github.com/mattermost/mattermost/blob/master/webapp/channels/src/types/store/plugins.ts) — the registry's own types

## What already exists

### In Mattermost itself

| Feature | Since | What it does | What it does not do |
|:--|:--|:--|:--|
| [Message reminders](https://docs.mattermost.com/end-user-guide/collaborate/message-reminders.html) | v7.10 | One timed reminder on a post, from the post's **…** menu, delivered by the system bot | No recurrence, no reminder text of your own, no slash command, no list |
| Scheduled messages | v10.3 | Send a message you write now at a later time | Sends to a channel rather than reminding you; not recurring |

The docs are explicit that recurring reminders are not supported natively.

### Community plugins

- [`scottleedavis/mattermost-plugin-remind`](https://github.com/scottleedavis/mattermost-plugin-remind) —
  the closest prior art. **Archived**, last pushed November 2023, last release
  v1.0.0 in March 2023, 60 open issues, Apache-2.0. Never listed in the
  Marketplace. A Mattermost staff member noted on the forum that post reminders
  became a native feature, without full parity.

### Where that leaves this plugin

The gap is: reminders with your own text, settable from a command, repeating,
and manageable as a list. That is what this plugin implements, and it stays out
of the way of the native features rather than replacing them.

## Compatibility

| | Chosen | Why |
|:--|:--|:--|
| `min_server_version` | `9.5.0` | A conservative modern baseline. Every API used predates it comfortably; see the table below. |
| SDK | `server/public v0.5.0` | Current at the time of writing, and what the starter template pins. |
| Go | per `go.mod` (1.26.7) | Matches the starter template. Built and tested here on Go 1.27.3. |
| Node | per `.nvmrc` (24.13.1) | Matches the starter template. |

Mattermost's current Extended Support Release at the time of writing is v11.7,
and ESRs are supported for twelve months. A `min_server_version` of 9.5.0 covers
that and several releases before it.

> **Measured, not just inferred.** The integration suite passes end-to-end
> against both v9.5 and v12.0.1 Team Edition. The releases in between have not
> been run; see [TESTING.md](TESTING.md).

## APIs verified

Each was confirmed by reading the declaration in the SDK source, not assumed.

### Server

| Need | API | Verified at | Minimum server |
|:--|:--|:--|:--|
| Slash command | `pluginapi.SlashCommandService.Register` | `pluginapi/slashcommand.go` | 5.2 |
| Command execution | `plugin.Hooks.ExecuteCommand` | `plugin/hooks.go` | — |
| Autocomplete | `model.NewAutocompleteData`, `AddTextArgument`, `AddCommand` | `model/command_autocomplete.go` | 5.24 |
| Direct message | `pluginapi.PostService.DM` | `pluginapi/post.go:37` | 5.2 |
| Bot account | `pluginapi.BotService.EnsureBot` | `pluginapi/bot.go:147` | 5.10 |
| KV get/set | `pluginapi.KVService.Get` / `.Set` | `pluginapi/kv.go:58,156` | 5.2 / 5.18 |
| KV compare-and-set | `pluginapi.SetAtomic` | `pluginapi/kv.go:36` | 5.18 |
| KV expiry | `pluginapi.SetExpiry` | `pluginapi/kv.go:44` | 5.18 |
| Cluster-wide job | `pluginapi/cluster.Schedule` | `pluginapi/cluster/job.go:115` | — |
| Cluster-wide lock | `pluginapi/cluster.NewMutex` | `pluginapi/cluster/mutex.go:55` | — |
| HTTP endpoint | `plugin.Hooks.ServeHTTP` | `plugin/hooks.go` | — |
| User lookup | `pluginapi.UserService.Get` | `pluginapi/user.go:19` | 5.2 |
| User timezone | `model.User.GetPreferredTimezone` | `model/user.go:1008` | — |
| Channel permission | `pluginapi.UserService.HasPermissionToChannel` | `pluginapi/user.go:213` | 5.2 |
| Channel lookup | `pluginapi.ChannelService.Get` | `pluginapi/channel.go:21` | 5.2 |
| Teams for a user | `pluginapi.TeamService.List` + `FilterTeamsByUser` | `pluginapi/team.go:53` | 5.2 |
| Site URL | `pluginapi.ConfigurationService.GetConfig` | `pluginapi/configuration.go:25` | 5.2 |
| Interactive buttons | `model.PostAction`, `model.PostActionIntegration` | `model/integration_action.go:172,418` | 5.2 |
| Button callback | `model.PostActionIntegrationRequest` | `model/integration_action.go:425` | 5.2 |
| Message attachment | `model.MessageAttachment` via `post.AddProp(model.PostPropsAttachments, …)` | `model/message_attachment.go:20`, `model/post.go:119` | 5.2 |
| Settings schema | `plugin.API.LoadPluginConfiguration` | `plugin/api.go` | 5.2 |
| In-memory KV for tests | `pluginapi.MemoryStore` | `pluginapi/kv_memory.go:20` | — |
| Mocked API for tests | `plugin/plugintest.API` | `plugin/plugintest/api.go` | — |
| Containerised integration tests | `pluginapi/testhelper.Setup` | `pluginapi/testhelper/helper.go:171` | — |

**Two assumptions turned out to be wrong and were corrected:**

- There is no `model.ParseSlackAttachment` in `server/public`. Attachments are
  set with `post.AddProp(model.PostPropsAttachments, []*model.MessageAttachment{…})`.
- There is no `TeamService.ListForUser`. The equivalent is
  `TeamService.List(pluginapi.FilterTeamsByUser(userID))`.
- There is no `model.PostActionIntegrationRequestFromJSON` helper in this
  module; the payload is decoded with `encoding/json` directly.

### Webapp

Confirmed against the real `registry.ts`, which the starter template's local
type stub only partially describes.

| Need | API | Notes |
|:--|:--|:--|
| Message action | `registerPostDropdownMenuAction(text, action, filter)` | `action` and `filter` each receive the **post ID**. The starter's stub declared `action` as taking nothing and `filter` as taking a `Post`; both were corrected locally against `types/store/plugins.ts`. |
| Sidebar | `registerRightHandSidebarComponent(component, title)` | Returns `{id, showRHSPlugin, hideRHSPlugin, toggleRHSPlugin}`; the last three are actions to dispatch. |
| Header button | `registerChannelHeaderButtonAction(icon, action, dropdownText, tooltipText)` | Used to toggle the sidebar. |
| Always-mounted root | `registerRootComponent(component)` | Hosts the dialog. |

`registerAppBarComponent` also exists and would give a dedicated App Bar icon,
but it requires an icon **URL** rather than a React element, which means serving
an asset. The channel header button needs no asset and works on every supported
version, so that was chosen instead. The App Bar is noted as a v0.2.0 idea.

## Decisions

### Date parsing: written here, not taken from a library

The brief allowed a well-maintained library. The candidates were
`olebedev/when`, `araddon/dateparse` and `tj/go-naturaldate`. A purpose-built
parser was chosen instead because:

- The brief asks for **deterministic** parsing. The general-purpose libraries
  are heuristic, and `when` in particular resolves ambiguity in ways that are
  hard to predict or document.
- Timezone handling has to be exactly right — a reminder in the wrong hour is a
  broken reminder — and these libraries vary in how carefully they apply a
  location.
- The grammar needed is small and closed: relative offsets, named days,
  weekdays, dates, clock times and a handful of recurrence forms. It is about
  900 lines including the recurrence engine, and every form is covered by a
  test.
- It removes a dependency from a plugin that otherwise has none beyond the
  Mattermost SDK, `gorilla/mux` and `pkg/errors`.

The trade-off is that unusual phrasings are refused rather than guessed at. The
command responds with the forms it does understand, which is a better failure
than a reminder at the wrong time.

### Scheduling: hourly buckets, not a scan

`pluginapi.KVService.ListKeys` filters client-side after paging through every
key the plugin owns, so prefix listing is O(all keys). Using it on each tick
would make the scheduler's cost grow with the size of the server. The hourly,
sharded due index in [ARCHITECTURE.md](ARCHITECTURE.md) makes a steady-state
tick cost 8 reads regardless of how many users there are.

### Storage: one record per user, not one per reminder

The key-value store has no transactions, so quota checks, edits and delivery
claims can only be made atomic by keeping a user's reminders under a single key
and using compare-and-set. This also makes cross-user exposure structurally
impossible rather than merely checked for.

### Delivery: a direct message, not a custom channel

Posting as a bot means reminders inherit each user's existing notification
settings, reach the mobile apps through the normal push pipeline, and are
searchable afterwards. No separate notification mechanism is needed, and none
exists in the plugin API that would be better.

## Limitations found during research

| Wanted | Available? | What was done instead |
|:--|:--|:--|
| Exactly-once delivery | No. Posting and recording state cannot commit together. | At-least-once, with the duplicate window documented and bounded by a 5-minute claim timeout. |
| Efficient prefix scan of KV | No. `ListKeys` pages everything. | The hourly due index. |
| Transactions across keys | No. | One aggregate record per user. |
| Cleaning up index buckets | Possible, but costly. | Buckets carry a 90-day expiry and clean themselves up. |
| Reacting to a deleted user | No hook for user deletion in this SDK version. | Not claimed. A deleted user's record remains until an administrator removes it; the API to delete it exists in the store layer. |
| App Bar icon without an asset | No; it needs an icon URL. | Channel header button, which takes a React element. |
