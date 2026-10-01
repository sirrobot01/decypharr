#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SPKSRC_REF="${SPKSRC_REF:-e353e52f6117f59326550ece07ca0c68903a57fc}"
SPKSRC_DIR="${SPKSRC_DIR:-${ROOT_DIR}/.synology-build/spksrc}"
SOURCE_SHA="${DECYPHARR_GIT_HASH:-$(git -C "${ROOT_DIR}" rev-parse HEAD)}"

mkdir -p "$(dirname "${SPKSRC_DIR}")"

if [ ! -d "${SPKSRC_DIR}/.git" ]; then
    git clone https://github.com/SynoCommunity/spksrc.git "${SPKSRC_DIR}"
fi

git -C "${SPKSRC_DIR}" fetch --depth=1 origin "${SPKSRC_REF}"
git -C "${SPKSRC_DIR}" checkout --detach FETCH_HEAD

# libfuse only needs a udev rules directory for build-time discovery. Avoid
# pulling libudev/util-linux into older Synology cross toolchains.
for fuse3_makefile in     "${SPKSRC_DIR}/cross/fuse3-latest/Makefile"     "${SPKSRC_DIR}/cross/fuse3.16/Makefile"
do
    sed -i '/^OPTIONAL_DEPENDS.*libudev/d' "${fuse3_makefile}"
    perl -0pi -e 's/\nifeq \(\$\(call version_ge, \$\{TCVERSION\}, 7\.0\),1\)\nDEPENDS = cross\/libudev_219\nelse\nDEPENDS = cross\/libudev_204\nendif\n/\n/g' "${fuse3_makefile}"

    if ! grep -q -- '-Dudevrulesdir=/usr/lib/udev/rules.d' "${fuse3_makefile}"; then
        sed -i '/CONFIGURE_ARGS += -Duseroot=false/a CONFIGURE_ARGS += -Dudevrulesdir=/usr/lib/udev/rules.d' "${fuse3_makefile}"
    fi
done

rm -rf "${SPKSRC_DIR}/cross/decypharr" "${SPKSRC_DIR}/cross/rapidyenc" "${SPKSRC_DIR}/spk/decypharr"
mkdir -p "${SPKSRC_DIR}/cross/decypharr" "${SPKSRC_DIR}/cross/rapidyenc" "${SPKSRC_DIR}/spk/decypharr/src"

cp -a "${ROOT_DIR}/synology/cross/decypharr/." "${SPKSRC_DIR}/cross/decypharr/"
cp -a "${ROOT_DIR}/synology/cross/rapidyenc/." "${SPKSRC_DIR}/cross/rapidyenc/"
cp -a "${ROOT_DIR}/synology/spk/decypharr/." "${SPKSRC_DIR}/spk/decypharr/"
cp "${ROOT_DIR}/docs/src/assets/logo.png" "${SPKSRC_DIR}/spk/decypharr/src/decypharr.png"

mkdir -p "${SPKSRC_DIR}/distrib"

RAPIDYENC_SHA="47f67f5ae31455a4e7bb2566fb2b6c3c1b0105e9"
RAPIDYENC_ARCHIVE="rapidyenc-${RAPIDYENC_SHA}.tar.gz"
RAPIDYENC_PATH="${SPKSRC_DIR}/distrib/${RAPIDYENC_ARCHIVE}"

if [ ! -f "${RAPIDYENC_PATH}" ]; then
    curl --fail --location --retry 3         "https://github.com/animetosho/rapidyenc/archive/${RAPIDYENC_SHA}.tar.gz"         --output "${RAPIDYENC_PATH}"
fi

{
    printf '%s SHA1 %s\n' "${RAPIDYENC_ARCHIVE}" "$(sha1sum "${RAPIDYENC_PATH}" | awk '{print $1}')"
    printf '%s SHA256 %s\n' "${RAPIDYENC_ARCHIVE}" "$(sha256sum "${RAPIDYENC_PATH}" | awk '{print $1}')"
    printf '%s MD5 %s\n' "${RAPIDYENC_ARCHIVE}" "$(md5sum "${RAPIDYENC_PATH}" | awk '{print $1}')"
} > "${SPKSRC_DIR}/cross/rapidyenc/digests"

ARCHIVE_NAME="decypharr-${SOURCE_SHA}.tar.gz"
ARCHIVE_PATH="${SPKSRC_DIR}/distrib/${ARCHIVE_NAME}"

git -C "${ROOT_DIR}" archive --format=tar --prefix="decypharr-${SOURCE_SHA}/" "${SOURCE_SHA}" |
    gzip -n -9 > "${ARCHIVE_PATH}"

{
    printf '%s SHA1 %s\n' "${ARCHIVE_NAME}" "$(sha1sum "${ARCHIVE_PATH}" | awk '{print $1}')"
    printf '%s SHA256 %s\n' "${ARCHIVE_NAME}" "$(sha256sum "${ARCHIVE_PATH}" | awk '{print $1}')"
    printf '%s MD5 %s\n' "${ARCHIVE_NAME}" "$(md5sum "${ARCHIVE_PATH}" | awk '{print $1}')"
} > "${SPKSRC_DIR}/cross/decypharr/digests"

cat <<EOF
spksrc prepared in: ${SPKSRC_DIR}
source commit:       ${SOURCE_SHA}

Build example:
  DECYPHARR_GIT_HASH=${SOURCE_SHA} DECYPHARR_VERSION=0.0.0-dev \
    make -C "${SPKSRC_DIR}/spk/decypharr" arch-avoton-7.3 SPK_PACKAGE_ARCHS=avoton
EOF
