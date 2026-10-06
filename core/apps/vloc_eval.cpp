// vloc_eval: localizes every frame of a split against a map and scores it against GT.
#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdio>
#include <fstream>
#include <iterator>
#include <string>
#include <thread>
#include <vector>

#include <opencv2/core.hpp>

#include "dataset.h"
#include "localize.h"
#include "vloc.h"
#include "vmap.h"

using namespace vloc;

namespace {

struct Args {
  std::string map, scene_dir, split = "test", csv, summary;
  int threads = 4;
  int val_every = 0;
  int val_offset = 0;
  int limit = 0;
  int stride = 1;
  bool use_c_abi = false;
  LocalizeParams p;
};

bool parse(int argc, char** argv, Args* a) {
  for (int i = 1; i < argc; ++i) {
    std::string k = argv[i];
    auto next = [&]() -> const char* { return i + 1 < argc ? argv[++i] : ""; };
    if (k == "--map") a->map = next();
    else if (k == "--scene-dir") a->scene_dir = next();
    else if (k == "--split") a->split = next();
    else if (k == "--csv") a->csv = next();
    else if (k == "--summary") a->summary = next();
    else if (k == "--threads") a->threads = std::atoi(next());
    else if (k == "--val-every") a->val_every = std::atoi(next());
    else if (k == "--val-offset") a->val_offset = std::atoi(next());
    else if (k == "--limit") a->limit = std::atoi(next());
    else if (k == "--stride") a->stride = std::max(1, std::atoi(next()));
    else if (k == "--c-abi") a->use_c_abi = true;
    else if (k == "--ratio") a->p.ratio = std::atof(next());
    else if (k == "--checks") a->p.checks = std::atoi(next());
    else if (k == "--max-features") a->p.feat.max_features = std::atoi(next());
    else if (k == "--contrast") a->p.feat.contrast_threshold = std::atof(next());
    else if (k == "--match-budget") a->p.match_budget = std::atoi(next());
    else if (k == "--max-matches") a->p.max_matches = std::atoi(next());
    else if (k == "--ransac-px") a->p.ransac_px = std::atof(next());
    else if (k == "--ransac-iters") a->p.ransac_iters = std::atoi(next());
    else if (k == "--min-inliers") a->p.min_inliers = std::atoi(next());
    else if (k == "--no-refine") a->p.refine = false;
    else if (k == "--no-active") a->p.active_search = false;
    else if (k == "--as-radius") a->p.as_radius_px = std::atof(next());
    else if (k == "--as-ratio") a->p.as_ratio = std::atof(next());
    else if (k == "--pnp") a->p.pnp = std::atoi(next());
    else { std::fprintf(stderr, "unknown arg %s\n", k.c_str()); return false; }
  }
  return !a->map.empty() && !a->scene_dir.empty();
}

double pct(std::vector<double> v, double q) {
  if (v.empty()) return std::nan("");
  std::sort(v.begin(), v.end());
  double pos = q * (v.size() - 1);
  size_t lo = size_t(std::floor(pos)), hi = size_t(std::ceil(pos));
  if (lo == hi || v[lo] == v[hi]) return v[lo];
  return v[lo] + (v[hi] - v[lo]) * (pos - lo);
}

}  // namespace

int main(int argc, char** argv) {
  Args a;
  if (!parse(argc, argv, &a)) {
    std::fprintf(stderr,
                 "usage: vloc_eval --map M.vmap --scene-dir DIR [--split test|train --val-every N]\n"
                 "  [--csv out.csv] [--summary out.json] [--threads 4] [--limit N] [--stride S] [--c-abi]\n"
                 "  [--ratio 0.8] [--checks 64] [--max-features 4000] [--ransac-px 8] [--min-inliers 12]\n"
                 "  [--no-refine] [--no-active] [--as-radius 6] [--as-ratio 0.8] [--pnp 0|1]\n");
    return 2;
  }
  std::vector<Frame> all = list_frames(a.scene_dir, a.split), frames;
  for (size_t i = 0; i < all.size(); ++i) {
    if (a.split == "train" && !is_val_query(all[i], a.val_every, a.val_offset)) continue;
    if (i % a.stride) continue;
    frames.push_back(all[i]);
  }
  if (a.limit > 0 && int(frames.size()) > a.limit) frames.resize(a.limit);
  if (frames.empty()) { std::fprintf(stderr, "no frames\n"); return 1; }

  auto tl = std::chrono::steady_clock::now();
  vloc_map* cmap = nullptr;
  char err[256];
  if (vloc_map_load(a.map.c_str(), &cmap, err, sizeof(err)) != 0) {
    std::fprintf(stderr, "map load: %s\n", err);
    return 1;
  }
  double load_s = std::chrono::duration<double>(std::chrono::steady_clock::now() - tl).count();
  vloc_map_info info;
  vloc_map_get_info(cmap, &info);
  const Map& map = internal_map(cmap);
  std::fprintf(stderr, "[eval] map %s: %d points, loaded+indexed in %.2fs; %zu frames, %d threads\n",
               info.name, info.num_points, load_s, frames.size(), a.threads);

  const vloc_intrinsics K{525.0, 525.0, 320.0, 240.0};
  std::vector<vloc_result> res(frames.size());
  std::vector<double> terr(frames.size(), INFINITY), rerr(frames.size(), INFINITY), total(frames.size());
  cv::setNumThreads(1);  // parallelism is across frames
  std::atomic<int> next{0};
  auto T0 = std::chrono::steady_clock::now();
  std::vector<std::thread> ts;
  for (int t = 0; t < a.threads; ++t)
    ts.emplace_back([&]() {
      for (int i; (i = next.fetch_add(1)) < int(frames.size());) {
        std::ifstream f(frames[i].stem + ".color.jpg", std::ios::binary);
        std::vector<uint8_t> bytes((std::istreambuf_iterator<char>(f)), std::istreambuf_iterator<char>());
        auto t0 = std::chrono::steady_clock::now();
        if (a.use_c_abi) vloc_localize(cmap, bytes.data(), bytes.size(), &K, &res[i]);
        else localize(map, bytes.data(), bytes.size(), K, a.p, &res[i]);
        total[i] = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
        const vloc_result& r = res[i];
        if (!r.ok) continue;
        const cv::Matx44d& G = frames[i].pose;
        double dx = r.tx - G(0, 3), dy = r.ty - G(1, 3), dz = r.tz - G(2, 3);
        terr[i] = std::sqrt(dx * dx + dy * dy + dz * dz);
        // Rotation from quaternion, then angle of R_est^T R_gt.
        double w = r.qw, x = r.qx, y = r.qy, z = r.qz;
        cv::Matx33d R(1 - 2 * (y * y + z * z), 2 * (x * y - z * w), 2 * (x * z + y * w),
                      2 * (x * y + z * w), 1 - 2 * (x * x + z * z), 2 * (y * z - x * w),
                      2 * (x * z - y * w), 2 * (y * z + x * w), 1 - 2 * (x * x + y * y));
        cv::Matx33d Rg;
        for (int rr = 0; rr < 3; ++rr)
          for (int c = 0; c < 3; ++c) Rg(rr, c) = G(rr, c);
        cv::Matx33d D = R.t() * Rg;
        double c = std::clamp((D(0, 0) + D(1, 1) + D(2, 2) - 1.0) / 2.0, -1.0, 1.0);
        rerr[i] = std::acos(c) * 180.0 / CV_PI;
      }
    });
  for (auto& t : ts) t.join();
  double wall = std::chrono::duration<double>(std::chrono::steady_clock::now() - T0).count();

  int nok = 0, n5 = 0, n2 = 0, n10 = 0;
  std::vector<double> lat, ext, mat, pose, dec, inl;
  for (size_t i = 0; i < frames.size(); ++i) {
    nok += res[i].ok;
    if (terr[i] < 0.05 && rerr[i] < 5.0) n5++;
    if (terr[i] < 0.02 && rerr[i] < 2.0) n2++;
    if (terr[i] < 0.10 && rerr[i] < 10.0) n10++;
    lat.push_back(total[i]);
    dec.push_back(res[i].ms_decode); ext.push_back(res[i].ms_extract);
    mat.push_back(res[i].ms_match); pose.push_back(res[i].ms_pose);
    inl.push_back(res[i].num_inliers);
  }
  const double N = double(frames.size());
  // Failed frames count as infinite error, so the median is over all frames.
  if (!a.csv.empty()) {
    FILE* f = std::fopen(a.csv.c_str(), "w");
    std::fprintf(f, "frame,ok,trans_err_m,rot_err_deg,keypoints,matches,inliers,ms_decode,ms_extract,ms_match,ms_pose,ms_total\n");
    for (size_t i = 0; i < frames.size(); ++i) {
      const auto& r = res[i];
      std::fprintf(f, "%s/frame-%06d,%d,%.5f,%.4f,%d,%d,%d,%.2f,%.2f,%.2f,%.2f,%.2f\n", frames[i].seq.c_str(),
                   frames[i].index, r.ok, r.ok ? terr[i] : -1.0, r.ok ? rerr[i] : -1.0, r.num_keypoints,
                   r.num_matches, r.num_inliers, r.ms_decode, r.ms_extract, r.ms_match, r.ms_pose, total[i]);
    }
    std::fclose(f);
  }
  std::string cmd;
  for (int i = 0; i < argc; ++i) { if (i) cmd += ' '; cmd += argv[i]; }
  char buf[4096];
  std::snprintf(buf, sizeof(buf),
                "{\n  \"scene\": \"%s\",\n  \"split\": \"%s\",\n  \"frames\": %zu,\n  \"localized\": %d,\n"
                "  \"median_trans_cm\": %.2f,\n  \"median_rot_deg\": %.3f,\n"
                "  \"pct_within_5cm_5deg\": %.2f,\n  \"pct_within_2cm_2deg\": %.2f,\n  \"pct_within_10cm_10deg\": %.2f,\n"
                "  \"latency_ms_p50\": %.1f,\n  \"latency_ms_p90\": %.1f,\n  \"latency_ms_p99\": %.1f,\n"
                "  \"stage_ms_p50\": {\"decode\": %.1f, \"extract\": %.1f, \"match\": %.1f, \"pose\": %.1f},\n"
                "  \"median_inliers\": %.0f,\n  \"threads\": %d,\n  \"wall_s\": %.1f,\n  \"throughput_fps\": %.1f,\n"
                "  \"map_points\": %d,\n  \"map_load_s\": %.2f,\n  \"command\": \"%s\"\n}\n",
                info.name, a.split.c_str(), frames.size(), nok, pct(terr, 0.5) * 100.0, pct(rerr, 0.5),
                100.0 * n5 / N, 100.0 * n2 / N, 100.0 * n10 / N, pct(lat, 0.5), pct(lat, 0.9), pct(lat, 0.99),
                pct(dec, 0.5), pct(ext, 0.5), pct(mat, 0.5), pct(pose, 0.5), pct(inl, 0.5), a.threads, wall,
                N / wall, info.num_points, load_s, cmd.c_str());
  std::fputs(buf, stdout);
  if (!a.summary.empty()) {
    FILE* f = std::fopen(a.summary.c_str(), "w");
    std::fputs(buf, f);
    std::fclose(f);
  }
  vloc_map_free(cmap);
  return 0;
}
