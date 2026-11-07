#!/usr/bin/env bash
# Builds and installs controlplane, agent, and haco onto this machine's
# $GOBIN (usually ~/go/bin), so `haco`/`agent`/`controlplane` work from
# any directory without a ./bin/ prefix.
set -euo pipefail

if ! command -v go >/dev/null 2>&1; then
	echo "error: go is not installed or not on PATH (https://go.dev/dl/)" >&2
	exit 1
fi

cd "$(dirname "${BASH_SOURCE[0]}")"

echo "Installing controlplane, agent, haco..."
go install ./cmd/controlplane
go install ./cmd/agent
go install ./cmd/haco

GOBIN="$(go env GOBIN)"
if [ -z "$GOBIN" ]; then
	GOBIN="$(go env GOPATH)/bin"
fi

echo "Installed to $GOBIN"

# The CLI used to be called hacoctl; drop the old binary this script
# installed, so the stale one can't be run by mistake.
if [ -f "$GOBIN/hacoctl" ]; then
	rm -f "$GOBIN/hacoctl"
	echo "Removed the old hacoctl binary (the command is now 'haco')"
fi

# The web console (served by 'haco start --web') is built into the
# control plane's data dir. It needs npm; without it the CLI still works.
WEB_DEST="${HACO_DATA_DIR:-$HOME/.haco}/web"
if command -v npm >/dev/null 2>&1; then
	echo "Building the web console..."
	(cd web && npm ci --no-audit --no-fund --loglevel=error && npm run build --silent)
	mkdir -p "$WEB_DEST"
	rm -rf "${WEB_DEST:?}"/*
	cp -R web/dist/. "$WEB_DEST/"
	echo "Web console installed to $WEB_DEST (serve it with 'haco start --web')"
else
	echo "npm not found: skipping the web console ('haco start --web' needs it)"
fi

case ":$PATH:" in
*":$GOBIN:"*)
	;;
*)
	echo
	echo "$GOBIN is not on your PATH yet. Add it, e.g.:"
	echo "  echo 'export PATH=\"\$PATH:$GOBIN\"' >> ~/.zshrc && source ~/.zshrc"
	;;
esac

cat <<'EOF'

Next steps:
  On this machine (runs the control plane and its own node):
    haco start            # add --web to also serve the web console

  On every OTHER machine you want as a worker node, once the control
  plane above is running (it prints a one-time join command/token too):
    haco agent join '<token-from-haco-start-output>'   # first time only
    haco agent run                                        # later restarts (reuses the saved credential, no token needed)
EOF
