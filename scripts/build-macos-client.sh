#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "${ROOT_DIR}/dist"
swiftc -O -o "${ROOT_DIR}/dist/DJ4HubClient" \
  "${ROOT_DIR}/cmd/dj4hub-client/ServiceManager.swift" "${ROOT_DIR}/cmd/dj4hub-client/main.swift"
echo "macOS menu-bar client written to ${ROOT_DIR}/dist/DJ4HubClient"
