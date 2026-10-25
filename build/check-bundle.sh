#!/usr/bin/env bash
# Checks that the built bundle actually contains everything plugin.json promises.
#
# A bundle that is missing a platform binary, or whose webapp bundle failed to
# build, installs without complaint and then misbehaves at runtime. This catches
# that before the artifact is published.
set -euo pipefail

plugin_id="$(./build/bin/manifest id)"
version="$(./build/bin/manifest version)"
bundle="dist/${plugin_id}-${version}.tar.gz"

if [ ! -f "$bundle" ]; then
    echo "No bundle at $bundle. Run 'make dist' first."
    exit 1
fi

contents="$(tar -tzf "$bundle")"

check() {
    if ! grep -Fqx "$1" <<<"$contents"; then
        echo "Missing from the bundle: $1"
        exit 1
    fi
    echo "  ok  $1"
}

echo "Checking $bundle"
check "${plugin_id}/plugin.json"
check "${plugin_id}/assets/icon.svg"
check "${plugin_id}/webapp/dist/main.js"

for platform in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
    check "${plugin_id}/server/dist/plugin-${platform}"
done
check "${plugin_id}/server/dist/plugin-windows-amd64.exe"

# The manifest the bundle carries has to be the one that was built, not a stale
# copy from a previous version.
bundled_version="$(tar -xzOf "$bundle" "${plugin_id}/plugin.json" | sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' | head -1)"
if [ "$bundled_version" != "$version" ]; then
    echo "The bundled manifest says $bundled_version but the build says $version"
    exit 1
fi
echo "  ok  bundled manifest version is $version"

echo "Bundle looks complete."
