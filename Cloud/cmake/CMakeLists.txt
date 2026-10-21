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

add_subdirectory("../Processes/Mosquitto" "${CMAKE_CURRENT_BINARY_DIR}/mosquitto")

target_link_libraries(shs_cloud_api PRIVATE
    shs_mosquitto_endpoint libmosquitto_static cJSON
)
target_include_directories(shs_cloud_api PRIVATE
    "${CMAKE_CURRENT_SOURCE_DIR}/../Processes/Mosquitto/upstream/include"
)
