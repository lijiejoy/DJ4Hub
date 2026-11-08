#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/dj4hub-client-tests.XXXXXX")
trap 'rm -rf "${TEST_DIR}"' EXIT
swiftc -o "${TEST_DIR}/tests" "${ROOT_DIR}/cmd/dj4hub-client/ServiceManager.swift" \
  "${ROOT_DIR}/cmd/dj4hub-client/tests/main.swift"
"${TEST_DIR}/tests"
