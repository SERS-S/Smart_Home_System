set(CMAKE_SYSTEM_NAME Linux)
set(CMAKE_SYSTEM_PROCESSOR x86_64)

find_program(ZIG_EXECUTABLE NAMES zig REQUIRED)

set(CMAKE_C_COMPILER
    "${ZIG_EXECUTABLE}" cc -target x86_64-linux-musl
)

set(CMAKE_CXX_COMPILER
    "${ZIG_EXECUTABLE}" c++ -target x86_64-linux-musl
)

set(CMAKE_EXE_LINKER_FLAGS_INIT "-static")
