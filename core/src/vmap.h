// vmap.h: in-memory map and the binary .vmap file format.
#pragma once

#include <cstdint>
#include <memory>
#include <string>
#include <vector>

#include <opencv2/core.hpp>
#include <opencv2/flann.hpp>

namespace vloc {

// File layout, little endian, version 1:
//   char[4]  magic "VMAP"
//   u32      version
//   char[128] name, char[32] feature
//   u32 dim, u32 num_points, u32 num_desc, u32 num_frames
//   f32[num_points*3]      point xyz, world frame, meters
//   u8 [num_desc*dim]      descriptors (SIFT scale, 0..255)
//   u32[num_desc]          descriptor -> point id
//   u32[num_points+1]      observation offsets (CSR)
//   u32[num_obs]           observing frame ids
//   f32[num_frames*12]     frame poses, camera-to-world, row-major 3x4
inline constexpr uint32_t kVmapVersion = 1;

struct MapData {
  std::string name;
  std::string feature = "sift";
  int dim = 128;
  std::vector<float> xyz;              // 3 * num_points
  std::vector<uint8_t> desc;           // dim * num_desc
  std::vector<uint32_t> desc_point;    // num_desc
  std::vector<uint32_t> obs_offset;    // num_points + 1
  std::vector<uint32_t> obs_frame;     // num_obs
  std::vector<float> frame_pose;       // 12 * num_frames

  size_t num_points() const { return xyz.size() / 3; }
  size_t num_desc() const { return desc_point.size(); }
  size_t num_frames() const { return frame_pose.size() / 12; }
};

bool write_vmap(const std::string& path, const MapData& m, std::string* err);
bool read_vmap(const std::string& path, MapData* m, std::string* err);

// A loaded map with its search index. Immutable after construction, so concurrent
// const access needs no locking.
struct Map {
  MapData data;
  cv::Mat desc_f32;                        // num_desc x dim, CV_32F, index backing store
  std::unique_ptr<cv::flann::Index> index; // randomized kd-forest
  std::vector<uint32_t> point_desc;        // first descriptor id of each point
  std::vector<uint32_t> frame_offset;      // CSR: frame -> observed points
  std::vector<uint32_t> frame_points;
};

std::unique_ptr<Map> build_index(MapData&& d, int trees = 4);

}  // namespace vloc
