#include "vmap.h"

#include <cstdio>
#include <cstring>
#include <cstdlib>

namespace vloc {

namespace {

struct FileCloser {
  void operator()(FILE* f) const { if (f) std::fclose(f); }
};
using File = std::unique_ptr<FILE, FileCloser>;

template <typename T>
bool wr(FILE* f, const T* p, size_t n) { return n == 0 || std::fwrite(p, sizeof(T), n, f) == n; }
template <typename T>
bool rd(FILE* f, T* p, size_t n) { return n == 0 || std::fread(p, sizeof(T), n, f) == n; }

}  // namespace

bool write_vmap(const std::string& path, const MapData& m, std::string* err) {
  if (m.desc.size() != m.num_desc() * size_t(m.dim) ||
      m.obs_offset.size() != m.num_points() + 1 ||
      m.obs_frame.size() != m.obs_offset.back()) {
    *err = "inconsistent map arrays";
    return false;
  }
  std::string tmp = path + ".tmp";
  File f(std::fopen(tmp.c_str(), "wb"));
  if (!f) { *err = "cannot open " + tmp; return false; }
  char name[128] = {0}, feature[32] = {0};
  std::strncpy(name, m.name.c_str(), sizeof(name) - 1);
  std::strncpy(feature, m.feature.c_str(), sizeof(feature) - 1);
  uint32_t hdr[5] = {uint32_t(m.dim), uint32_t(m.num_points()), uint32_t(m.num_desc()),
                     uint32_t(m.num_frames()), 0};
  bool ok = wr(f.get(), "VMAP", 4) && wr(f.get(), &kVmapVersion, 1) && wr(f.get(), name, 128) &&
            wr(f.get(), feature, 32) && wr(f.get(), hdr, 4) &&
            wr(f.get(), m.xyz.data(), m.xyz.size()) && wr(f.get(), m.desc.data(), m.desc.size()) &&
            wr(f.get(), m.desc_point.data(), m.desc_point.size()) &&
            wr(f.get(), m.obs_offset.data(), m.obs_offset.size()) &&
            wr(f.get(), m.obs_frame.data(), m.obs_frame.size()) &&
            wr(f.get(), m.frame_pose.data(), m.frame_pose.size());
  if (!ok || std::fclose(f.release()) != 0) { *err = "write failed"; return false; }
  if (std::rename(tmp.c_str(), path.c_str()) != 0) { *err = "rename failed"; return false; }
  return true;
}

bool read_vmap(const std::string& path, MapData* m, std::string* err) {
  File f(std::fopen(path.c_str(), "rb"));
  if (!f) { *err = "cannot open " + path; return false; }
  char magic[4];
  uint32_t version = 0;
  char name[128], feature[32];
  uint32_t hdr[4];
  if (!rd(f.get(), magic, 4) || std::memcmp(magic, "VMAP", 4) != 0) { *err = "bad magic"; return false; }
  if (!rd(f.get(), &version, 1) || version != kVmapVersion) {
    *err = "unsupported vmap version " + std::to_string(version);
    return false;
  }
  if (!rd(f.get(), name, 128) || !rd(f.get(), feature, 32) || !rd(f.get(), hdr, 4)) {
    *err = "truncated header";
    return false;
  }
  name[127] = 0; feature[31] = 0;
  const uint64_t dim = hdr[0], np = hdr[1], nd = hdr[2], nf = hdr[3];
  if (dim == 0 || dim > 1024 || np > (1u << 28) || nd > (1u << 28) || nf > (1u << 24)) {
    *err = "implausible header sizes";
    return false;
  }
  m->name = name;
  m->feature = feature;
  m->dim = int(dim);
  m->xyz.resize(np * 3);
  m->desc.resize(nd * dim);
  m->desc_point.resize(nd);
  m->obs_offset.resize(np + 1);
  if (!rd(f.get(), m->xyz.data(), m->xyz.size()) || !rd(f.get(), m->desc.data(), m->desc.size()) ||
      !rd(f.get(), m->desc_point.data(), nd) || !rd(f.get(), m->obs_offset.data(), np + 1)) {
    *err = "truncated body";
    return false;
  }
  const uint64_t nobs = m->obs_offset.back();
  if (nobs > (1ull << 32)) { *err = "bad obs count"; return false; }
  m->obs_frame.resize(nobs);
  m->frame_pose.resize(nf * 12);
  if (!rd(f.get(), m->obs_frame.data(), nobs) || !rd(f.get(), m->frame_pose.data(), nf * 12)) {
    *err = "truncated body";
    return false;
  }
  for (uint32_t p : m->desc_point)
    if (p >= np) { *err = "descriptor references missing point"; return false; }
  for (size_t i = 0; i < np; ++i)
    if (m->obs_offset[i] > m->obs_offset[i + 1]) { *err = "bad obs offsets"; return false; }
  return true;
}

std::unique_ptr<Map> build_index(MapData&& d, int trees) {
  auto map = std::make_unique<Map>();
  map->data = std::move(d);
  const int nd = int(map->data.num_desc());
  cv::Mat(nd, map->data.dim, CV_8U, map->data.desc.data()).convertTo(map->desc_f32, CV_32F);
  const size_t np = map->data.num_points(), nf = map->data.num_frames();
  map->point_desc.assign(np, UINT32_MAX);
  for (int i = nd - 1; i >= 0; --i) map->point_desc[map->data.desc_point[i]] = uint32_t(i);
  map->frame_offset.assign(nf + 1, 0);
  for (uint32_t f : map->data.obs_frame)
    if (f < nf) map->frame_offset[f + 1]++;
  for (size_t f = 0; f < nf; ++f) map->frame_offset[f + 1] += map->frame_offset[f];
  map->frame_points.resize(map->frame_offset[nf]);
  std::vector<uint32_t> fill(map->frame_offset.begin(), map->frame_offset.end() - 1);
  for (size_t p = 0; p < np; ++p)
    for (uint32_t o = map->data.obs_offset[p]; o < map->data.obs_offset[p + 1]; ++o) {
      uint32_t f = map->data.obs_frame[o];
      if (f < nf) map->frame_points[fill[f]++] = uint32_t(p);
    }
  if (nd > 0) {
    // cvflann seeds its tree randomisation from the C rand(); fix it so a map always
    // builds the same forest.
    std::srand(1234);
    map->index = std::make_unique<cv::flann::Index>(map->desc_f32, cv::flann::KDTreeIndexParams(trees),
                                                    cvflann::FLANN_DIST_L2);
  }
  return map;
}

}  // namespace vloc
