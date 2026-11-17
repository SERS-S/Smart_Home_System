find_program(GO_EXECUTABLE NAMES go REQUIRED)

# build.sh exports Cloud/.env before configuring. Embed only this allowlist;
# deployment credentials and other services' settings never enter the binary.
set(control_variables
    MOSQUITTO_HOST MOSQUITTO_PORT REMOTE_CONTROL_HOST REMOTE_CONTROL_PORT
    CONTROL_MQTT_CLIENT_ID CONTROL_MQTT_QOS CONTROL_QUEUE_SIZE
    CONTROL_HISTORY_POINTS CONTROL_JOURNAL_LIMIT
    CONTROL_PUBLISH_TIMEOUT_MS CONTROL_HTTP_TIMEOUT_SECONDS
    CONTROL_SHUTDOWN_TIMEOUT_SECONDS CONTROL_MQTT_RECONNECT_SECONDS
    CONTROL_MQTT_KEEPALIVE_SECONDS
    CONTROL_LIGHT_DEVICE_ID CONTROL_TEMPERATURE_DEVICE_ID
    CONTROL_MOTOR_DEVICE_ID CONTROL_HAB_DEVICE_ID
    CONTROL_HAB_RETRY_SECONDS CONTROL_HAB_MAX_ATTEMPTS
    CONTROL_MOTOR_RETRY_SECONDS CONTROL_MOTOR_MAX_ATTEMPTS
    CONTROL_HAB_CODEC CONTROL_HAB_SAMPLE_FORMAT CONTROL_HAB_CHANNELS
    CONTROL_HAB_SAMPLE_RATE CONTROL_HAB_FRAGMENT_DURATION_MS CONTROL_HAB_FRAGMENT_SIZE
    CONTROL_TOPIC_LIGHT CONTROL_TOPIC_TEMPERATURE CONTROL_TOPIC_MOTOR
    CONTROL_TOPIC_HAB CONTROL_TOPIC_VOICE CONTROL_TOPIC_JOURNAL
)
set(control_config "{")
set(control_separator "")
foreach(variable IN LISTS control_variables)
    set(value "$ENV{${variable}}")
    if("${value}" STREQUAL "" OR "${value}" MATCHES "[^A-Za-z0-9_./:-]")
        message(FATAL_ERROR "${variable} must be set in Cloud/.env and contain only letters, digits or _./:-")
    endif()
    string(APPEND control_config "${control_separator}\"${variable}\":\"${value}\"")
    set(control_separator ",")
endforeach()
string(APPEND control_config "}")
file(WRITE "${CMAKE_CURRENT_BINARY_DIR}/compiled-config.json" "${control_config}\n")

add_custom_target(shs_cloud_control ALL
    COMMAND "${CMAKE_COMMAND}" -E env CGO_ENABLED=0 GOOS=linux GOARCH=amd64
        "GOCACHE=${CMAKE_BINARY_DIR}/go-cache"
        "GOMODCACHE=${CMAKE_BINARY_DIR}/go-modcache"
        GOTELEMETRY=off
        "${GO_EXECUTABLE}" build
        -ldflags "-X 'main.compiledConfig=${control_config}'"
        -o "${CMAKE_BINARY_DIR}/shs_cloud_control" .
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    BYPRODUCTS "${CMAKE_BINARY_DIR}/shs_cloud_control"
    VERBATIM
)
install(PROGRAMS "${CMAKE_BINARY_DIR}/shs_cloud_control" DESTINATION bin)
