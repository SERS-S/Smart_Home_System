# Runtime data is kept in the build tree, alongside the Cloud binaries.
if(NOT DEFINED SHS_VOICE_MODEL_ROOT OR SHS_VOICE_MODEL_ROOT STREQUAL "")
    message(FATAL_ERROR "SHS_VOICE_MODEL_ROOT must point to the build models directory")
endif()

set(model_name "vosk-model-small-ru-0.22")
set(model_hash "961d5ff98a17f4aa6de69864d0aa71fa5bac682301d2b5d17a3f24c5c99a46d4")
set(model_dir "${SHS_VOICE_MODEL_ROOT}/${model_name}")
set(stamp "${model_dir}/.shs-ready")
set(required_files
    am/final.mdl conf/model.conf conf/mfcc.conf
    graph/HCLr.fst graph/Gr.fst graph/disambig_tid.int
    graph/phones/word_boundary.int
    ivector/final.ie ivector/final.dubm ivector/final.mat
    ivector/global_cmvn.stats ivector/splice.conf ivector/online_cmvn.conf
)

set(model_ready TRUE)
foreach(required_file IN LISTS required_files)
    if(NOT EXISTS "${model_dir}/${required_file}")
        set(model_ready FALSE)
    else()
        file(SIZE "${model_dir}/${required_file}" model_file_size)
        if(model_file_size EQUAL 0)
            set(model_ready FALSE)
        endif()
    endif()
endforeach()
if(model_ready AND EXISTS "${stamp}")
    file(READ "${stamp}" stamp_hash)
    if(stamp_hash STREQUAL model_hash)
        return()
    endif()
endif()

file(MAKE_DIRECTORY "${SHS_VOICE_MODEL_ROOT}")
set(archive "${SHS_VOICE_MODEL_ROOT}/${model_name}.zip")
message(STATUS "Preparing Russian Voice model ${model_name}")
file(DOWNLOAD
    "https://alphacephei.com/vosk/models/${model_name}.zip"
    "${archive}"
    EXPECTED_HASH "SHA256=${model_hash}"
    TLS_VERIFY ON
    TIMEOUT 300
    INACTIVITY_TIMEOUT 30
    SHOW_PROGRESS
)

execute_process(
    COMMAND "${CMAKE_COMMAND}" -E tar xf "${archive}"
    WORKING_DIRECTORY "${SHS_VOICE_MODEL_ROOT}"
    RESULT_VARIABLE extract_result
)
if(NOT extract_result EQUAL 0)
    message(FATAL_ERROR "Could not extract the Voice model: ${extract_result}")
endif()
foreach(required_file IN LISTS required_files)
    if(NOT EXISTS "${model_dir}/${required_file}")
        message(FATAL_ERROR "Voice model is missing ${required_file}")
    endif()
endforeach()
file(WRITE "${stamp}" "${model_hash}")
