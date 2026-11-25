step() {
    [ -n "$ERRAND_STEPS" ] || return 0
    errand_line="{\"t\": $( date +%s )"
    if [ $# -gt 0 ] && { [ "${1%%=*}" = "$1" ] || [ -n "$( printf '%s' "${1%%=*}" | tr -d 'A-Za-z0-9_' )" ]; }; then
        errand_line="$errand_line, \"text\": \"$( printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' )\""
        shift
    fi
    for errand_kv in "$@"; do
        errand_line="$errand_line, \"${errand_kv%%=*}\": ${errand_kv#*=}"
    done
    printf '%s}\n' "$errand_line" >> "$ERRAND_STEPS" 2>/dev/null
    return 0
}
view() {
    [ -n "$ERRAND_STEPS" ] || return 0
    printf '{"t": %s, "view": "%s"}\n' "$( date +%s )" "$1" >> "$ERRAND_STEPS" 2>/dev/null
    return 0
}
result() {
    [ -n "$ERRAND_RESULTS" ] || return 0
    errand_line=""
    for errand_kv in "$@"; do
        errand_line="$errand_line${errand_line:+, }\"${errand_kv%%=*}\": ${errand_kv#*=}"
    done
    printf '{%s}\n' "$errand_line" >> "$ERRAND_RESULTS" 2>/dev/null
    return 0
}
download() {
    "$ERRAND" download "$@"
}
require() {
    [ -n "$ERRAND_ASKS" ] || return 0
    errand_set=$( "$ERRAND" require "$@" )
    errand_rc=$?
    [ "$errand_rc" -eq 75 ] && exit 75
    [ "$errand_rc" -eq 0 ] || return "$errand_rc"
    for errand_kv in $errand_set; do export "$errand_kv"; done
    return 0
}
