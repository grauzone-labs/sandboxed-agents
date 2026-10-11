#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
    printf 'usage: %s KEY_FILE FINGERPRINT_FILE\n' "$0" >&2
    exit 1
fi

key_file=$1
fingerprint_file=$2
gnupg_home=$(mktemp -d)
trap 'rm -rf -- "$gnupg_home"' EXIT
chmod 700 "$gnupg_home"

expected=$(< "$fingerprint_file")
expected="${expected#"${expected%%[![:space:]]*}"}"
expected="${expected%"${expected##*[![:space:]]}"}"

if gpg --homedir "$gnupg_home" --batch --with-colons --import-options show-only \
    --import "$key_file" > "$gnupg_home/keys" 2> "$gnupg_home/stderr"; then
    gpg_status=0
else
    gpg_status=$?
fi

primary_count=$(awk -F: '$1 == "pub" { count++ } END { print count + 0 }' "$gnupg_home/keys")
primary_fingerprints=$(awk -F: '
    $1 == "pub" { primary = 1; next }
    $1 == "sub" { primary = 0 }
    $1 == "fpr" && primary { print $10; primary = 0 }
' "$gnupg_home/keys")

if [[ "$primary_count" -ne 1 ]]; then
    found_list=$(awk 'NF { list = list (list == "" ? "" : ", ") $0 } END { print (list == "" ? "none" : list) }' <<< "$primary_fingerprints")
    printf 'NodeSource signing key check failed: expected exactly one primary key in %s, found %s; expected fingerprint %s, found %s\n' \
        "$key_file" "$primary_count" "$expected" "$found_list" >&2
    exit 1
fi

if [[ "${expected^^}" != "${primary_fingerprints^^}" ]]; then
    printf 'NodeSource signing key fingerprint mismatch: expected %s, found %s\n' "$expected" "$primary_fingerprints" >&2
    exit 1
fi

if [[ "$gpg_status" -ne 0 ]]; then
    cat "$gnupg_home/stderr" >&2
    exit 1
fi
