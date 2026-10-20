#!/bin/sh
# Export SBX_AGENT_BIN when the release image contains sbx-agent and neither
# it nor SBX_DOCKER_AGENT_BIN is set. A build from before that binary exists
# leaves the variable unset, and the server keeps the shell exec path.
set -eu
if [ -x /usr/local/bin/sbx-agent ] && [ -z "${SBX_AGENT_BIN:-}" ] && [ -z "${SBX_DOCKER_AGENT_BIN:-}" ]; then
  export SBX_AGENT_BIN=/usr/local/bin/sbx-agent
fi
exec /usr/local/bin/sbx "$@"
