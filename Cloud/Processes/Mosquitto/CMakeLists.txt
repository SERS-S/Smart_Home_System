# Build the broker from the vendored Eclipse Mosquitto sources as part of
# shs_cloud_mosquitto. No system mosquitto executable or library is required.
set(mosquitto_SOURCE_DIR "${CMAKE_CURRENT_SOURCE_DIR}/upstream")
set(VERSION "2.1.2")
set(SHS_EMBEDDED_BROKER ON)
set(WITH_BUNDLED_DEPS ON)
set(WITH_TLS OFF)
set(WITH_WEBSOCKETS OFF)
set(WITH_HTTP_API OFF)
set(WITH_THREADING ON)
set(INC_MEMTRACK ON)

include(CheckIncludeFiles)
include(CheckSourceCompiles)
include(CMakePushCheckState)

add_compile_definitions(CMAKE "VERSION=\"${VERSION}\"" WITH_THREADING WITH_UNIX_SOCKETS)

add_library(cJSON OBJECT cjson/cJSON.c)
target_include_directories(cJSON PUBLIC "${CMAKE_CURRENT_SOURCE_DIR}")

add_library(config-header INTERFACE)
target_include_directories(config-header INTERFACE "${mosquitto_SOURCE_DIR}")

add_library(common-options INTERFACE)
target_include_directories(common-options INTERFACE
    "${mosquitto_SOURCE_DIR}"
    "${mosquitto_SOURCE_DIR}/include"
    "${mosquitto_SOURCE_DIR}/deps"
)

add_subdirectory(upstream/libcommon libcommon)
add_subdirectory(upstream/src broker)
# Vendored spdlog v1.15.3, built from headers for the Linux cross target.
add_subdirectory(spdlog EXCLUDE_FROM_ALL)

add_executable(shs_cloud_mosquitto main.cpp)
target_compile_features(shs_cloud_mosquitto PRIVATE cxx_std_17)

set(mosquitto_host "$ENV{MOSQUITTO_HOST}")
set(mosquitto_port "$ENV{MOSQUITTO_PORT}")
set(mosquitto_anonymous_allow "$ENV{MOSQUITTO_ANONYMOUS_ALLOW}")

if(NOT mosquitto_host MATCHES "^[A-Za-z0-9.:-]+$")
    message(FATAL_ERROR "MOSQUITTO_HOST must be a hostname or IP address")
endif()
if(NOT mosquitto_port MATCHES "^[0-9]+$" OR
   mosquitto_port LESS 1 OR mosquitto_port GREATER 65535)
    message(FATAL_ERROR "MOSQUITTO_PORT must be between 1 and 65535")
endif()
if(NOT mosquitto_anonymous_allow STREQUAL "true" AND
   NOT mosquitto_anonymous_allow STREQUAL "false")
    message(FATAL_ERROR "MOSQUITTO_ANONYMOUS_ALLOW must be true or false")
endif()

target_compile_definitions(shs_cloud_mosquitto PRIVATE
    "MOSQUITTO_HOST=\"${mosquitto_host}\""
    "MOSQUITTO_PORT=${mosquitto_port}"
    "MOSQUITTO_ANONYMOUS_ALLOW=${mosquitto_anonymous_allow}"
)

target_link_libraries(shs_cloud_mosquitto PRIVATE
    mosquitto libmosquitto_common cJSON spdlog::spdlog_header_only
)
set_target_properties(shs_cloud_mosquitto PROPERTIES
    RUNTIME_OUTPUT_DIRECTORY "${CMAKE_BINARY_DIR}"
)
install(TARGETS shs_cloud_mosquitto RUNTIME DESTINATION bin)
