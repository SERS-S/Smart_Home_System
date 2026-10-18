cmake_minimum_required(VERSION 3.16)

project(SmartHomeSystemCloud LANGUAGES CXX)

set(PROCESSES
    AI-Assistant
    Api
    Control
    Mosquitto
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
