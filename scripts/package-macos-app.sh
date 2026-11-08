#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${1:-dev}
case "${VERSION}" in
  ""|*[!A-Za-z0-9._-]*) echo "Version must contain only letters, digits, dots, underscores or hyphens." >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) echo "Unsupported Mac architecture." >&2; exit 1 ;;
esac
if [ "$(go env GOARCH)" != "${ARCH}" ]; then
  echo "Build on the target Mac architecture; unset GOARCH before packaging." >&2
  exit 1
fi
NUMERIC_VERSION=$(printf '%s' "${VERSION}" | sed -E 's/^v//; s/[-+].*$//')
if printf '%s' "${NUMERIC_VERSION}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  DJ4HUB_APP_VERSION="${NUMERIC_VERSION}"
  export DJ4HUB_APP_VERSION
fi
sh "${ROOT_DIR}/scripts/build-macos-app.sh"
RELEASE_DIR="${ROOT_DIR}/dist/release"
mkdir -p "${RELEASE_DIR}"
NAME="DJ-4G-Hub-App-macOS-${ARCH}-${VERSION}.zip"
ditto -c -k --keepParent --norsrc --noextattr --noqtn --noacl \
  "${ROOT_DIR}/dist/DJ 4G Hub.app" "${RELEASE_DIR}/${NAME}"
(
  cd "${RELEASE_DIR}"
  shasum -a 256 "${NAME}" > "${NAME}.sha256"
)
echo "App archive: ${RELEASE_DIR}/${NAME}"
