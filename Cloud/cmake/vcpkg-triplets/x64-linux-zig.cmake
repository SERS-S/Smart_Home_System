set(VCPKG_TARGET_ARCHITECTURE x64)
set(VCPKG_CMAKE_SYSTEM_NAME Linux)

set(VCPKG_CRT_LINKAGE dynamic)
set(VCPKG_LIBRARY_LINKAGE static)
set(VCPKG_BUILD_TYPE release)
# vcpkg otherwise requires a host patchelf even for header-only Linux ports.
# Vosk only depends on system libraries; the executable sets its own RPATH.
set(VCPKG_FIXUP_ELF_RPATH OFF)

set(VCPKG_CHAINLOAD_TOOLCHAIN_FILE
    "${CMAKE_CURRENT_LIST_DIR}/../linux-x86_64.cmake"
)
