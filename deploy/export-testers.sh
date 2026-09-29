#!/bin/sh
# Print emails of beta-signup subscribers added since the last run, one per line.
# Paste them into Play Console > Testing > Closed testing > Testers (or the Google Group).
#
# Env: LISTMONK_URL   e.g. https://lists.example.com
#      LISTMONK_AUTH  API user and token as user:token (Listmonk > Users > API)
#      LISTMONK_LIST  numeric list ID (Lists page; not the UUID used by the web form)
# Usage: export-testers.sh [--all]   --all ignores the saved cutoff
set -eu

: "${LISTMONK_URL:?}" "${LISTMONK_AUTH:?}" "${LISTMONK_LIST:?}"
state="${XDG_STATE_HOME:-$HOME/.local/state}/muxalot-testers-since"
since=""
if [ "${1:-}" != "--all" ] && [ -f "$state" ]; then since=$(cat "$state"); fi

query="subscribers.status = 'enabled'"
[ -n "$since" ] && query="$query AND subscribers.created_at > '$since'"

json=$(curl -fsS -G "$LISTMONK_URL/api/subscribers" \
  -H "Authorization: token $LISTMONK_AUTH" \
  --data-urlencode "list_id=$LISTMONK_LIST" \
  --data-urlencode "per_page=all" \
  --data-urlencode "order_by=created_at" \
  --data-urlencode "order=asc" \
  --data-urlencode "query=$query")

echo "$json" | jq -r '.data.results[].email'

# advance the cutoff only after a successful fetch
newest=$(echo "$json" | jq -r '[.data.results[].created_at] | max // empty')
if [ -n "$newest" ]; then
  mkdir -p "$(dirname "$state")"
  echo "$newest" > "$state"
fi
