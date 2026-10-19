#!/bin/sh
# Exercises the exact requirement the service-name-dns plan exists for:
# META_API_URL is a plain Compose-style service URL, never a node address
# or an injected *_HOST/*_PORT variable.
set -eu

if [ -z "${META_API_URL:-}" ]; then
	echo "FAIL: META_API_URL is not set"
	exit 1
fi

body=$(curl -fsS "$META_API_URL")
if [ "$body" != "metaapi-ok" ]; then
	echo "FAIL: expected 'metaapi-ok', got '$body'"
	exit 1
fi

echo "PASS: $META_API_URL -> $body"
