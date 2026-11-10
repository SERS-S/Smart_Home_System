cmake_minimum_required(VERSION 3.16)

project(SmartHomeSystemCloud LANGUAGES C CXX)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

set(PROCESSES
    AI-Assistant
    Api
    Control
    Telemetry
    Voice
)

foreach(process IN LISTS PROCESSES)
    string(TOLOWER "${process}" process_name)
    set(target "shs_cloud_${process_name}")

    add_executable(${target}
        "../Processes/${process}/main.cpp"
    )

    target_compile_features(${target} PRIVATE cxx_std_17)
    install(TARGETS ${target} RUNTIME DESTINATION bin)
endforeach()

find_package(unofficial-sqlite3 CONFIG REQUIRED)
find_package(nlohmann_json CONFIG REQUIRED)

target_link_libraries(shs_cloud_telemetry PRIVATE
    unofficial::sqlite3::sqlite3
    nlohmann_json::nlohmann_json
)

# build.sh exports Cloud/.env before configuring CMake, as for Mosquitto.
foreach(variable IN ITEMS
    SQLITE_TELEMETRY_DB_PATH SQLITE_API_DB_PATH
    SQLITE_JOURNAL_DB_PATH SQLITE_VOICE_DB_PATH
)
    set(${variable} "$ENV{${variable}}")
    if("${${variable}}" STREQUAL "" OR NOT IS_ABSOLUTE "${${variable}}")
        message(FATAL_ERROR "${variable} must be an absolute database file path")
    endif()
    if("${${variable}}" MATCHES "[\r\n]")
        message(FATAL_ERROR "${variable} must not contain line breaks")
    endif()
    # Escape paths for C++ string literals in the generated header.
    string(REPLACE "\\" "\\\\" ${variable} "${${variable}}")
    string(REPLACE "\"" "\\\"" ${variable} "${${variable}}")
endforeach()

configure_file(
    "../Processes/Telemetry/sqlite_paths.h.in"
    "generated/telemetry/sqlite_paths.h"
    @ONLY
)
target_include_directories(shs_cloud_telemetry PRIVATE
    "${CMAKE_CURRENT_BINARY_DIR}/generated/telemetry"
)

add_subdirectory("../Processes/Mosquitto" "${CMAKE_CURRENT_BINARY_DIR}/mosquitto")

target_link_libraries(shs_cloud_telemetry PRIVATE
    shs_mosquitto_endpoint libmosquitto_static Threads::Threads
)
target_include_directories(shs_cloud_telemetry PRIVATE
    "${CMAKE_CURRENT_SOURCE_DIR}/../Processes/Mosquitto/upstream/include"
    "${CMAKE_CURRENT_SOURCE_DIR}/../Processes/Mosquitto"
)

target_link_libraries(shs_cloud_api PRIVATE
    shs_mosquitto_endpoint libmosquitto_static cJSON
)
target_include_directories(shs_cloud_api PRIVATE
    "${CMAKE_CURRENT_SOURCE_DIR}/../Processes/Mosquitto/upstream/include"
)

set(voice_host "$ENV{REMOTE_VOICE_HOST}")
set(voice_port "$ENV{REMOTE_VOICE_PORT}")

if(NOT voice_host MATCHES "^[A-Za-z0-9.:-]+$")
    message(FATAL_ERROR "REMOTE_VOICE_HOST must be a hostname or IP address")
endif()
if(NOT voice_port MATCHES "^[1-9][0-9]*$" OR voice_port GREATER 65535)
    message(FATAL_ERROR "REMOTE_VOICE_PORT must be between 1 and 65535")
endif()

target_compile_definitions(shs_cloud_voice PRIVATE
    "REMOTE_VOICE_HOST=\"${voice_host}\""
    "REMOTE_VOICE_PORT=${voice_port}"
)

find_package(Boost CONFIG REQUIRED COMPONENTS beast asio system)
find_package(Threads REQUIRED)

find_path(VOSK_INCLUDE_DIR NAMES vosk_api.h)
find_library(VOSK_LIBRARY NAMES vosk)

if(NOT VOSK_INCLUDE_DIR OR NOT VOSK_LIBRARY)
    message(FATAL_ERROR "Vosk header or library was not found")
endif()

add_library(Vosk::vosk SHARED IMPORTED)

set_target_properties(Vosk::vosk PROPERTIES
    IMPORTED_LOCATION "${VOSK_LIBRARY}"
    # The official binary has no SONAME. Link with -lvosk so DT_NEEDED
    # contains libvosk.so instead of a path into Cloud/vcpkg/installed.
    IMPORTED_NO_SONAME TRUE
    INTERFACE_INCLUDE_DIRECTORIES "${VOSK_INCLUDE_DIR}"
)

target_link_libraries(shs_cloud_voice PRIVATE
    shs_mosquitto_endpoint
    libmosquitto_static
    cJSON
    Boost::beast
    Boost::asio
    Boost::system
    Threads::Threads
    Vosk::vosk
)
target_include_directories(shs_cloud_voice PRIVATE
    "${CMAKE_CURRENT_SOURCE_DIR}/../Processes/Mosquitto/upstream/include"
)

# Пути поиска библиотек:
# при локальном запуске — build/lib;
# на сервере — /usr/local/lib/smart-home.
set_target_properties(shs_cloud_voice PROPERTIES
    BUILD_WITH_INSTALL_RPATH TRUE
    INSTALL_RPATH "$ORIGIN/lib;$ORIGIN/../lib/smart-home"
)

# Run on every build so deleting build/lib does not leave an otherwise
# up-to-date executable without its runtime dependency.
add_custom_target(shs_voice_runtime ALL
    COMMAND "${CMAKE_COMMAND}" -E make_directory
        "${CMAKE_BINARY_DIR}/lib"
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different
        "$<TARGET_FILE:Vosk::vosk>"
        "${CMAKE_BINARY_DIR}/lib/"
    COMMAND "${CMAKE_COMMAND}"
        "-DSHS_VOICE_MODEL_ROOT=${CMAKE_BINARY_DIR}/models"
        -P "${CMAKE_CURRENT_SOURCE_DIR}/prepare_voice_model.cmake"
    DEPENDS "${VOSK_LIBRARY}"
    VERBATIM
)
add_dependencies(shs_cloud_voice shs_voice_runtime)

install(FILES "${VOSK_LIBRARY}" DESTINATION lib/smart-home)
install(DIRECTORY "${CMAKE_BINARY_DIR}/models/vosk-model-small-ru-0.22"
    DESTINATION share/smart-home/models
)
