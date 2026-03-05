#!/usr/bin/env bash
set -euo pipefail

ENV_NAME="aevitas-workspace"
WORK_ROOT="${HOME}/.aevitas/workspace/var/python-conda-workspace"
WORK_NAME=""
MODE=""
FILE_ARG=""
CODE_ARG=""
EXTRA_ARGS=()

if [ "$#" -lt 4 ]; then
  echo "Usage:"
  echo "  run.sh --work <workname> --file <python_file> [args...]"
  echo "  run.sh --work <workname> --code \"<python_code>\""
  exit 1
fi

while [ "$#" -gt 0 ]; do
  case "$1" in
    --work)
      WORK_NAME="${2:-}"
      shift 2
      ;;
    --file)
      MODE="file"
      FILE_ARG="${2:-}"
      shift 2
      EXTRA_ARGS=("$@")
      break
      ;;
    --code)
      MODE="code"
      CODE_ARG="${2:-}"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1"
      exit 1
      ;;
  esac
done

if [ -z "${WORK_NAME}" ]; then
  echo "ERROR: --work is required"
  exit 1
fi

WORK_DIR="${WORK_ROOT}/${WORK_NAME}"
if [ ! -d "${WORK_DIR}" ]; then
  echo "ERROR: work directory not found: ${WORK_DIR}"
  echo "Run init-work.sh ${WORK_NAME} first"
  exit 1
fi
WORK_DIR="$(cd "${WORK_DIR}" && pwd)"

case "${MODE}" in
  file)
    if [ -z "${FILE_ARG}" ]; then
      echo "ERROR: --file requires a python file path"
      exit 1
    fi
    if [[ "${FILE_ARG}" = /* ]]; then
      TARGET_PATH="${FILE_ARG}"
    else
      TARGET_PATH="${WORK_DIR}/${FILE_ARG}"
    fi

    if [ ! -f "${TARGET_PATH}" ]; then
      echo "ERROR: python file not found: ${TARGET_PATH}"
      exit 1
    fi

    TARGET_DIR="$(cd "$(dirname "${TARGET_PATH}")" && pwd)"
    TARGET_REAL="${TARGET_DIR}/$(basename "${TARGET_PATH}")"
    case "${TARGET_REAL}" in
      "${WORK_DIR}"/*) ;;
      *)
        echo "ERROR: file must be inside ${WORK_DIR}"
        exit 1
        ;;
    esac

    (
      cd "${WORK_DIR}"
      AEVITAS_PY_WORK_DIR="${WORK_DIR}" conda run -n "${ENV_NAME}" python "${TARGET_REAL}" "${EXTRA_ARGS[@]}"
    )
    ;;
  code)
    if [ -z "${CODE_ARG}" ]; then
      echo "ERROR: --code requires inline code string"
      exit 1
    fi
    (
      cd "${WORK_DIR}"
      AEVITAS_PY_WORK_DIR="${WORK_DIR}" conda run -n "${ENV_NAME}" python -c "${CODE_ARG}"
    )
    ;;
  *)
    echo "ERROR: either --file or --code is required"
    exit 1
    ;;
esac
