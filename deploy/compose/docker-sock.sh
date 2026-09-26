#!/bin/sh
# Print the container-engine socket to mount into the docker-proxy service.
# Override with GOAP_DOCKER_SOCK. Handles docker, rootless docker, podman
# (rootful/rootless) and Docker Desktop (macOS/Windows: the socket lives in
# the VM, so the default path is the right one).
usable() { [ -S "$1" ] && [ -r "$1" ] && [ -w "$1" ]; }

[ -n "$GOAP_DOCKER_SOCK" ] && { echo "$GOAP_DOCKER_SOCK"; exit 0; }

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*|Darwin) echo /var/run/docker.sock; exit 0 ;;
esac

case "$DOCKER_HOST" in
  unix://*) s=${DOCKER_HOST#unix://}; usable "$s" && { echo "$s"; exit 0; } ;;
esac

rt=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
for s in /var/run/docker.sock "$rt/docker.sock" "$rt/podman/podman.sock" /run/podman/podman.sock; do
  usable "$s" && { echo "$s"; exit 0; }
done

echo "docker-sock: no usable engine socket found (for podman: systemctl --user enable --now podman.socket)" >&2
echo /var/run/docker.sock
