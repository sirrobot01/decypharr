PKG_NAME = decypharr
PKG_VERS ?= $(DECYPHARR_GIT_HASH)
ifeq ($(strip $(PKG_VERS)),)
PKG_VERS = dev
endif
PKG_EXT = tar.gz
PKG_DIST_NAME = $(PKG_NAME)-$(PKG_VERS).$(PKG_EXT)
PKG_DIST_SITE = https://github.com/sirrobot01/decypharr/archive
PKG_DIR = $(PKG_NAME)-$(PKG_VERS)

BUILD_DEPENDS = native/go
DEPENDS = cross/fuse

ARM32_SYNO_ARCHS = alpine alpine4k armada38x monaco dakota ipq806x
ifneq ($(filter $(ARCH),$(ARM32_SYNO_ARCHS)),)
DEPENDS += cross/rapidyenc
endif

CGO_ENABLED = 1

# cgofuse uses FUSE 2 headers on Linux by default. cross/fuse stages them under
# <staging>/include/fuse, outside the compiler's default sysroot include path.
CGO_CFLAGS = -I$(STAGING_INSTALL_PREFIX)/include/fuse -I$(STAGING_INSTALL_PREFIX)/include
CGO_LDFLAGS = -L$(STAGING_INSTALL_PREFIX)/lib
ENV += CGO_CFLAGS="$(CGO_CFLAGS)"
ENV += CGO_LDFLAGS="$(CGO_LDFLAGS)"

REQUIRED_MIN_DSM = 7.3
REQUIRED_MIN_SRM = 1.3
UNSUPPORTED_ARCHS = $(PPC_ARCHS) $(ARMv5_ARCHS)

HOMEPAGE = https://github.com/sirrobot01/decypharr
COMMENT = Media gateway for Debrid services and Usenet
LICENSE = MIT

GO_SRC_DIR = $(WORK_DIR)/$(PKG_DIR)
GO_BIN_DIR = $(GO_SRC_DIR)/decypharr

DECYPHARR_BUILD_VERSION ?= $(if $(DECYPHARR_VERSION),$(DECYPHARR_VERSION),0.0.0-dev)
GO_BUILD_ARGS += -trimpath
GO_LDFLAGS = -s -w -X github.com/sirrobot01/decypharr/pkg/version.Version=$(DECYPHARR_BUILD_VERSION) -X github.com/sirrobot01/decypharr/pkg/version.Channel=synology

PRE_COMPILE_TARGET = decypharr_pre_compile

.PHONY: decypharr_pre_compile
decypharr_pre_compile:
	@$(MSG) Apply Synology-specific FUSE compatibility profile
	@sh $(CURDIR)/src/patch-synology-fuse.sh "$(GO_SRC_DIR)"
	@mkdir -p $(GO_SRC_DIR)/internal/jsonx
	@cp $(CURDIR)/src/jsonx_64.go $(GO_SRC_DIR)/internal/jsonx/jsonx_64.go
	@cp $(CURDIR)/src/jsonx_32.go $(GO_SRC_DIR)/internal/jsonx/jsonx_32.go
	@find $(GO_SRC_DIR) -type f -name '*.go' -exec sed -i 's|json "github.com/bytedance/sonic"|json "github.com/sirrobot01/decypharr/internal/jsonx"|g' {} +
ifneq ($(filter $(ARCH),$(ARM32_SYNO_ARCHS)),)
	@$(MSG) Patch github.com/mnightingale/rapidyenc to link the Synology ARMv7 build
	@env $(ENV) sh $(CURDIR)/src/patch-rapidyenc.sh "$(GO_SRC_DIR)"
endif

include ../../mk/spksrc.cross-go.mk
