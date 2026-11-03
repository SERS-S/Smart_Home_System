vcpkg_check_linkage(ONLY_DYNAMIC_LIBRARY)

vcpkg_download_distfile(ARCHIVE
    URLS
        "https://github.com/alphacep/vosk-api/releases/download/v${VERSION}/vosk-linux-x86_64-${VERSION}.zip"
    FILENAME "vosk-linux-x86_64-${VERSION}.zip"
    SHA512
        1ae02b4305c2b1fa0213821d57f1888ba2eaf5eb160f40968a22065ffe560640816630826127acb84637310acdc32eadbdc0e38229b79b2d6ebfcc49a55d561b
)

vcpkg_extract_source_archive(SOURCE_PATH
    ARCHIVE "${ARCHIVE}"
)

file(INSTALL "${SOURCE_PATH}/vosk_api.h"
    DESTINATION "${CURRENT_PACKAGES_DIR}/include"
)

file(INSTALL "${SOURCE_PATH}/libvosk.so"
    DESTINATION "${CURRENT_PACKAGES_DIR}/lib"
)

if(NOT VCPKG_BUILD_TYPE)
    file(INSTALL "${SOURCE_PATH}/libvosk.so"
        DESTINATION "${CURRENT_PACKAGES_DIR}/debug/lib"
    )
endif()

file(INSTALL "${CMAKE_CURRENT_LIST_DIR}/copyright"
    DESTINATION "${CURRENT_PACKAGES_DIR}/share/${PORT}"
)
