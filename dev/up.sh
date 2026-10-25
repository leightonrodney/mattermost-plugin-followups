#!/usr/bin/env bash
# Brings up the local development server and makes it usable: waits for it to
# be healthy, then creates a system admin, a team and a second user.
#
# Running it again is safe. Everything it creates is created only if missing.
set -euo pipefail

cd "$(dirname "$0")"

compose() { docker compose "$@"; }
mm() { compose exec -T mattermost mmctl --local "$@"; }

ADMIN_USER=admin
ADMIN_PASS='Password1!'
ADMIN_EMAIL=admin@example.com
SECOND_USER=tester
SECOND_EMAIL=tester@example.com
TEAM=followups

if ! docker info >/dev/null 2>&1; then
    echo "Docker is not running. Start OrbStack (or whichever runtime you use) and try again."
    exit 1
fi

echo "Starting Mattermost ${MM_VERSION:-12.0} and Postgres..."
compose up -d --wait

echo "Creating accounts..."
# mmctl exits non-zero when a user already exists, which is not a failure here.
mm user create --email "$ADMIN_EMAIL" --username "$ADMIN_USER" --password "$ADMIN_PASS" --system-admin --email-verified >/dev/null 2>&1 \
    || echo "  admin already exists"
mm user create --email "$SECOND_EMAIL" --username "$SECOND_USER" --password "$ADMIN_PASS" --email-verified >/dev/null 2>&1 \
    || echo "  tester already exists"

# A second account matters: the plugin's whole security model is that one user
# cannot see another's reminders, and that is only checkable with two.
mm team create --name "$TEAM" --display-name "Follow-ups" --email "$ADMIN_EMAIL" >/dev/null 2>&1 \
    || echo "  team already exists"
mm team users add "$TEAM" "$ADMIN_USER" "$SECOND_USER" >/dev/null 2>&1 || true

cat <<INFO

Ready at http://localhost:8065

  admin   $ADMIN_USER / $ADMIN_PASS   (system admin)
  second  $SECOND_USER / $ADMIN_PASS

Install the plugin into it with:

  export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
  export MM_ADMIN_USERNAME=$ADMIN_USER
  export MM_ADMIN_PASSWORD='$ADMIN_PASS'
  make deploy

INFO
