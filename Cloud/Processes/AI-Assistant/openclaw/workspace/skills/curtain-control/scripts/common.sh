# Sourced only by the two trusted wrappers; no user-controlled shell expansion.
ivus_publish() {
    if [ "$#" -ne 2 ]; then printf '%s\n' 'Expected exactly one integer argument 0..100' >&2; return 64; fi
    direction=$1
    level=$2
    case "$direction" in up|down) ;; *) return 64 ;; esac
    case "$level" in 0|[1-9]|[1-9][0-9]|100) ;; *) printf '%s\n' 'Invalid target percentage' >&2; return 64 ;; esac
    request_id=${IVUS_REQUEST_ID:-}
    case "$request_id" in ????????-????-????-????-????????????) ;; *) printf '%s\n' 'Missing request identity' >&2; return 77 ;; esac
    case "$(printf '%s' "$request_id" | tr -d '0123456789abcdef-')" in '') ;; *) return 77 ;; esac
    . "$script_dir/../../../../generated/mqtt.env"
    turn="$IVUS_MODULE_ROOT/.runtime/turns/$request_id"
    if [ ! -f "$turn/permit" ]; then printf '%s\n' 'Request is not active' >&2; return 77; fi
    expiry=$(cat "$turn/permit")
    case "$expiry" in ''|*[!0-9]*) return 77 ;; esac
    if [ "$(date +%s)" -ge "$expiry" ]; then printf '%s\n' 'Request expired' >&2; return 77; fi
    intent="$direction $level"
    if ! mkdir "$turn/action" 2>/dev/null; then
        if [ -f "$turn/action/result.json" ] && [ "$(cat "$turn/action/intent")" = "$intent" ]; then
            cat "$turn/action/result.json"
            return 0
        fi
        printf '%s\n' 'An action was already attempted for this request' >&2
        return 75
    fi
    printf '%s' "$intent" > "$turn/action/intent"
    operation_id=$(od -An -N6 -tu8 /dev/urandom | tr -d '[:space:]')
    case "$operation_id" in ''|*[!0-9]*) return 70 ;; esac
    if [ "$operation_id" = 0 ]; then operation_id=1; fi
    topic="$IVUS_MOTOR_TOPIC/command/$direction"
    payload=$(printf '{"operation_id":%s,"level":%s}' "$operation_id" "$level")
    set -- -h "$IVUS_MQTT_HOST" -p "$IVUS_MQTT_PORT" -q 1 -t "$topic" -m "$payload"
    if [ -n "$IVUS_MQTT_USERNAME" ]; then set -- "$@" -u "$IVUS_MQTT_USERNAME"; fi
    if [ -n "$IVUS_MQTT_PASSWORD" ]; then set -- "$@" -P "$IVUS_MQTT_PASSWORD"; fi
    # Literal MQTT CLI invocation, not a Go/HTTP route through Control.
    "$IVUS_MOSQUITTO_PUB" "$@"
    printf '{"operation_id":%s,"level":%s,"topic":"%s","publish_status":"published"}\n' "$operation_id" "$level" "$topic" > "$turn/action/result.tmp"
    mv "$turn/action/result.tmp" "$turn/action/result.json"
    cat "$turn/action/result.json"
}
