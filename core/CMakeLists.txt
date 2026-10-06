cmake_minimum_required(VERSION 3.20)
project(vloc CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

find_package(OpenCV REQUIRED COMPONENTS core imgproc imgcodecs features flann geometry)

set(VLOC_SOURCES src/vmap.cpp src/localize.cpp src/vloc_c.cpp)

add_library(vloc_obj OBJECT ${VLOC_SOURCES})
target_include_directories(vloc_obj PUBLIC include src ${OpenCV_INCLUDE_DIRS})
target_compile_options(vloc_obj PRIVATE -Wall -Wextra -Wno-unused-parameter)
set_target_properties(vloc_obj PROPERTIES POSITION_INDEPENDENT_CODE ON)

# Shared library for cgo. Its install name uses @rpath so the consumer sets the search path.
add_library(vloc SHARED $<TARGET_OBJECTS:vloc_obj>)
target_link_libraries(vloc PUBLIC ${OpenCV_LIBS})
target_include_directories(vloc PUBLIC include)
set_target_properties(vloc PROPERTIES INSTALL_NAME_DIR "@rpath" BUILD_WITH_INSTALL_NAME_DIR ON
                                      BUILD_RPATH "${CMAKE_PREFIX_PATH}/lib")

add_library(vloc_static STATIC $<TARGET_OBJECTS:vloc_obj>)
target_link_libraries(vloc_static PUBLIC ${OpenCV_LIBS})
target_include_directories(vloc_static PUBLIC include src ${OpenCV_INCLUDE_DIRS})

add_executable(vloc_build apps/vloc_build.cpp)
target_link_libraries(vloc_build PRIVATE vloc_static)
add_executable(vloc_eval apps/vloc_eval.cpp)
target_link_libraries(vloc_eval PRIVATE vloc_static)

find_package(GTest CONFIG)
if(GTest_FOUND)
  enable_testing()
  add_executable(vloc_tests tests/test_core.cpp)
  target_link_libraries(vloc_tests PRIVATE vloc_static GTest::gtest_main)
  add_test(NAME vloc_tests COMMAND vloc_tests)
endif()
