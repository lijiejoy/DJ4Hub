#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TARGET=${1:?Pass the app licenses directory}
MODULE_LIST=$(mktemp "${TMPDIR:-/tmp}/dj4hub-go-licenses.XXXXXX")
trap 'rm -f "${MODULE_LIST}"' EXIT
cd "${ROOT_DIR}"
CGO_ENABLED=1 go list -deps -f '{{with .Module}}{{.Path}}{{"\t"}}{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{end}}' \
  ./cmd/dj4ghub-macos > "${MODULE_LIST}"
sort -u "${MODULE_LIST}" | while IFS="$(printf '\t')" read -r module_path module_dir; do
  [ -n "${module_path}" ] && [ -d "${module_dir}" ] || continue
  label=$(printf '%s' "${module_path}" | tr '/' '_')
  for license in "${module_dir}"/LICENSE* "${module_dir}"/COPYING* "${module_dir}"/NOTICE*; do
    [ -f "${license}" ] || continue
    mkdir -p "${TARGET}/go/${label}"
    cp "${license}" "${TARGET}/go/${label}/"
  done
done
