#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "${ROOT_DIR}"

FORMAT_ERRORS=$(gofmt -l cmd internal pkg)
if [ -n "${FORMAT_ERRORS}" ]; then
  printf 'Run gofmt on:\n%s\n' "${FORMAT_ERRORS}" >&2
  exit 1
fi
for script in scripts/*.sh packaging/dj4ghub packaging/install internal/sip/module_voice_prepare.sh; do
  sh -n "${script}"
done
if command -v zsh >/dev/null 2>&1; then
  zsh -n start-dj4hub.command stop-dj4hub.command
fi
if ! command -v node >/dev/null 2>&1; then
  echo "Node.js is required for repository and JavaScript checks." >&2
  exit 1
fi
node --check cmd/dj4ghub-macos/web/app.js
node --check scripts/check-web-ui.js
node --check scripts/check-demo.cjs
node --test scripts/check-repository.test.cjs
node scripts/check-repository.cjs
git diff --check
