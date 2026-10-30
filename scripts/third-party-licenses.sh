#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# third-party-licenses.sh writes the license, notice and patent grant
# files of the Go standard library and of every module compiled into
# occamyd, on any release platform, to THIRD_PARTY_LICENSES (or the file
# given as the first argument). Release archives carry it. jpegn ports
# parts of jpegli, whose notice it does not carry, so jpegli's, kept in
# third_party/jpegli, follows jpegn's.
set -eu

jpegli=$(dirname "$0")/../third_party/jpegli/LICENSE

out=${1:-THIRD_PARTY_LICENSES}
rule================================================================================

section() {
	printf '%s\n%s\n%s\n\n' "$rule" "$1" "$rule"
}

{
	echo "occamyd includes the following third-party software."
	echo
	for f in LICENSE PATENTS; do
		section "Go standard library $(go env GOVERSION): $f"
		cat "$(go env GOROOT)/$f"
		echo
	done
	for goos in linux darwin windows; do
		GOOS=$goos go list -deps \
			-f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' .
	done | sort -u | while read -r path version dir; do
		files=$(ls "$dir" | grep -iE '^(licen[cs]e|copying|notice|patents|third-party-notices)([.-].*)?$' || true)
		if [ -z "$files" ]; then
			echo "third-party-licenses.sh: no license file in $path $version" >&2
			exit 1
		fi
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
	done
} > "$out"
