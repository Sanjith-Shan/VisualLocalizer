// dataset.h: 7-Scenes layout helpers shared by the builder and the evaluator.
#pragma once

#include <algorithm>
#include <cstdio>
#include <filesystem>
#include <fstream>
#include <string>
#include <vector>

#include <opencv2/core.hpp>

namespace vloc {

struct Frame {
  std::string seq;       // "seq-01"
  int index = 0;         // frame number in the file name
  int pos_in_seq = 0;    // position in the sorted, already-subsampled list of its sequence
  std::string stem;      // full path without ".color.jpg"
  cv::Matx44d pose;      // camera-to-world
};

inline bool read_pose(const std::string& path, cv::Matx44d* T) {
  std::ifstream f(path);
  if (!f) return false;
  for (int i = 0; i < 16; ++i)
    if (!(f >> (*T)(i / 4, i % 4))) return false;
  for (int i = 0; i < 16; ++i)
    if (!std::isfinite((*T)(i / 4, i % 4))) return false;
  return true;
}

// Lists <scene_dir>/<split>/seq-XX/frame-NNNNNN.pose.txt, sorted by sequence then frame.
// Frames with a missing or non-finite pose are skipped.
inline std::vector<Frame> list_frames(const std::string& scene_dir, const std::string& split) {
  namespace fs = std::filesystem;
  std::vector<Frame> out;
  fs::path root = fs::path(scene_dir) / split;
  if (!fs::exists(root)) return out;
  std::vector<fs::path> seqs;
  for (auto& e : fs::directory_iterator(root))
    if (e.is_directory()) seqs.push_back(e.path());
  std::sort(seqs.begin(), seqs.end());
  for (auto& sp : seqs) {
    std::vector<Frame> fr;
    for (auto& e : fs::directory_iterator(sp)) {
      const std::string n = e.path().filename().string();
      const std::string suf = ".pose.txt";
      if (n.size() < suf.size() || n.compare(n.size() - suf.size(), suf.size(), suf) != 0) continue;
      int idx = 0;
      if (std::sscanf(n.c_str(), "frame-%d", &idx) != 1) continue;
      Frame f;
      f.seq = sp.filename().string();
      f.index = idx;
      f.stem = (sp / n.substr(0, n.size() - suf.size())).string();
      if (!read_pose(e.path().string(), &f.pose)) continue;
      fr.push_back(f);
    }
    std::sort(fr.begin(), fr.end(), [](const Frame& a, const Frame& b) { return a.index < b.index; });
    for (size_t i = 0; i < fr.size(); ++i) fr[i].pos_in_seq = int(i);
    out.insert(out.end(), fr.begin(), fr.end());
  }
  return out;
}

// Validation split on training data, used only for tuning: frames with pos % every == 0
// are queries, and the map leaves out those frames and their immediate neighbours, so the
// nearest map frame is at least two subsampled steps away.
inline bool is_val_query(const Frame& f, int every) { return every > 0 && f.pos_in_seq % every == 0; }
inline bool is_val_excluded(const Frame& f, int every) {
  if (every <= 0) return false;
  int r = f.pos_in_seq % every;
  return r == 0 || r == 1 || r == every - 1;
}

}  // namespace vloc
