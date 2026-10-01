#!/bin/sh
set -eu

GO_SRC_DIR="$1"
MODULE="github.com/mnightingale/rapidyenc"
VERSION="v0.0.0-20260606125752-cdd7bcd89529"

cd "$GO_SRC_DIR"
go mod download "${MODULE}@${VERSION}"

MOD_DIR="$(go list -m -f '{{.Dir}}' "${MODULE}")"
CGO_FILE="${MOD_DIR}/cgo.go"

chmod u+w "${CGO_FILE}"

if ! grep -q '#cgo linux,arm LDFLAGS:' "${CGO_FILE}"; then
    sed -i '/#cgo linux,arm64 LDFLAGS:/a #cgo linux,arm LDFLAGS: -lrapidyenc -lstdc++' "${CGO_FILE}"
fi
