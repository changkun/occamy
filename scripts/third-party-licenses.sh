#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# third-party-licenses.sh writes the license and notice files of the Go
# standard library and of every module compiled into occamyd, on any
# release platform, to THIRD_PARTY_LICENSES (or the file given as the
# first argument), and those of any compiled package that has its own,
# such as go-jose's fork of encoding/json. Release archives and the
# image carry it.
set -eu

out=${1:-THIRD_PARTY_LICENSES}
rule================================================================================
names='^(licen[cs]e|copying|notice|third-party-notices)([.-].*)?$'

section() {
	printf '%s\n%s\n%s\n\n' "$rule" "$1" "$rule"
}

deps() {
	for goos in linux darwin windows; do
		GOOS=$goos go list -deps -f "$1" .
	done | sort -u
}

{
	echo "occamyd includes the following third-party software."
	echo
	section "Go standard library $(go env GOVERSION): LICENSE"
	cat "$(go env GOROOT)/LICENSE"
	echo
	deps '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' |
	while read -r path version dir; do
		files=$(ls "$dir" | grep -iE "$names" || true)
		if [ -z "$files" ]; then
			echo "third-party-licenses.sh: no license file in $path $version" >&2
			exit 1
		fi
		for f in $files; do
			section "$path $version: $f"
			cat "$dir/$f"
			echo
		done
	done
	deps '{{if and .Module (not .Module.Main) (ne .Dir .Module.Dir)}}{{.Module.Path}} {{.Module.Version}} {{.Module.Dir}} {{.Dir}}{{end}}' |
	while read -r path version moddir dir; do
		for f in $(ls "$dir" | grep -iE "$names" || true); do
			section "$path $version: ${dir#"$moddir"/}/$f"
			cat "$dir/$f"
			echo
		done
	done
} > "$out"
