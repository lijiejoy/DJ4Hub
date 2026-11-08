#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
APP="${ROOT_DIR}/dist/DJ 4G Hub.app"
IDENTITY=${DJ4HUB_SIGN_IDENTITY:--}

sh "${ROOT_DIR}/scripts/build-macos.sh"
sh "${ROOT_DIR}/scripts/build-macos-client.sh"

ADB_BIN=${DJ4HUB_ADB_BIN:-$(command -v adb || true)}
if [ ! -x "${ADB_BIN}" ]; then
  echo "ADB is required for the bundled voice service; set DJ4HUB_ADB_BIN." >&2
  exit 1
fi
ADB_NOTICE=${DJ4HUB_ADB_NOTICE:-$(dirname "$(realpath "${ADB_BIN}")")/NOTICE.txt}
if [ ! -f "${ADB_NOTICE}" ]; then
  echo "Set DJ4HUB_ADB_NOTICE to the platform-tools NOTICE.txt." >&2
  exit 1
fi
LIBUSB_BIN=$(otool -L "${ROOT_DIR}/dist/dj4ghub-macos" | awk '/libusb.*dylib / {print $1; exit}')
LIBUSB_LICENSE=${DJ4HUB_LIBUSB_LICENSE:-$(pkg-config --variable=prefix libusb-1.0)/COPYING}
if [ ! -f "${LIBUSB_BIN}" ] || [ ! -f "${LIBUSB_LICENSE}" ]; then
  echo "Cannot locate libusb and its license; set DJ4HUB_LIBUSB_LICENSE if needed." >&2
  exit 1
fi

rm -rf "${APP}"
mkdir -p "${APP}/Contents/MacOS" "${APP}/Contents/Frameworks" "${APP}/Contents/Resources/licenses"
cp "${ROOT_DIR}/packaging/macos/Info.plist" "${APP}/Contents/Info.plist"
if [ -n "${DJ4HUB_APP_VERSION:-}" ]; then
  if ! printf '%s' "${DJ4HUB_APP_VERSION}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "DJ4HUB_APP_VERSION must be a numeric major.minor.patch version." >&2
    exit 1
  fi
  plutil -replace CFBundleShortVersionString -string "${DJ4HUB_APP_VERSION}" "${APP}/Contents/Info.plist"
  plutil -replace CFBundleVersion -string "${DJ4HUB_APP_VERSION}" "${APP}/Contents/Info.plist"
fi
cp "${ROOT_DIR}/dist/DJ4HubClient" "${ROOT_DIR}/dist/dj4ghub-macos" "${APP}/Contents/MacOS/"
cp "${ADB_BIN}" "${APP}/Contents/MacOS/adb"
cp "${LIBUSB_BIN}" "${APP}/Contents/Frameworks/libusb-1.0.0.dylib"
cp "${LIBUSB_LICENSE}" "${APP}/Contents/Resources/licenses/libusb-COPYING"
cp "${ADB_NOTICE}" "${APP}/Contents/Resources/licenses/platform-tools-NOTICE.txt"
cp "${ROOT_DIR}/LICENSE" "${APP}/Contents/Resources/LICENSE"
cp "${ROOT_DIR}/THIRD_PARTY_NOTICES.md" "${APP}/Contents/Resources/THIRD_PARTY_NOTICES.md"
sh "${ROOT_DIR}/scripts/copy-go-licenses.sh" "${APP}/Contents/Resources/licenses"

ICONSET=$(mktemp -d "${TMPDIR:-/tmp}/dj4hub-icon.XXXXXX")
trap 'rm -rf "${ICONSET}"' EXIT
mkdir "${ICONSET}/DJ4Hub.iconset"
for size in 16 32 128 256 512; do
  sips -z "${size}" "${size}" "${ROOT_DIR}/cmd/dj4ghub-macos/web/app-icon.png" \
    --out "${ICONSET}/DJ4Hub.iconset/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "${double}" "${double}" "${ROOT_DIR}/cmd/dj4ghub-macos/web/app-icon.png" \
    --out "${ICONSET}/DJ4Hub.iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "${ICONSET}/DJ4Hub.iconset" -o "${APP}/Contents/Resources/DJ4Hub.icns"

# Build hosts and prebuilt dependencies can require a newer OS than the
# source template. Never advertise an OS older than an included Mach-O needs.
MIN_OS=13.0
for executable in "${APP}/Contents/MacOS/"* "${APP}/Contents/Frameworks/"*; do
  BUILD_VERSION=$(vtool -show-build "${executable}")
  MIN_OS=$(printf '%s\nminos %s\n' "${BUILD_VERSION}" "${MIN_OS}" | \
    awk 'BEGIN { maximum=0; version="13.0" }
         /cmd LC_VERSION_MIN_MACOSX/ { legacy=1 }
         /cmd LC_BUILD_VERSION/ { legacy=0 }
         /minos / || (legacy && $1=="version") {
           split($2, parts, "."); value=parts[1]*10000+parts[2]*100+parts[3];
           if (value>maximum) { maximum=value; version=$2 } }
         END { print version }')
done
plutil -replace LSMinimumSystemVersion -string "${MIN_OS}" "${APP}/Contents/Info.plist"
plutil -create xml1 "${APP}/Contents/Resources/BuildInfo.plist"
plutil -insert LibUSBVersion -string "$(pkg-config --modversion libusb-1.0)" "${APP}/Contents/Resources/BuildInfo.plist"
plutil -insert Architecture -string "$(go env GOARCH)" "${APP}/Contents/Resources/BuildInfo.plist"
plutil -insert MinimumSystemVersion -string "${MIN_OS}" "${APP}/Contents/Resources/BuildInfo.plist"

install_name_tool -change "${LIBUSB_BIN}" \
  '@executable_path/../Frameworks/libusb-1.0.0.dylib' "${APP}/Contents/MacOS/dj4ghub-macos"
install_name_tool -id '@rpath/libusb-1.0.0.dylib' "${APP}/Contents/Frameworks/libusb-1.0.0.dylib"
for executable in "${APP}/Contents/Frameworks/"* \
  "${APP}/Contents/MacOS/adb" "${APP}/Contents/MacOS/dj4ghub-macos" \
  "${APP}/Contents/MacOS/DJ4HubClient"; do
  if otool -L "${executable}" | awk 'NR > 1 {print $1}' | \
    rg -q '^(/usr/local|/opt/homebrew|/.*Cellar/)'; then
    echo "Unbundled dependency in ${executable}" >&2
    exit 1
  fi
  codesign --force --sign "${IDENTITY}" "${executable}"
done
codesign --force --sign "${IDENTITY}" "${APP}"
codesign --verify --deep --strict "${APP}"
echo "App written to ${APP}"
echo "Minimum macOS version: ${MIN_OS}"
