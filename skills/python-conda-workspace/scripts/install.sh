#!/usr/bin/env bash
set -euo pipefail

ENV_NAME="aevitas-workspace"
WORK_ROOT="${HOME}/.aevitas/workspace/var/python-conda-workspace"
WORK_NAME=""
MODE="pip"

if [ "$#" -lt 1 ]; then
  echo "Usage: install.sh [--work <workname>] [--conda] <package...>"
  exit 1
fi

if ! command -v conda >/dev/null 2>&1; then
  echo "ERROR: conda not found in PATH"
  exit 1
fi

while [ "$#" -gt 0 ]; do
  case "$1" in
    --work)
      if [ "$#" -lt 2 ]; then
        echo "Usage: install.sh [--work <workname>] [--conda] <package...>"
        exit 1
      fi
      WORK_NAME="$2"
      shift 2
      ;;
    --conda)
      MODE="conda"
      shift
      ;;
    *)
      break
      ;;
  esac
done

if [ "$#" -lt 1 ]; then
  echo "Usage: install.sh [--work <workname>] [--conda] <package...>"
  exit 1
fi

if [ -n "${WORK_NAME}" ]; then
  WORK_DIR="${WORK_ROOT}/${WORK_NAME}"
  if [ ! -d "${WORK_DIR}" ]; then
    echo "ERROR: work directory not found: ${WORK_DIR}"
    echo "Run init-work.sh ${WORK_NAME} first"
    exit 1
  fi
fi

if [ "${MODE}" = "conda" ]; then
  echo "Installing conda packages into ${ENV_NAME}: $*"
  conda install -n "${ENV_NAME}" -c conda-forge "$@" -y
  echo "Conda install complete"
else
  echo "Installing pip packages into ${ENV_NAME}: $*"
  conda run -n "${ENV_NAME}" python -m pip install "$@"
  echo "Pip install complete"
fi
