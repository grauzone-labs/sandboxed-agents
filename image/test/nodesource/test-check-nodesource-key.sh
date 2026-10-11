#!/usr/bin/env bash
set -euo pipefail

test_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
base_dir="$test_dir/../../context/base"
checker="$base_dir/check-nodesource-key.sh"
containerfile="$base_dir/Containerfile"
expected_file="$base_dir/nodesource-signing-key.fingerprint"
fixture="$test_dir/nodesource-repo.gpg.key"
expected_fingerprint=$(< "$expected_file")
last_digit=${expected_fingerprint: -1}
if [[ "$last_digit" == 0 ]]; then
    wrong_fingerprint="${expected_fingerprint:0:39}1"
else
    wrong_fingerprint="${expected_fingerprint:0:39}0"
fi

scratch=$(mktemp -d)
gnupg_home=$(mktemp -d)
trap 'rm -rf -- "$scratch" "$gnupg_home"' EXIT
chmod 700 "$gnupg_home"

printf '%s\n' "$wrong_fingerprint" > "$scratch/wrong.fingerprint"
failures=0

run_check() {
    if "$checker" "$1" "$2" > "$scratch/stdout" 2> "$scratch/stderr"; then
        check_status=0
    else
        check_status=$?
    fi
}

report() {
    if [[ "$2" == true ]]; then
        printf 'PASS: %s\n' "$1"
    else
        printf 'FAIL: %s\n' "$1"
        failures=$((failures + 1))
    fi
}

# expect_refusal NAME KEY_FILE EXPECTED_FILE TEXT...
# The checker must fail, and its stderr must contain every TEXT.
expect_refusal() {
    local name=$1 key_file=$2 expected_path=$3 passed=false text
    shift 3
    run_check "$key_file" "$expected_path"
    if [[ "$check_status" -ne 0 ]]; then
        passed=true
        for text in "$@"; do
            grep -Fq -- "$text" "$scratch/stderr" || passed=false
        done
    fi
    report "$name" "$passed"
}

run_check "$fixture" "$expected_file"
passed=false
if [[ "$check_status" -eq 0 && ! -s "$scratch/stdout" ]]; then
    passed=true
fi
report 'fixture with expected fingerprint is accepted silently' "$passed"

expect_refusal 'different fingerprint is refused with expected and found fingerprints' \
    "$fixture" "$scratch/wrong.fingerprint" \
    "expected $wrong_fingerprint, found $expected_fingerprint"

two_keys_ready=false
if gpg --homedir "$gnupg_home" --batch --passphrase '' \
    --quick-gen-key 'test <test@example.invalid>' ed25519 default never \
    > "$scratch/generate.stdout" 2> "$scratch/generate.stderr" &&
    gpg --homedir "$gnupg_home" --batch --armor --export 'test@example.invalid' \
        > "$scratch/additional.key" 2> "$scratch/export.stderr"; then
    cat "$fixture" "$scratch/additional.key" > "$scratch/two-keys.asc"
    two_keys_ready=true
else
    printf 'Test key generation or export failed:\n' >&2
    cat "$scratch/generate.stderr" >&2
    if [[ -f "$scratch/export.stderr" ]]; then
        cat "$scratch/export.stderr" >&2
    fi
    report 'two primary keys are refused, naming the expected and both found fingerprints' false
fi
if [[ "$two_keys_ready" == true ]]; then
    generated_fingerprint=$(gpg --homedir "$gnupg_home" --batch --with-colons --list-keys 'test@example.invalid' |
        awk -F: '$1 == "fpr" { print $10; exit }')
    expect_refusal 'two primary keys are refused, naming the expected and both found fingerprints' \
        "$scratch/two-keys.asc" "$expected_file" \
        "expected exactly one primary key in $scratch/two-keys.asc, found 2" \
        "expected fingerprint $expected_fingerprint, found $expected_fingerprint, $generated_fingerprint"
    expect_refusal 'a single key with another fingerprint is refused on its own' \
        "$scratch/additional.key" "$expected_file" \
        "expected $expected_fingerprint, found $generated_fingerprint"
else
    report 'a single key with another fingerprint is refused on its own' false
fi

: > "$scratch/empty.key"
expect_refusal 'empty key file is refused with count 0 and the expected fingerprint' \
    "$scratch/empty.key" "$expected_file" \
    "expected exactly one primary key in $scratch/empty.key, found 0" \
    "expected fingerprint $expected_fingerprint, found none"

passed=false
if [[ $(wc -c < "$expected_file") -eq 41 ]] &&
    LC_ALL=C grep -Eq '^[0-9A-F]{40}$' "$expected_file"; then
    passed=true
fi
report 'expected fingerprint file has exactly 40 uppercase hex characters and one LF' "$passed"

# Static: the recipe runs the check before it writes the keyring or the source list.
check_line=$(grep -n 'check-nodesource-key.sh "\$key_file"' "$containerfile" | head -1 | cut -d: -f1 || true)
dearmor_line=$(grep -n 'gpg --batch --yes --dearmor' "$containerfile" | head -1 | cut -d: -f1 || true)
source_line=$(grep -n 'sources.list.d/nodesource.list' "$containerfile" | head -1 | cut -d: -f1 || true)
passed=false
if [[ -n "$check_line" && -n "$dearmor_line" && -n "$source_line" &&
    "$check_line" -lt "$dearmor_line" && "$check_line" -lt "$source_line" ]]; then
    passed=true
fi
report 'Containerfile runs the check before it writes the keyring and the source list (static)' "$passed"

if [[ "$failures" -ne 0 ]]; then
    exit 1
fi
