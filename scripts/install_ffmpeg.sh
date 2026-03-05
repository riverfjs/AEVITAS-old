#!/bin/bash

set -euo pipefail

INSTALL_DIR="${1:-${HOME}/.aevitas/bin}"
STATE_FILE="${INSTALL_DIR}/.ffmpeg-release"
LEGACY_STATUS_FILE="${INSTALL_DIR}/.ffmpeg-install-status"

mkdir -p "${INSTALL_DIR}"
rm -f "${LEGACY_STATUS_FILE}" 2>/dev/null || true

OS="$(uname -s)"
ARCH="$(uname -m)"

if [ "${OS}" != "Darwin" ]; then
  echo "⚠️ Auto ffmpeg download currently supports macOS only (detected ${OS})."
  echo "Please place ffmpeg and ffprobe in ${INSTALL_DIR} manually."
  exit 1
fi

case "${ARCH}" in
  arm64)
    FFMPEG_URL="https://ffmpeg.martin-riedl.de/download/macos/arm64/1712343170_7.0/ffmpeg.zip"
    FFPROBE_URL="https://ffmpeg.martin-riedl.de/download/macos/arm64/1712343170_7.0/ffprobe.zip"
    ;;
  x86_64)
    FFMPEG_URL="https://evermeet.cx/ffmpeg/getrelease/zip"
    FFPROBE_URL="https://evermeet.cx/ffmpeg/getrelease/ffprobe/zip"
    ;;
  *)
    echo "❌ Unsupported macOS architecture: ${ARCH}"
    exit 1
    ;;
esac

REMOTE_FFMPEG="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${FFMPEG_URL}" || echo "${FFMPEG_URL}")"
REMOTE_FFPROBE="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${FFPROBE_URL}" || echo "${FFPROBE_URL}")"
REMOTE_RELEASE="ffmpeg=${REMOTE_FFMPEG}|ffprobe=${REMOTE_FFPROBE}"

if [ -x "${INSTALL_DIR}/ffmpeg" ] && [ -x "${INSTALL_DIR}/ffprobe" ]; then
  if [ -f "${STATE_FILE}" ] && [ "$(cat "${STATE_FILE}")" = "${REMOTE_RELEASE}" ]; then
    echo "✓ ffmpeg tools already exist and are up-to-date, skip update"
    exit 0
  fi
  if [ ! -f "${STATE_FILE}" ]; then
    # Binaries already exist from previous installs; initialize fingerprint and skip.
    printf "%s" "${REMOTE_RELEASE}" > "${STATE_FILE}"
    echo "✓ ffmpeg tools already exist; fingerprint initialized, skip update"
    exit 0
  fi
  UPDATE_MODE=1
else
  UPDATE_MODE=0
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

curl -fL "${FFMPEG_URL}" -o "${TMP_DIR}/ffmpeg.zip"
curl -fL "${FFPROBE_URL}" -o "${TMP_DIR}/ffprobe.zip"
unzip -qo "${TMP_DIR}/ffmpeg.zip" -d "${TMP_DIR}/ffmpeg"
unzip -qo "${TMP_DIR}/ffprobe.zip" -d "${TMP_DIR}/ffprobe"

if [ ! -f "${TMP_DIR}/ffmpeg/ffmpeg" ] || [ ! -f "${TMP_DIR}/ffprobe/ffprobe" ]; then
  echo "❌ Downloaded archive missing ffmpeg/ffprobe binaries"
  exit 1
fi

cp "${TMP_DIR}/ffmpeg/ffmpeg" "${INSTALL_DIR}/ffmpeg"
cp "${TMP_DIR}/ffprobe/ffprobe" "${INSTALL_DIR}/ffprobe"
chmod +x "${INSTALL_DIR}/ffmpeg" "${INSTALL_DIR}/ffprobe"
printf "%s" "${REMOTE_RELEASE}" > "${STATE_FILE}"

if [ "${UPDATE_MODE}" -eq 1 ]; then
  echo "✓ ffmpeg tools updated to latest release"
else
  echo "✓ ffmpeg tools installed to ${INSTALL_DIR}/ffmpeg and ${INSTALL_DIR}/ffprobe"
fi
