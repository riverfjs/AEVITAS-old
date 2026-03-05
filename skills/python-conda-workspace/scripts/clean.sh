#!/usr/bin/env bash
set -euo pipefail

WORK_ROOT="${HOME}/.aevitas/workspace/var/python-conda-workspace"
WORK_NAME="${1:-}"
MODE="${2:-}"

if [ -z "${WORK_NAME}" ] || [ "${MODE}" != "--all" ]; then
  echo "Usage: clean.sh <workname> --all"
  exit 1
fi

WORK_DIR="${WORK_ROOT}/${WORK_NAME}"
if [ ! -d "${WORK_DIR}" ]; then
  echo "ERROR: work directory not found: ${WORK_DIR}"
  exit 1
fi

rm -rf "${WORK_DIR:?}/"* "${WORK_DIR:?}"/.[!.]* "${WORK_DIR:?}"/..?* 2>/dev/null || true

echo "Cleaned all contents under: ${WORK_DIR}"
