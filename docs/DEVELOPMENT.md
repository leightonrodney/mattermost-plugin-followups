# Development

## What you need

| | Version | Why |
|:--|:--|:--|
| Go | as in `go.mod` (1.26.7 or newer) | The server |
| Node | as in `.nvmrc` (24.13.1) | The webapp |
| GNU Make | any | The build |
| Docker | optional | Only for `make test-integration` |

`nvm use` picks up `.nvmrc`.

## Getting started

```bash
git clone https://github.com/leightonrodney/mattermost-plugin-followups.git
cd mattermost-plugin-followups

make dist          # build the bundle
make test          # unit tests, no Docker needed
make check-style   # linters and type checks
```

`dist/com.github.leightonrodney.followups-<version>.tar.gz` is the installable
bundle.

## Layout

```
plugin.json              the manifest: ID, version, settings schema
Makefile                 build, test, lint, package, deploy
build/                   build tooling, from the official starter template
  check-bundle.sh        verifies a built bundle has everything the manifest promises
server/
  main.go                entry point; also embeds Go's timezone database
  plugin.go              lifecycle: activation, deactivation, the scheduler job
  configuration.go       settings, with clamping
  service.go             the rules: quotas, ownership, the delivery protocol
  scheduler.go           the sweep over hourly index buckets
  delivery.go            building and sending the reminder post
  command.go             /remind
  api.go                 the HTTP API and interactive buttons
  reminder/              the domain: model, parser, recurrence engine
  store/                 persistence: records, the due index, the cursor
webapp/
  src/
    index.tsx            plugin registration
    client.ts            the API client
    store.ts             shared state, via useSyncExternalStore
    time.ts              formatting helpers
    styles.scss          themed with Mattermost's own custom properties
    components/
      app.tsx            the two registered roots
      reminder_modal.tsx the create/edit dialog
      rhs.tsx            the sidebar
      reminder_row.tsx   one row
      icon.tsx           the channel header icon
docs/
```

`server/manifest.go` and `webapp/src/manifest.ts` are **generated** from
`plugin.json` by `make apply`. They are gitignored; do not edit them, and run
`make apply` before `go build` or `tsc` in a fresh checkout.

## Deploying to a development server

Set credentials for your server, then:

```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_TOKEN=<a personal access token>
# or MM_ADMIN_USERNAME / MM_ADMIN_PASSWORD

make deploy
```

`MM_SERVICESETTINGS_ENABLEDEVELOPER=true` builds only for your own platform,
which is much faster than building all five.

To iterate on the webapp without rebuilding the server each time:

```bash
make watch
```

Other useful targets:

```bash
make reset        # disable and re-enable the plugin on the server
make logs         # tail the plugin's server logs
make logs-watch   # follow them
make kill         # kill running plugin processes
make clean        # remove all build output
```

## Debugging

```bash
MM_DEBUG=1 make deploy   # build without optimisations and with symbols
make attach              # attach delve
make attach-headless     # attach delve on port 2346, for an IDE
make detach
```

Release builds strip symbols (`-ldflags="-s -w"`), which is why `MM_DEBUG=1`
matters: it skips the stripping and disables inlining so delve can see
everything.

Turn on **Enable debug logging** in the plugin's settings to get scheduler and
request activity in the server log. Reminder text is never logged, at any level,
so a debug log is safe to share.

## Conventions

- **Go.** Standard library style. `gofmt`, `go vet` and `golangci-lint` all
  clean; the configuration is `.golangci.yml`, inherited from the starter
  template. Comments explain *why*, not *what*.
- **TypeScript.** `strict` mode. The ESLint configuration is Mattermost's own
  (`@mattermost/eslint-plugin`); `npm run fix` applies what it can.
- **CSS.** Every class is prefixed `followups-`, and every colour comes from a
  Mattermost theme custom property. There is a test that enforces both.
- **Errors.** Errors meant for a user are built with `userErrorf` and shown
  verbatim. Everything else is logged and replaced with a generic message, so
  internal details cannot leak into a channel. `IsUserFacing` is the boundary.
- **Time.** The service and scheduler take an injectable clock. Never call
  `time.Now()` in a handler: use `p.service.Now()`, so that a duration worked
  out in a handler and the instant the service adds it to agree.

## Making changes safely

Some things are easy to break without noticing:

- **Adding a field to a stored reminder.** It is written into the per-user
  aggregate, so it must be optional on read; old records will not have it. If it
  changes meaning rather than adding to it, bump `SchemaVersion` and add a
  migration — see the end of [ARCHITECTURE.md](ARCHITECTURE.md).
- **Changing the delivery protocol.** Claim, post, settle exists to make
  duplicate delivery rare. Read the guarantee section before touching it, and
  keep the concurrency tests passing under `-race`.
- **Adding an API endpoint.** It goes on the `api` subrouter, which is behind
  `requireUser`. Scope it to `userIDFrom(r)` and nothing else.
- **Adding a plugin setting.** Add it to `plugin.json` *and* to
  `configuration.go` *and* to `defaultConfiguration()`. A test asserts those
  defaults agree with the manifest, and it will fail if you forget one.
- **Changing a parser form.** Update the table in the README and the help text
  in `command.go`. The parser tests use the same forms the README documents.
