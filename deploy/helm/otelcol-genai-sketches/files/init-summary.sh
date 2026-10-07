# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
directory=/var/lib/genai-sketches/exports
fail() { echo 'Summary storage must contain only private exports owned by the collector UID' >&2; exit 1; }
[ ! -L "$directory" ] || fail
if [ ! -e "$directory" ]; then
  mkdir -m 700 "$directory"
fi
[ -d "$directory" ] && [ "$(stat -c %u "$directory")" = "$(id -u)" ] || fail
chmod 700 "$directory"
# fsGroup remounts can add group bits. Restore only our own flat export files.
for file in "$directory"/* "$directory"/.[!.]* "$directory"/..?*; do
  [ -e "$file" ] || [ -L "$file" ] || continue
  [ ! -L "$file" ] && [ -f "$file" ] || fail
  [ "$(stat -c %u "$file")" = "$(id -u)" ] && [ "$(stat -c %h "$file")" = 1 ] || fail
  name=${file##*/}
  printf '%s\n' "$name" | grep -Eq '^([0-9]{20}-[a-f0-9]{32}\.json|\.[0-9]{20}-[a-f0-9]{32}\.json-[0-9]{1,20}\.tmp|\.write-check-[a-f0-9]{32})$' || fail
  chmod 600 "$file"
done
[ "$(stat -c %a "$directory")" = 700 ] && [ -w "$directory" ] || fail
