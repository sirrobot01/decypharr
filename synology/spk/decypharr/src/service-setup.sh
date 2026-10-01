# Decypharr service setup for Synology DSM/SRM.
#
# Keep immutable application files below SYNOPKG_PKGDEST and persistent data
# below SYNOPKG_PKGVAR.

PATH="${SYNOPKG_PKGDEST}/bin:${PATH}"
DECYPHARR="${SYNOPKG_PKGDEST}/bin/decypharr"

HOME_DIR="${SYNOPKG_PKGVAR}"
CONFIG_DIR="${SYNOPKG_PKGVAR}/data"
CACHE_DIR="${SYNOPKG_PKGVAR}/cache"
LOG_DIR="${SYNOPKG_PKGVAR}/logs"

SERVICE_COMMAND="env PATH=${PATH} HOME=${HOME_DIR} XDG_CACHE_HOME=${CACHE_DIR} LOG_PATH=${LOG_DIR} LD_LIBRARY_PATH=${SYNOPKG_PKGDEST}/lib UMASK=022 ${DECYPHARR} --config ${CONFIG_DIR}"

SVC_BACKGROUND=y
SVC_WRITE_PID=y
SVC_WAIT_TIMEOUT=120

create_decypharr_directories()
{
    mkdir -p "${CONFIG_DIR}" "${CACHE_DIR}" "${LOG_DIR}"
}

service_postinst()
{
    create_decypharr_directories
}

service_postupgrade()
{
    create_decypharr_directories
}
