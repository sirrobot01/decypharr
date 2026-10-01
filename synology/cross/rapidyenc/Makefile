PKG_NAME = rapidyenc
PKG_VERS = 47f67f5ae31455a4e7bb2566fb2b6c3c1b0105e9
PKG_EXT = tar.gz
PKG_DIST_NAME = $(PKG_VERS).$(PKG_EXT)
PKG_DIST_SITE = https://github.com/animetosho/rapidyenc/archive
PKG_DIST_FILE = $(PKG_NAME)-$(PKG_VERS).$(PKG_EXT)
PKG_DIR = $(PKG_NAME)-$(PKG_VERS)

DEPENDS =

HOMEPAGE = https://github.com/animetosho/rapidyenc
COMMENT = Rapid yEnc encoder and decoder library
LICENSE = LGPL-2.1-or-later

# This package is only pulled by Decypharr's 32-bit ARM builds. Explicitly
# select ARM32 so CMake enables its ARMv7/NEON implementation while using the
# Synology cross compiler and sysroot supplied by spksrc.
CONFIGURE_ARGS += -DBUILD_NATIVE=OFF
CONFIGURE_ARGS += -DTARGET_ARCH=arm

# Only the static library is required by the Go/cgo wrapper.
COMPILE_ARGS = rapidyenc_static
INSTALL_TARGET = rapidyenc_install

include ../../mk/spksrc.cross-cmake.mk

.PHONY: rapidyenc_install
rapidyenc_install:
	@install -d -m 755 $(STAGING_INSTALL_PREFIX)/lib
	@install -d -m 755 $(STAGING_INSTALL_PREFIX)/include
	@install -m 644 $(BUILD_DIR)/rapidyenc_static/librapidyenc.a $(STAGING_INSTALL_PREFIX)/lib/librapidyenc.a
	@install -m 644 $(WORK_DIR)/$(PKG_DIR)/rapidyenc.h $(STAGING_INSTALL_PREFIX)/include/rapidyenc.h
