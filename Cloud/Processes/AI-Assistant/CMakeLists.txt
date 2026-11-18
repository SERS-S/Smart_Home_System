find_program(GO_EXECUTABLE NAMES go REQUIRED)
find_package(Python3 REQUIRED COMPONENTS Interpreter)

set(ai_root "/usr/local/lib/smart-home/ai-assistant")
set(ai_package "${CMAKE_CURRENT_SOURCE_DIR}/.cache/linux-runtime")
set(ai_cache "${CMAKE_CURRENT_SOURCE_DIR}/.cache")

# Reuse the project's Mosquitto libraries; the vendored tree omits CLI sources.
set(client_archive "${ai_cache}/downloads/mosquitto-2.1.2.tar.gz")
file(MAKE_DIRECTORY "${ai_cache}/downloads")
file(DOWNLOAD "https://mosquitto.org/files/source/mosquitto-2.1.2.tar.gz"
    "${client_archive}" TLS_VERIFY ON
    EXPECTED_HASH SHA256=fd905380691ac65ea5a93779e8214941829e3d6e038d5edff9eac5fd74cbed02)
if(NOT EXISTS "${ai_cache}/mosquitto-2.1.2/client/pub_client.c")
    execute_process(COMMAND "${CMAKE_COMMAND}" -E tar xzf "${client_archive}"
        WORKING_DIRECTORY "${ai_cache}" RESULT_VARIABLE extracted)
    if(NOT extracted EQUAL 0)
        message(FATAL_ERROR "Cannot extract mosquitto_pub sources")
    endif()
endif()
set(client_source "${ai_cache}/mosquitto-2.1.2")
add_executable(shs_ivus_mosquitto_pub
    "${client_source}/client/pub_client.c" "${client_source}/client/pub_shared.c"
    "${client_source}/client/client_shared.c" "${client_source}/client/client_props.c")
target_compile_definitions(shs_ivus_mosquitto_pub PRIVATE CMAKE VERSION="2.1.2" WITH_THREADING WITH_UNIX_SOCKETS)
target_include_directories(shs_ivus_mosquitto_pub PRIVATE "${client_source}" "${client_source}/client")
target_link_libraries(shs_ivus_mosquitto_pub PRIVATE libmosquitto_static libmosquitto_common Threads::Threads)
set_target_properties(shs_ivus_mosquitto_pub PROPERTIES
    OUTPUT_NAME mosquitto_pub RUNTIME_OUTPUT_DIRECTORY "${ai_package}/bin")

add_custom_target(shs_cloud_ai-assistant ALL
    COMMAND "${Python3_EXECUTABLE}" "${CMAKE_CURRENT_SOURCE_DIR}/scripts/prepare-linux.py"
    COMMAND "${CMAKE_COMMAND}" -E env --unset=GOOS --unset=GOARCH
        "GOCACHE=${ai_cache}/go-build" "GOMODCACHE=${ai_cache}/go-mod"
        "GOTMPDIR=${ai_cache}/tmp" GOTELEMETRY=off
        "${GO_EXECUTABLE}" run ./cmd/build -os linux -arch amd64 -root "${ai_root}"
        -output "${CMAKE_BINARY_DIR}/shs_cloud_ai-assistant"
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different "${CMAKE_BINARY_DIR}/shs_cloud_ai-assistant" "${ai_package}/shs_cloud_ai-assistant"
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    DEPENDS shs_ivus_mosquitto_pub
    BYPRODUCTS "${CMAKE_BINARY_DIR}/shs_cloud_ai-assistant"
    VERBATIM)
install(DIRECTORY "${ai_package}/" DESTINATION lib/smart-home/ai-assistant USE_SOURCE_PERMISSIONS)
