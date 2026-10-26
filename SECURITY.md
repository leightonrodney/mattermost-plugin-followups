# Security policy

## Reporting a vulnerability

**Please do not open a public issue for a security vulnerability.**

Report it privately through GitHub's
[private vulnerability reporting](https://github.com/leightonrodney/mattermost-plugin-followups/security/advisories/new)
on this repository. That opens a draft advisory only the maintainers can see.

If you would rather not use GitHub, email **leighton.rodney@mattermost.com**
instead. Either route reaches the same person; the advisory is preferred only
because it keeps the report, the fix and the disclosure in one place.

Please include:

- what the issue is and what an attacker could do with it;
- the steps to reproduce it, as precisely as you can;
- the plugin version and the Mattermost server version;
- anything you know about how widely it applies.

You will get an acknowledgement within **5 working days**, and an assessment
with a plan and a rough timeline within **10 working days**. A fix for a
confirmed vulnerability will be released as promptly as its severity warrants,
and you will be credited in the release notes unless you would rather not be.

Please give us a reasonable chance to fix the issue before describing it
publicly.

## What is in scope

Anything in this repository, including:

- one user reading, changing or deleting another user's reminders;
- bypassing the authentication on the plugin's API;
- obtaining a permalink to, or any content from, a channel the user cannot read;
- causing the plugin to act as a user other than the authenticated one;
- getting reminder text or message content into the server log;
- denial of service against the scheduler, or escaping the per-user quotas;
- injection of any kind through reminder text or command input.

## What is not in scope

- Vulnerabilities in Mattermost Server itself. Report those through
  [Mattermost's own responsible disclosure process](https://mattermost.com/security-vulnerability-report/).
- Anything that needs system administrator access to exploit. An administrator
  can already read the database.
- The fact that an administrator can disable the Follow-ups bot and stop
  reminders being delivered. That is intended.
- Missing hardening that has no demonstrable impact.

## Supported versions

Only the most recent release receives security fixes. This project is
pre-1.0; expect to upgrade.

| Version | Supported |
|:--|:--|
| 0.1.x | yes |

## What this plugin stores, and where

Useful context when assessing a report. All of it lives in the Mattermost
plugin key-value store, in your own database. Nothing leaves your server.

| Stored | Not stored |
|:--|:--|
| Reminder text you typed | The text of any message a reminder points at |
| The time a reminder is due, and its repeat rule | Anything about other users |
| Your IANA timezone name | Credentials or tokens of any kind |
| The channel, post and team IDs a reminder links to | |
| Delivery state: attempts, status, timestamps | |

Reminder text and message content are never written to the server log, at any
log level, including debug.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the security boundaries and
how each is enforced.
