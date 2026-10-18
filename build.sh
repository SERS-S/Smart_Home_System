#!/bin/bash

set -euo pipefail

# # # UPLOAD VARIABLES
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -f "$SCRIPT_DIR/Cloud/.env" ]]; then
    set -a
    source "$SCRIPT_DIR/Cloud/.env"
    set +a
fi
# # #

# # # FUNCTIONS
run_step() {
    local label="$1"
    shift
    if command -v python3 >/dev/null 2>&1; then
        python3 "$SCRIPT_DIR/scripts/build_output.py" "$label" "$@"
    else
        printf ' > %s\n' "$label"
        "$@"
    fi
}

cloud_build() {
    local cloud_dir="$SCRIPT_DIR/Cloud"
    local build_dir="$cloud_dir/build-linux-x86_64"
    local color_diagnostics=OFF

    mkdir -p "$build_dir/zig-cache"
    export ZIG_GLOBAL_CACHE_DIR="$build_dir/zig-cache"

    if [[ -t 1 && "${TERM:-dumb}" != dumb && -z "${NO_COLOR+x}" ]]; then
        color_diagnostics=ON
    fi

    run_step 'Настройка CMake' cmake -S "$cloud_dir/cmake" -B "$build_dir" \
        -G "Unix Makefiles" \
        -DCMAKE_TOOLCHAIN_FILE="$cloud_dir/cmake/linux-x86_64.cmake" \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_COLOR_DIAGNOSTICS="$color_diagnostics" \
        --fresh &&
    run_step 'Сборка Cloud' cmake --build "$build_dir" --parallel
}

cloud_deploy() {
    (
        local cloud_dir="$SCRIPT_DIR/Cloud"
        local build_dir="$cloud_dir/build-linux-x86_64"

        if [[ ! -f "$build_dir/cmake_install.cmake" ]]; then
            printf " !> Cloud build not found. Please build it first\n" >&2
            exit 1
        fi

        local variable
        for variable in PASSWORD REMOTE_PORT REMOTE_USER REMOTE_HOST; do
            if [[ -z "${!variable:-}" ]]; then
                printf ' !> Missing deployment variable: %s\n' "$variable" >&2
                exit 1
            fi
        done

        local binary
        local binaries=("$build_dir"/shs_cloud_*)
        for binary in "${binaries[@]}"; do
            if [[ ! -f "$binary" || ! -x "$binary" ]]; then
                printf ' !> Missing executable: %s\n' "$binary" >&2
                exit 1
            fi
            if [[ "$(LC_ALL=C file -b "$binary")" != ELF* ]]; then
                printf ' !> Linux deployment requires ELF binaries: %s. Rebuild Cloud with the Linux toolchain.\n' "$binary" >&2
                exit 1
            fi
        done

        local service_file
        for service_file in "$cloud_dir"/services/*.service; do
            if [[ ! -s "$service_file" ]]; then
                printf ' !> Service file is missing or empty: %s\n' "$service_file" >&2
                exit 1
            fi
        done

        printf " > > Stopping Cloud services\n"
        sshpass -p "$PASSWORD" ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" 'bash -s' <<'EOF'
            set -euo pipefail
        
            systemctl stop shs_ai-assistant.service > /dev/null 2>&1 || true; printf "  > > > Stopped shs_ai-assistant.service\n"
            systemctl stop shs_api.service > /dev/null 2>&1 || true; printf "  > > > Stopped shs_api.service\n"
            systemctl stop shs_voice.service > /dev/null 2>&1 || true; printf "  > > > Stopped shs_voice.service\n"
            systemctl stop shs_telemetry.service > /dev/null 2>&1 || true; printf "  > > > Stopped shs_telemetry.service\n"
            systemctl stop shs_control.service > /dev/null 2>&1 || true; printf "  > > > Stopped shs_control.service\n"
            systemctl stop shs_mosquitto.service > /dev/null 2>&1 || true; printf "  > > > Stopped shs_mosquitto.service\n"
EOF

        printf "\n"
        
        printf " > > Uploading Cloud binaries to remote server\n"
        run_step 'Загрузка бинарников' \
            scp -S "$SCRIPT_DIR/scripts/sshpass_ssh.sh" -P "$REMOTE_PORT" \
            "$build_dir"/shs_cloud_* "$REMOTE_USER@$REMOTE_HOST:/usr/local/bin/"

         printf "\n"

        printf " > > Uploading Cloud service files to remote server\n"
        run_step 'Загрузка service-файлов' \
            scp -S "$SCRIPT_DIR/scripts/sshpass_ssh.sh" -P "$REMOTE_PORT" \
            "$cloud_dir"/services/*.service "$REMOTE_USER@$REMOTE_HOST:/etc/systemd/system/"

         printf "\n"

        printf " > > Enabling Cloud services\n"
        sshpass -p "$PASSWORD" ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" 'bash -s' <<'EOF'
            set -euo pipefail
        
            systemctl daemon-reload

            systemctl enable shs_mosquitto.service; printf "  > > > Enabled shs_mosquitto.service\n"
            systemctl enable shs_control.service; printf "  > > > Enabled shs_control.service\n"
            systemctl enable shs_telemetry.service; printf "  > > > Enabled shs_telemetry.service\n"
            systemctl enable shs_voice.service; printf "  > > > Enabled shs_voice.service\n"
            systemctl enable shs_api.service; printf "  > > > Enabled shs_api.service\n"
            systemctl enable shs_ai-assistant.service; printf "  > > > Enabled shs_ai-assistant.service\n"
EOF

        printf "\n"

        printf " > > Starting Cloud services\n"
        sshpass -p "$PASSWORD" ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" 'bash -s' <<'EOF'
            set -euo pipefail
        
            systemctl start shs_mosquitto.service; printf "  > > > Started shs_mosquitto.service\n"
            systemctl start shs_control.service; printf "  > > > Started shs_control.service\n"
            systemctl start shs_telemetry.service; printf "  > > > Started shs_telemetry.service\n"
            systemctl start shs_voice.service; printf "  > > > Started shs_voice.service\n"
            systemctl start shs_api.service; printf "  > > > Started shs_api.service\n"
            systemctl start shs_ai-assistant.service; printf "  > > > Started shs_ai-assistant.service\n"
EOF

        printf "\n"
    )
}
# # #

if [ "$#" -ge 1 ]; then

    if [[ "${1:-}" == "cloud" ]]; then

        printf " >>> Cloud action\n\n"
        if [[ "${2:-build}" == "build" ]]; then
            printf " > Start building Cloud\n"

            cloud_build

            printf " > Finished building Cloud\n"
        elif [[ "${2:-}" == "deploy" ]]; then
            printf " > Start building Cloud\n"

            cloud_build

            printf " > Finished building Cloud\n\n"

            printf " > Start deploying Cloud\n"

            cloud_deploy

            printf " > Finished deploying Cloud\n"
        else
            printf ' !> Unknown cloud action: %s\n' "$2" >&2
            exit 2
        fi

    elif [[ "${1:-}" == "device" ]]; then

        printf " >>> Device action\n\n"
        if [[ -z "${2:-}" ]]; then
            printf ' !> Missing device type. Usage: %s device {light|temperature|hab|motor}\n' "${0##*/}" >&2
            exit 2
        fi

        if [[ "${2:-}" == "light" ]]; then
            printf " > Start building Light Sensor\n"

            sleep 1

            printf " > Finished building Light Sensor\n"
        elif [[ "${2:-}" == "temperature" ]]; then
            printf " > Start building Temperature Sensor\n"

            sleep 1

            printf " > Finished building Temperature Sensor\n"
        elif [[ "${2:-}" == "hab" ]]; then
            printf " > Start building HAB\n"

            sleep 1

            printf " > Finished building HAB\n"
        elif [[ "${2:-}" == "motor" ]]; then
            printf " > Start deploying Motor\n"

            sleep 1

            printf " > Finished deploying Motor\n"
        else
            printf ' !> Unknown device type: %s\n' "$2" >&2
            exit 2
        fi

    else
        printf ' !> Unknown command type: %s\n' "$1" >&2
        exit 2
    fi

else 
    printf ' !> Missing command type. Usage: %s {cloud|device}\n' "${0##*/}" >&2
    exit 2
fi
