#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# third-party-licenses.sh writes the license, notice and patent grant
# files of the Go standard library and of every module compiled into
# occamyd, on any release platform, to THIRD_PARTY_LICENSES (or the file
# given as the first argument), and those of any compiled package that
# has its own, such as go-jose's fork of encoding/json. Release archives
# and the image carry it. jpegn ports parts of jpegli, whose notice it
# does not carry, so jpegli's, kept in third_party/jpegli, follows
# jpegn's. It fails, writing nothing, when go list fails or names no
# module, or a module has no license file.
set -eu

jpegli=$(dirname "$0")/../third_party/jpegli/LICENSE

out=${1:-THIRD_PARTY_LICENSES}
rule================================================================================
names='^(licen[cs]e|copying|notice|patents|third-party-notices)([.-].*)?$'
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() {
	echo "third-party-licenses.sh: $*" >&2
	exit 1
}

section() {
	printf '%s\n%s\n%s\n\n' "$rule" "$1" "$rule"
}

# deps writes the packages compiled into occamyd on each release
# platform, formatted by the template $1, to the file $2, sorted and
# without duplicates. Each go list writes to a file of its own, since in
# a pipeline its failure would go unseen: the build stage's sh, dash, has
# no pipefail.
deps() {
	for goos in linux darwin windows; do
		GOOS=$goos go list -deps -f "$1" . > "$tmp/$goos" || fail "go list for $goos failed"
	done
	sort -u "$tmp/linux" "$tmp/darwin" "$tmp/windows" > "$2"
}

deps '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' "$tmp/modules"
[ -s "$tmp/modules" ] || fail "go list names no module"
deps '{{if and .Module (not .Module.Main) (ne .Dir .Module.Dir)}}{{.Module.Path}} {{.Module.Version}} {{.Module.Dir}} {{.Dir}}{{end}}' "$tmp/packages"

goroot=$(go env GOROOT)
stdlib=$(ls "$goroot" | grep -iE "$names" || true)
[ -n "$stdlib" ] || fail "no license file in $goroot"
{
	echo "occamyd includes the following third-party software."
	echo
	for f in $stdlib; do
		section "Go standard library $(go env GOVERSION): $f"
		cat "$goroot/$f"
		echo
	done
	while read -r path version dir; do
		files=$(ls "$dir" | grep -iE "$names" || true)
		[ -n "$files" ] || fail "no license file in $path $version"
		for f in $files; do
			section "$path $version: $f"
			cat "$dir/$f"
			echo
		done
		if [ "$path" = github.com/gen2brain/jpegn ]; then
			section "github.com/google/jpegli, which $path ports in part: LICENSE"
			cat "$jpegli"
			echo
		fi
	done < "$tmp/modules"
	while read -r path version moddir dir; do
		for f in $(ls "$dir" | grep -iE "$names" || true); do
			section "$path $version: ${dir#"$moddir"/}/$f"
			cat "$dir/$f"
			echo
		done
	done < "$tmp/packages"
} > "$tmp/out"
cp "$tmp/out" "$out"
