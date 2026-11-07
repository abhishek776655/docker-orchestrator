#!/usr/bin/env bash
# Builds and installs controlplane, agent, and hacoctl onto this machine's
# $GOBIN (usually ~/go/bin), so `hacoctl`/`agent`/`controlplane` work from
# any directory without a ./bin/ prefix.
set -euo pipefail

if ! command -v go >/dev/null 2>&1; then
	echo "error: go is not installed or not on PATH (https://go.dev/dl/)" >&2
	exit 1
fi

cd "$(dirname "${BASH_SOURCE[0]}")"

echo "Installing controlplane, agent, hacoctl..."
go install ./cmd/controlplane
go install ./cmd/agent
go install ./cmd/hacoctl

GOBIN="$(go env GOBIN)"
if [ -z "$GOBIN" ]; then
	GOBIN="$(go env GOPATH)/bin"
fi

echo "Installed to $GOBIN"

# The web console (served by 'hacoctl start --web') is built into the
# control plane's data dir. It needs npm; without it the CLI still works.
WEB_DEST="${HACO_DATA_DIR:-$HOME/.haco}/web"
if command -v npm >/dev/null 2>&1; then
	echo "Building the web console..."
	(cd web && npm ci --no-audit --no-fund --loglevel=error && npm run build --silent)
	mkdir -p "$WEB_DEST"
	rm -rf "${WEB_DEST:?}"/*
	cp -R web/dist/. "$WEB_DEST/"
	echo "Web console installed to $WEB_DEST (serve it with 'hacoctl start --web')"
else
	echo "npm not found: skipping the web console ('hacoctl start --web' needs it)"
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
    hacoctl start            # add --web to also serve the web console

  On every OTHER machine you want as a worker node, once the control
  plane above is running (it prints a one-time join command/token too):
    hacoctl agent join '<token-from-hacoctl-start-output>'   # first time only
    hacoctl agent run                                        # later restarts (reuses the saved credential, no token needed)
EOF
