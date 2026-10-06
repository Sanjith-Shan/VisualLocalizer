# Setup

Everything builds against a project-local conda env. Nothing is installed globally and
nothing comes from Homebrew.

## Toolchain (Apple Silicon, macOS)

```bash
conda create -y -p ~/.local/envs/visualloc --override-channels -c conda-forge \
  "libopencv=*=headless*" cmake ninja eigen numpy scipy pkg-config gtest python=3.12
```

This resolves to OpenCV 5.0.0 (headless build, no Qt or VTK), CMake 4.4, Ninja 1.13,
Eigen 5.0 and GoogleTest 1.18. The env takes about 850 MB. The compiler is the system
Apple clang (Xcode), the libraries come from the env.

OpenCV 5 renamed some modules. The core links `core`, `imgproc`, `imgcodecs`, `features`
(SIFT lives here now), `flann` and `geometry` (`solvePnPRansac` lives here now).

## Build

```bash
E=~/.local/envs/visualloc
$E/bin/cmake -S core -B core/build -G Ninja -DCMAKE_BUILD_TYPE=Release -DCMAKE_PREFIX_PATH=$E \
  -DCMAKE_MAKE_PROGRAM=$E/bin/ninja
$E/bin/cmake --build core/build
(cd core/build && $E/bin/ctest --output-on-failure)
```

Outputs in `core/build/`: `libvloc.dylib`, `libvloc_static.a`, `vloc_build`, `vloc_eval`,
`vloc_tests`.
