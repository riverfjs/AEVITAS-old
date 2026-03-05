#!/usr/bin/env bash
set -euo pipefail

ENV_NAME="aevitas-workspace"
PY_VER="${PY_VER:-3.11}"
WORKSPACE_ROOT="${HOME}/.aevitas/workspace"
WORK_ROOT="${HOME}/.aevitas/workspace/var/python-conda-workspace"

if ! command -v conda >/dev/null 2>&1; then
  echo "ERROR: conda not found in PATH"
  exit 1
fi

# Avoid conda implicit-channel warning by explicitly setting defaults once.
if ! conda config --show channels 2>/dev/null | awk '/^- /{print $2}' | grep -Fxq "defaults"; then
  echo "Adding conda channel: defaults"
  conda config --add channels defaults >/dev/null
fi

if conda env list | awk '{print $1}' | grep -Fxq "${ENV_NAME}"; then
  echo "Conda env '${ENV_NAME}' already exists"
else
  echo "Creating conda env '${ENV_NAME}' (python=${PY_VER})..."
  conda create -n "${ENV_NAME}" "python=${PY_VER}" -y
fi

mkdir -p "${WORK_ROOT}"

echo "Upgrading pip/setuptools/wheel..."
conda run -n "${ENV_NAME}" python -m pip install --upgrade pip setuptools wheel

echo "Bootstrap complete: ${ENV_NAME}"
echo "Workspace root: ${WORKSPACE_ROOT}"
echo "Work root: ${WORK_ROOT}"
