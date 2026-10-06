// C ABI over the C++ core. No exception may cross this boundary.
#include <cstdio>
#include <cstring>
#include <cstdlib>
#include <exception>
#include <mutex>

#include <opencv2/core.hpp>

#include "localize.h"
#include "vloc.h"
#include "vmap.h"

struct vloc_map {
  std::unique_ptr<vloc::Map> map;
};

const vloc::Map& vloc::internal_map(const vloc_map* m) { return *m->map; }

namespace {
void copy_err(char* dst, size_t n, const std::string& msg) {
  if (dst && n > 0) std::snprintf(dst, n, "%s", msg.c_str());
}
}  // namespace

extern "C" int vloc_map_load(const char* path, vloc_map** out, char* err, size_t errlen) {
  if (!path || !out) { copy_err(err, errlen, "null argument"); return -1; }
  *out = nullptr;
  // VLOC_CV_THREADS caps OpenCV's internal thread pool (process-wide). A server that
  // already runs one localize per core should set it to 1 to avoid oversubscription.
  static std::once_flag once;
  std::call_once(once, [] {
    if (const char* s = std::getenv("VLOC_CV_THREADS")) {
      int n = std::atoi(s);
      if (n > 0) cv::setNumThreads(n);
    }
  });
  try {
    vloc::MapData d;
    std::string e;
    if (!vloc::read_vmap(path, &d, &e)) { copy_err(err, errlen, e); return -1; }
    auto* m = new vloc_map;
    m->map = vloc::build_index(std::move(d));
    *out = m;
    return 0;
  } catch (const std::exception& ex) {
    copy_err(err, errlen, std::string("load failed: ") + ex.what());
  } catch (...) {
    copy_err(err, errlen, "load failed: unknown exception");
  }
  return -1;
}

extern "C" void vloc_map_free(vloc_map* map) { delete map; }

extern "C" int vloc_map_get_info(const vloc_map* map, vloc_map_info* out) {
  if (!map || !out) return -1;
  std::memset(out, 0, sizeof(*out));
  const auto& d = map->map->data;
  out->num_points = int(d.num_points());
  out->descriptor_dim = d.dim;
  std::snprintf(out->feature, sizeof(out->feature), "%s", d.feature.c_str());
  std::snprintf(out->name, sizeof(out->name), "%s", d.name.c_str());
  return 0;
}

extern "C" int vloc_localize(const vloc_map* map, const uint8_t* image, size_t image_len,
                             const vloc_intrinsics* K, vloc_result* out) {
  if (!out) return -1;
  std::memset(out, 0, sizeof(*out));
  if (!map || !K) { std::snprintf(out->err, sizeof(out->err), "null argument"); return -1; }
  try {
    vloc::localize(*map->map, image, image_len, *K, vloc::LocalizeParams{}, out);
  } catch (const std::exception& ex) {
    out->ok = 0;
    std::snprintf(out->err, sizeof(out->err), "localize failed: %s", ex.what());
  } catch (...) {
    out->ok = 0;
    std::snprintf(out->err, sizeof(out->err), "localize failed: unknown exception");
  }
  return 0;
}
