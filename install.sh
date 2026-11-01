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
    hacoctl start

  On every OTHER machine you want as a worker node, once the control
  plane above is running (it prints a one-time join command/token too):
    hacoctl agent join '<token-from-hacoctl-start-output>'   # first time only
    hacoctl agent run                                        # later restarts (reuses the saved credential, no token needed)
EOF
