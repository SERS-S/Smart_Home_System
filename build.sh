#!/bin/bash

set -euo pipefail

# # # DOWNLOAD DEPENDENCIES
printf " >>> Checking dependencies\n\n"
case "$(uname -s)" in
    Darwin)
        # MacOS
        if ! xcode-select -p >/dev/null 2>&1; then
            xcode-select --install
            printf 'Finish installing Command Line Tools, then run the script again.\n' >&2
            exit 1
        fi

        brew_path="$(command -v brew || true)"
        if [[ -z "$brew_path" && -x /opt/homebrew/bin/brew ]]; then
            brew_path=/opt/homebrew/bin/brew
        elif [[ -z "$brew_path" && -x /usr/local/bin/brew ]]; then
            brew_path=/usr/local/bin/brew
        fi
        if [[ -z "$brew_path" ]]; then
            brew_installer="$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
            /bin/bash -c "$brew_installer"
            if [[ -x /opt/homebrew/bin/brew ]]; then
                brew_path=/opt/homebrew/bin/brew
            elif [[ -x /usr/local/bin/brew ]]; then
                brew_path=/usr/local/bin/brew
            else
                printf 'Homebrew was not found after installation.\n' >&2
                exit 1
            fi
        fi
        brew_env="$("$brew_path" shellenv)"
        eval "$brew_env"

        for formula in libgcrypt glib pixman sdl2 libslirp dfu-util cmake python zig sshpass; do
            if ! brew list --formula --versions "$formula" >/dev/null 2>&1; then
                brew install "$formula"
            fi
        done

        if ! command -v eim >/dev/null 2>&1; then
            brew tap espressif/eim
            if brew help trust >/dev/null 2>&1; then
                brew trust --formula espressif/eim/eim
            fi
            brew install eim
        fi

        idf_activate="${ESP_IDF_ACTIVATE:-$HOME/.espressif/tools/activate_idf_v6.0.3.sh}"
        if [[ ! -f "$idf_activate" ]]; then
            (cd "$HOME" && eim install -i v6.0.3)
            if [[ ! -f "$idf_activate" ]]; then
                printf 'ESP-IDF activation script was not found: %s\n' "$idf_activate" >&2
                exit 1
            fi
        fi
        ;;
    Linux)
        # Linux
        brew_path="$(command -v brew || true)"
        if [[ -z "$brew_path" && -x /home/linuxbrew/.linuxbrew/bin/brew ]]; then
            brew_path=/home/linuxbrew/.linuxbrew/bin/brew
        fi
        if [[ -z "$brew_path" ]]; then
            if [[ "$(id -u)" -eq 0 ]]; then
                package_command=()
            else
                package_command=(sudo)
            fi
            if command -v apt-get >/dev/null 2>&1; then
                "${package_command[@]}" apt-get update
                "${package_command[@]}" apt-get install -y build-essential procps curl file git
            elif command -v dnf >/dev/null 2>&1; then
                "${package_command[@]}" dnf group install -y development-tools
                "${package_command[@]}" dnf install -y procps-ng curl file git
            elif command -v pacman >/dev/null 2>&1; then
                "${package_command[@]}" pacman -S --needed --noconfirm base-devel procps-ng curl file git
            else
                printf 'Install Homebrew prerequisites first: no supported Linux package manager found.\n' >&2
                exit 1
            fi

            brew_installer="$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
            /bin/bash -c "$brew_installer"
            brew_path=/home/linuxbrew/.linuxbrew/bin/brew
            if [[ ! -x "$brew_path" ]]; then
                printf 'Homebrew was not found after installation.\n' >&2
                exit 1
            fi
        fi
        brew_env="$("$brew_path" shellenv)"
        eval "$brew_env"

        for formula in libgcrypt glib pixman sdl2 libslirp dfu-util cmake python zig sshpass; do
            if ! brew list --formula --versions "$formula" >/dev/null 2>&1; then
                brew install "$formula"
            fi
        done

        if ! command -v eim >/dev/null 2>&1; then
            brew tap espressif/eim
            if brew help trust >/dev/null 2>&1; then
                brew trust --formula espressif/eim/eim
            fi
            brew install eim
        fi

        idf_activate="${ESP_IDF_ACTIVATE:-$HOME/.espressif/tools/activate_idf_v6.0.3.sh}"
        if [[ ! -f "$idf_activate" ]]; then
            (cd "$HOME" && eim install -i v6.0.3)
            if [[ ! -f "$idf_activate" ]]; then
                printf 'ESP-IDF activation script was not found: %s\n' "$idf_activate" >&2
                exit 1
            fi
        fi

        ;;
    *)
        printf "  > > Unsupported OS: $(uname -s)\n" >&2
        exit 1
        ;;
esac
# # #

# # # UPLOAD VARIABLES
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ "${1:-}" == cloud && -f "$SCRIPT_DIR/Cloud/.env" ]]; then
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

device_idf() {
    local label="$1"
    local project_dir="$2"
    shift 2

    local command=()
    if [[ -n "${IDF_PATH:-}" && -n "${IDF_PYTHON_ENV_PATH:-}" && \
          -f "$IDF_PATH/tools/idf.py" && -x "$IDF_PYTHON_ENV_PATH/bin/python" ]]; then
        command=("$IDF_PYTHON_ENV_PATH/bin/python" "$IDF_PATH/tools/idf.py")
    else
        local activate_script="${ESP_IDF_ACTIVATE:-$HOME/.espressif/tools/activate_idf_v6.0.3.sh}"
        if [[ ! -f "$activate_script" ]]; then
            printf ' !> ESP-IDF is not active. Set ESP_IDF_ACTIVATE to the path of its activation script: %s\n' "$activate_script" >&2
            return 1
        fi
        # EIM's activation script must be sourced from a shell named "bash".
        # EIM may return a failure after activation if its optional selection sync fails.
        command=(bash -c '. "$1" >/dev/null; shift; type idf.py >/dev/null 2>&1 || exit 1; idf.py "$@"' bash "$activate_script")
    fi

    if [[ -n "$label" ]]; then
        run_step "$label" "${command[@]}" -C "$project_dir" "$@"
    else
        "${command[@]}" -C "$project_dir" "$@"
    fi
}

device_port() {
    local port="${1:-${ESP_PORT:-}}"
    if [[ -z "$port" ]]; then
        printf ' !> Specify a serial port, for example /dev/cu.usbserial-0001, or set ESP_PORT.\n' >&2
        return 2
    fi
    if [[ ! -e "$port" ]]; then
        printf ' !> Serial port does not exist: %s\n' "$port" >&2
        return 2
    fi
    printf '%s\n' "$port"
}

device_build() {
    local project_dir="$1"
    local device_name="$2"

    printf ' > Start building %s\n' "$device_name"
    device_idf "Сборка $device_name" "$project_dir" build
    printf ' > Finished building %s\n' "$device_name"
}

device_flash() {
    local project_dir="$1"
    local device_name="$2"
    local port="$3"
    local baud="$4"

    printf ' > Flashing %s on %s at %s baud\n' "$device_name" "$port" "$baud"
    device_idf "Прошивка $device_name" "$project_dir" -p "$port" -b "$baud" flash
    printf ' > Finished flashing %s\n' "$device_name"
}

device_monitor() {
    local project_dir="$1"
    local device_name="$2"
    local port="$3"

    printf ' > Opening %s monitor on %s (exit: Ctrl+])\n' "$device_name" "$port"
    device_idf '' "$project_dir" -p "$port" monitor
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
            printf ' !> Usage: %s device {light|temperature|hab|motor} [build|flash|monitor|flash-monitor] [serial-port]\n' "${0##*/}" >&2
            exit 2
        fi

        local_device_dir=''
        local_device_name=''
        case "$2" in
            light)       local_device_dir=LightSensor;       local_device_name='Light Sensor' ;;
            temperature) local_device_dir=TemperatureSensor; local_device_name='Temperature Sensor' ;;
            hab)         local_device_dir=Hab;               local_device_name=HAB ;;
            motor)       local_device_dir=Motor;             local_device_name=Motor ;;
            *) printf ' !> Unknown device type: %s\n' "$2" >&2; exit 2 ;;
        esac

        project_dir="$SCRIPT_DIR/Devices/$local_device_dir"
        if [[ ! -f "$project_dir/CMakeLists.txt" ]]; then
            printf ' !> ESP-IDF project not found: %s\n' "$project_dir" >&2
            exit 1
        fi

        action="${3:-build}"
        case "$action" in
            build)
                device_build "$project_dir" "$local_device_name"
                ;;
            flash|monitor|flash-monitor)
                port="$(device_port "${4:-}")"
                if [[ "$action" == monitor ]]; then
                    device_monitor "$project_dir" "$local_device_name" "$port"
                else
                    baud="${ESP_BAUD:-115200}"
                    if [[ ! "$baud" =~ ^[0-9]+$ || "$baud" == 0 ]]; then
                        printf ' !> ESP_BAUD must be a positive integer.\n' >&2
                        exit 2
                    fi
                    device_build "$project_dir" "$local_device_name"
                    device_flash "$project_dir" "$local_device_name" "$port" "$baud"
                    if [[ "$action" == flash-monitor ]]; then
                        device_monitor "$project_dir" "$local_device_name" "$port"
                    fi
                fi
                ;;
            *) printf ' !> Unknown device action: %s\n' "$action" >&2; exit 2 ;;
        esac

    else
        printf ' !> Unknown command type: %s\n' "$1" >&2
        exit 2
    fi

else 
    printf ' !> Missing command type. Usage: %s {cloud|device}\n' "${0##*/}" >&2
    exit 2
fi
