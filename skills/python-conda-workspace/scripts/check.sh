#!/usr/bin/env bash
set -euo pipefail

ENV_NAME="aevitas-workspace"
WORKSPACE_ROOT="${HOME}/.aevitas/workspace"
WORK_ROOT="${HOME}/.aevitas/workspace/var/python-conda-workspace"

if ! command -v conda >/dev/null 2>&1; then
  echo "ERROR: conda not found in PATH"
  exit 1
fi

echo "== conda env =="
conda env list | awk '{print $1}' | grep -Fxq "${ENV_NAME}" || {
  echo "ERROR: env '${ENV_NAME}' not found"
  exit 1
}

echo "== python =="
conda run -n "${ENV_NAME}" python - <<'PY'
import sys
print("python:", sys.version.split()[0])
print("executable:", sys.executable)
PY

echo "== pip =="
conda run -n "${ENV_NAME}" python -m pip --version

mkdir -p "${WORK_ROOT}"
echo "== workspace =="
echo "${WORKSPACE_ROOT}"
echo "== var/python-conda-workspace =="
echo "${WORK_ROOT}"
if [ -n "$(ls -A "${WORK_ROOT}" 2>/dev/null)" ]; then
  echo "projects:"
  ls -1 "${WORK_ROOT}"
else
  echo "projects: (empty)"
fi

echo "Health check passed"
