# Contributing

Thanks for taking a look. Issues and pull requests are both welcome.

## Getting set up

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for the full picture. The short
version:

```bash
git clone https://github.com/leightonrodney/mattermost-plugin-followups.git
cd mattermost-plugin-followups
make test
make check-style
```

You need Go (the version in `go.mod`) and Node (the version in `.nvmrc`). You do
not need Docker unless you are running the integration suite.

## Reporting a bug

Please include:

- what you did, what you expected and what happened;
- your Mattermost server version and the plugin version;
- the exact `/remind` phrase, if parsing is involved;
- your timezone, if timing is involved — a surprising number of reminder bugs
  are really timezone bugs;
- anything relevant from the server log. Turn on **Enable debug logging** in the
  plugin's settings first. Reminder text is never logged, so a debug log is safe
  to attach.

**Please do not open an issue for a security vulnerability.** See
[SECURITY.md](SECURITY.md).

## Proposing a change

For anything beyond a small fix, open an issue first and say what you have in
mind. It is much easier to agree on an approach before the code exists than
after.

Changes that are likely to need discussion:

- new recurrence forms or parser syntax — the grammar is deliberately closed;
- anything touching the delivery protocol or the storage layout;
- new dependencies. The plugin has three (`gorilla/mux`, `pkg/errors`, the
  Mattermost SDK) and that is on purpose.

## Pull requests

1. Branch from `main`.
2. Make the change, with tests.
3. `make check-style && make test` — both clean.
4. Update the documentation in the same pull request. A change that alters a
   command form, a setting or a stored field is not finished until the README,
   the help text and the relevant file in `docs/` agree with it.
5. Add a line to the `## Unreleased` section of `CHANGELOG.md`.
6. Open the pull request, describing what changed and why. Link the issue.

CI runs linting, type-checking, both test suites and a build, and checks that
the resulting bundle contains everything the manifest promises.

## Coding conventions

Match the surrounding code. More specifically:

- **Go.** Standard library style. `gofmt` and `golangci-lint` are the arbiters;
  the configuration is in `.golangci.yml`.
- **TypeScript.** `strict` mode, and Mattermost's own ESLint configuration.
  `cd webapp && npm run fix` applies what it can.
- **Comments** explain why something is the way it is — a constraint, a
  trade-off, a non-obvious failure mode. Code that only restates what the line
  below does is noise. There are several comments in this codebase explaining
  daylight-saving and concurrency decisions; they are the model.
- **CSS.** Prefix every class `followups-`, and take every colour from a
  Mattermost theme custom property. A test enforces both, so a hard-coded colour
  fails CI rather than quietly breaking dark mode.

## Tests

A pull request should come with tests. Some guidance on what makes a good one
here:

- The storage and service tests use `pluginapi.MemoryStore`, which has the real
  compare-and-set semantics. Prefer it to a hand-written stub, which will agree
  with your assumptions rather than checking them.
- The service and scheduler take an injectable clock. No test should sleep.
- Webapp tests query **by role and accessible name**, not by class or test ID.
  That way a change which breaks the accessible name breaks the test.
- The webapp suite currently runs with no React warnings. Please keep it that
  way.

[docs/TESTING.md](docs/TESTING.md) lists what is covered today.

## Code of conduct

Be decent to each other. Assume the other person is trying to help. Disagree
about the work, not the person.

## Licence

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE), the same as the rest of the project.
