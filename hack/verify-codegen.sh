#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail
ROOT=$(unset CDPATH && cd $(dirname "${BASH_SOURCE[0]}")/.. && pwd)

DIFFROOT="${ROOT}/api"
TMP_DIFFROOT="${ROOT}/_tmp/api"
_tmp="${ROOT}/_tmp"

cleanup() {
  rm -rf "${_tmp}"
}
trap "cleanup" EXIT SIGINT

cleanup

mkdir -p "${TMP_DIFFROOT}"
cp -a "${DIFFROOT}"/* "${TMP_DIFFROOT}"

make generate
echo "diffing ${DIFFROOT} against freshly generated codegen"
ret=0
diff -Naupr "${DIFFROOT}" "${TMP_DIFFROOT}" || ret=$?
cp -a "${TMP_DIFFROOT}"/* "${DIFFROOT}"
if [[ $ret -eq 0 ]]; then
  echo "${DIFFROOT} up to date."
else
  echo "${DIFFROOT} is out of date. Please run make generate"
  exit 1
fi
