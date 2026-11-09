cmake_minimum_required(VERSION 3.22)
set(EXTRA_COMPONENT_DIRS
    "../Common/components"
)
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(shs_device_light_sensor)