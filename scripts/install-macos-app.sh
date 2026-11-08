#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SOURCE=${1:-"${ROOT_DIR}/dist/DJ 4G Hub.app"}
TARGET=${2:-"/Applications/DJ 4G Hub.app"}
codesign --verify --deep --strict "${SOURCE}"
BUILD_DIR=$(mktemp -d "${TMPDIR:-/tmp}/dj4hub-install.XXXXXX")
trap 'rm -rf "${BUILD_DIR}"' EXIT
swiftc -O -o "${BUILD_DIR}/install-app" \
  "${ROOT_DIR}/cmd/dj4hub-client/ServiceManager.swift" \
  "${ROOT_DIR}/cmd/dj4hub-client/install/main.swift"
"${BUILD_DIR}/install-app" "${SOURCE}" "${TARGET}"
