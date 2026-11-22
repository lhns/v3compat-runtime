#!/bin/sh
# End to end: runs the wrapper as the OCI runtime in front of runc, with the real v3compat bundle.
#   sudo test/e2e.sh WRAPPER BUNDLE_DIR PROBE
# Writes /etc/v3compat-runtime/config.toml. The runner has AVX2; the client emulates anyway, so
# translated runs exercise DynamoRIO. Ivy Bridge coverage is v3compat's lab.
set -eu

wrapper=$(readlink -f "$1"); bundle=$(readlink -f "$2"); probe=$(readlink -f "$3")
runc=$(command -v runc)
w=$(mktemp -d); chmod 755 "$w"
trap 'rm -rf "$w"' EXIT
mkdir -p /etc/v3compat-runtime
fail=0

conf() { # EXTRA_TOML_LINES
    printf 'real_runtime = "%s"\nbundle_path = "%s"\nlog_file = "%s/log"\n%s\n' "$runc" "$bundle" "$w" "$1" > /etc/v3compat-runtime/config.toml
}

# mkbundle NAME UID ARGS... ; sets $b. ARGS become process.args; annotations come from $ann (a jq object).
mkbundle() {
    name=$1 uid=$2; shift 2
    b=$w/$name; rm -rf "$b"; mkdir -p "$b/rootfs"
    cp "$probe" "$b/rootfs/probe"
    cp "$probe" "$b/rootfs/probe-suid"; chown 0:0 "$b/rootfs/probe-suid"; chmod 4755 "$b/rootfs/probe-suid"
    "$runc" spec --bundle "$b"
    args=$(printf '%s\n' "$@" | jq -R . | jq -s .)
    jq --argjson args "$args" --argjson uid "$uid" --argjson ann "$ann" \
        '.process.args=$args | .process.terminal=false | .process.user={uid:$uid,gid:$uid}
         | .process.noNewPrivileges=false | .root.readonly=false | .annotations=$ann' \
        "$b/config.json" > "$b/config.new" && mv "$b/config.new" "$b/config.json"
}

# check NAME WANT_STATUS WANT_OUT_REGEX ("-": any output) ; reads $status and the out/err files
check() {
    if [ "$status" = "$2" ] && { [ "$3" = - ] || grep -Eq "$3" "$w/out"; }; then echo "ok   $1"; else
        echo "FAIL $1: status $status, want $2; want output /$3/"; sed 's/^/  out: /' "$w/out"; sed 's/^/  err: /' "$w/err"
        sed 's/^/  log: /' "$w/log" 2>/dev/null || true; fail=1
    fi
}

# run NAME: run the container through the wrapper
run() {
    status=0; rm -f "$w/log"
    "$wrapper" --root "$w/state" run --bundle "$b" "$1" >"$w/out" 2>"$w/err" || status=$?
}

ann='{}'
conf 'native = false'
mkbundle translated 0 /probe translated
run c1; check "translated: DynamoRIO mapped, results right" 0 'libdynamorio mapped=1'
grep -q 'wrapped with v3compat-run' "$w/log" || { echo "FAIL no wrap log line"; fail=1; }

conf 'native = true'
mkbundle native 0 /probe native
run c2; check "native=true on a v3 CPU: no DynamoRIO" 0 'libdynamorio mapped=0'
grep -q 'running natively' "$w/log" || { echo "FAIL no native log line"; fail=1; }

# exec into a running wrapped container: the exec'd probe is translated.
conf 'native = false'
mkbundle execc 0 /probe wait
"$wrapper" --root "$w/state" create --bundle "$b" c3 >/dev/null 2>&1
"$wrapper" --root "$w/state" start c3
cat > "$w/process.json" <<'JSON'
{"terminal":false,"user":{"uid":0,"gid":0},"cwd":"/","env":["PATH=/"],"args":["/probe","translated"]}
JSON
status=0
"$wrapper" --root "$w/state" exec --process "$w/process.json" c3 >"$w/out" 2>"$w/err" || status=$?
check "exec into a wrapped container is translated" 0 'libdynamorio mapped=1'
"$runc" --root "$w/state" kill c3 KILL || true
sleep 1; "$runc" --root "$w/state" delete -f c3 || true

# Fail closed: without a usable bundle a translating node refuses the container; a native node runs it.
real_bundle=$bundle; bundle=$w/nobundle; mkdir -p "$bundle"
conf 'native = false'
mkbundle refused 0 /probe translated
status=0
"$wrapper" --root "$w/state" --log "$w/rlog" --log-format json run --bundle "$b" c7 >"$w/out" 2>"$w/err" || status=$?
check "broken bundle on a translating node: refused" 1 -
grep -q 'bundle missing' "$w/err" || { echo "FAIL no reason on stderr"; fail=1; }
jq -e 'select(.level == "error") | .msg | test("bundle missing")' "$w/rlog" >/dev/null || { echo "FAIL no reason in --log"; fail=1; }
"$runc" --root "$w/state" state c7 >/dev/null 2>&1 && { echo "FAIL c7 exists"; fail=1; }
conf 'native = true'
mkbundle nativebroken 0 /probe native
run c8; check "broken bundle on a native node: runs natively" 0 'libdynamorio mapped=0'
bundle=$real_bundle; conf 'native = false'

# The launcher refuses a setuid-root file run by another user: status 87.
mkbundle suid 65534 /probe-suid translated
run c4; check "setuid-root program as uid 65534: status 87" 87 -

# cache mount: a pinned image with a cache directory gets it read-only; others get nothing.
hex=$(printf 'a%.0s' $(seq 64))
mkdir -p "$w/cache/sha256-$hex"; echo x > "$w/cache/sha256-$hex/entry"
conf "native = false
cache_root = \"$w/cache\""
ann=$(jq -n --arg i "r.example/app:1@sha256:$hex" '{"io.kubernetes.cri.image-name":$i}')
mkbundle cachectr 0 /probe cache-ro /v3compat-cache
run c5; check "cache: matching digest mounted read-only" 0 'dir=1 write errno=30'
grep -q "image \"r.example/app:1@sha256:$hex\"" "$w/log" || { echo "FAIL image not logged"; fail=1; }
for img in "r.example/app:1" "r.example/app@sha256:$(printf 'b%.0s' $(seq 64))" "r.example/app@sha256:../../etc"; do
    ann=$(jq -n --arg i "$img" '{"io.kubernetes.cri.image-name":$i}')
    mkbundle cachenone 0 /probe cache-none /v3compat-cache
    run c6; check "cache: $img mounts nothing" 0 'exists=0'
    "$runc" --root "$w/state" delete -f c6 >/dev/null 2>&1 || true
done

exit $fail
