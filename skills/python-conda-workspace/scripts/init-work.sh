#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_ROOT="${HOME}/.aevitas/workspace"
WORK_ROOT="${HOME}/.aevitas/workspace/var/python-conda-workspace"
WORK_NAME="${1:-}"

if [ -z "${WORK_NAME}" ]; then
  echo "Usage: init-work.sh <workname>"
  exit 1
fi

if ! [[ "${WORK_NAME}" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  echo "ERROR: invalid workname. Use letters, numbers, _ or -"
  exit 1
fi

WORK_DIR="${WORK_ROOT}/${WORK_NAME}"
mkdir -p "${WORK_DIR}"

MAIN_PY="${WORK_DIR}/main.py"
if [ ! -f "${MAIN_PY}" ]; then
  cat > "${MAIN_PY}" <<'PY'
def main():
    print("hello from aevitas-workspace")


if __name__ == "__main__":
    main()
PY
fi

echo "Initialized: ${WORK_DIR}"
echo "Entry file: ${MAIN_PY}"
