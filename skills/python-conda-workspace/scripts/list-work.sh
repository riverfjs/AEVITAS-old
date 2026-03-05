#!/usr/bin/env bash
set -euo pipefail

WORK_ROOT="${HOME}/.aevitas/workspace/var/python-conda-workspace"

mkdir -p "${WORK_ROOT}"

if [ -n "$(ls -A "${WORK_ROOT}" 2>/dev/null)" ]; then
  ls -1 "${WORK_ROOT}"
else
  echo "(empty)"
fi
