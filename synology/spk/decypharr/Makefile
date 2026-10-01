SPK_NAME = decypharr
SPK_VERS ?= $(if $(DECYPHARR_VERSION),$(DECYPHARR_VERSION),0.0.0-dev)
SPK_REV ?= 1
SPK_ICON = src/decypharr.png

DEPENDS = cross/decypharr
DEPENDS += cross/rclone
DEPENDS += cross/fuse
DEPENDS += cross/fuse3

MAINTAINER = sirrobot01
MAINTAINER_URL = https://github.com/sirrobot01
DESCRIPTION = Decypharr is a media gateway for Debrid services and Usenet, providing qBittorrent and SABnzbd compatible APIs for Sonarr, Radarr and other Arr applications.
DISPLAY_NAME = Decypharr
HOMEPAGE = https://github.com/sirrobot01/decypharr
HELPURL = https://docs.decypharr.com
LICENSE = MIT

STARTABLE = yes
SERVICE_USER = auto
SERVICE_SETUP = src/service-setup.sh
SPK_COMMANDS = bin/decypharr-fuse-fix
CONF_DIR = src/conf

SERVICE_PORT = 8282
SERVICE_PORT_TITLE = Decypharr Web UI
ADMIN_PORT = $(SERVICE_PORT)

REQUIRED_MIN_DSM = 7.3
REQUIRED_MIN_SRM = 1.3

# DSM 7.3.1 build 86003 is the oldest runtime target validated by this package.
ifeq ($(TCVERSION),7.3)
OS_MIN_VER = 7.3-86003
endif

UNSUPPORTED_ARCHS = $(PPC_ARCHS) $(ARMv5_ARCHS)
USE_ALTERNATE_TMPDIR = 1
POST_STRIP_TARGET = decypharr_extra_install

include ../../mk/spksrc.common.mk
include ../../mk/spksrc.spk.mk

.PHONY: decypharr_extra_install
decypharr_extra_install:
	@install -d -m 755 $(STAGING_DIR)/etc
	@install -m 644 $(CURDIR)/src/fuse.conf $(STAGING_DIR)/etc/fuse.conf
	@install -m 755 $(CURDIR)/src/decypharr-fuse-fix $(STAGING_DIR)/bin/decypharr-fuse-fix
