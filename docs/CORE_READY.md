# Core ready: how the service links libvloc

The C ABI in `core/include/vloc.h` is implemented and localizes real 7-Scenes frames.
Nothing in the header changed.

## Build the library

```bash
E=~/.local/envs/visualloc
$E/bin/cmake -S core -B core/build -G Ninja -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_PREFIX_PATH=$E -DCMAKE_MAKE_PROGRAM=$E/bin/ninja
$E/bin/cmake --build core/build
```

Produces `core/build/libvloc.dylib` (install name `@rpath/libvloc.dylib`) and
`core/build/libvloc_static.a`. Use the shared library from Go. The static one would need
every OpenCV library and `-lc++` on the cgo link line.

## cgo flags

```go
/*
#cgo CFLAGS: -I${SRCDIR}/../../../core/include
#cgo LDFLAGS: -L${SRCDIR}/../../../core/build -lvloc -Wl,-rpath,${SRCDIR}/../../../core/build
#include "vloc.h"
*/
```

`libvloc.dylib` already carries an `LC_RPATH` to the conda env
(`/Users/sanjithshanmugavel/.local/envs/visualloc/lib`), so its OpenCV dependencies resolve
without anything else. If a binary is moved off this Mac, add
`-Wl,-rpath,$HOME/.local/envs/visualloc/lib` or set `DYLD_LIBRARY_PATH` to both directories.

## Maps

Build a map (about 3 s for heads on 12 cores):

```bash
core/build/vloc_build --scene-dir ~/Data/7scenes/heads --out results/maps/heads.vmap
```

`*.vmap` is gitignored. Load it with `vloc_map_load`. Loading also builds the kd-forest
in memory (heads: 10.8k points, 0.05 s).

## Runtime notes

- Intrinsics for 7-Scenes color: fx = fy = 525, cx = 320, cy = 240.
- `vloc_localize` is thread-safe on one map (tested from 8 threads, results bit-identical).
  Results are deterministic per image.
- Each call is CPU bound, roughly 100 to 200 ms on this Mac, mostly SIFT extraction.
- `VLOC_CV_THREADS=N` in the environment, read on the first `vloc_map_load`, caps
  OpenCV's internal thread pool. Set it to 1 when the service runs one call per core.
- `vloc_localize` returns 0 whenever it ran. Check `ok`, and `err` holds the reason when
  `ok == 0` (decode failure, too few matches, no pose).
