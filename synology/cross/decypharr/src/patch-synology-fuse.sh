#!/bin/sh
set -eu

GO_SRC_DIR="$1"
GO_MOD="${GO_SRC_DIR}/go.mod"
GO_SUM="${GO_SRC_DIR}/go.sum"
BACKEND="${GO_SRC_DIR}/pkg/mount/dfs/backend/hanwen/backend.go"

if [ ! -f "${GO_MOD}" ] || [ ! -f "${GO_SUM}" ] || [ ! -f "${BACKEND}" ]; then
    echo "Synology FUSE patch: expected Decypharr source files are missing" >&2
    exit 1
fi

# Older Synology DSM kernels (notably Linux 3.10 on Avoton) can leave newer
# go-fuse mounts disconnected after FUSE_INIT. Keep this pin local to the SPK
# build so normal Linux/Docker builds continue using upstream's dependency.
sed -i -E 's|github.com/hanwen/go-fuse/v2 v[^[:space:]]+|github.com/hanwen/go-fuse/v2 v2.5.1|' "${GO_MOD}"
sed -i '/github.com\/hanwen\/go-fuse\/v2 /d' "${GO_SUM}"
cat >> "${GO_SUM}" <<'EOF'
github.com/hanwen/go-fuse/v2 v2.5.1 h1:OQBE8zVemSocRxA4OaFJbjJ5hlpCmIWbGr7r0M4uoQQ=
github.com/hanwen/go-fuse/v2 v2.5.1/go.mod h1:xKwi1cF7nXAOBCXujD5ie0ZKsxc8GGSA1rlMJc+8IJs=
EOF

# go-fuse v2.5.1 predates MountOptions.PanicHandler. beta currently uses it,
# so remove only that option from the packaging copy.
if grep -q 'PanicHandler:' "${BACKEND}"; then
    sed -i '/"runtime\/debug"/d' "${BACKEND}"
    perl -0pi -e 's/\n\t\t\/\/ Route handler panics.*?\n\t\tPanicHandler: func\(p any\) fuse\.Status \{.*?\n\t\t\},//s' "${BACKEND}"
fi

# Conservative values validated on DSM 7.3.1 with a Synology Linux 3.10 kernel.
sed -i -E 's/MaxWrite:[[:space:]]+1024 \* 1024,/MaxWrite:             128 * 1024,/' "${BACKEND}"
sed -i -E 's/MaxBackground:[[:space:]]+b\.config\.FuseMaxBackground,/MaxBackground:        12,/' "${BACKEND}"
sed -i -E 's/MaxReadAhead:[[:space:]]+b\.config\.FuseMaxReadAhead,/MaxReadAhead:         128 * 1024,/' "${BACKEND}"

grep -q 'MaxWrite:[[:space:]]*128 \* 1024' "${BACKEND}" || {
    echo "Synology FUSE patch: MaxWrite anchor not found" >&2
    exit 1
}
grep -q 'MaxBackground:[[:space:]]*12' "${BACKEND}" || {
    echo "Synology FUSE patch: MaxBackground anchor not found" >&2
    exit 1
}
grep -q 'MaxReadAhead:[[:space:]]*128 \* 1024' "${BACKEND}" || {
    echo "Synology FUSE patch: MaxReadAhead anchor not found" >&2
    exit 1
}
