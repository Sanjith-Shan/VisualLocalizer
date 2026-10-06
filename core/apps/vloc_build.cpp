// vloc_build: builds a .vmap from 7-Scenes training frames.
//
// Pipeline: SIFT per frame, pairwise matching between nearby frames of a sequence
// (ratio test + epipolar check against the ground-truth relative pose), union-find
// into tracks, then one 3D point per track from either multi-view triangulation with
// the GT poses ("tri"), the depth image registered to the color camera ("depth"), or
// triangulation with a depth fallback for low-parallax tracks ("hybrid").
#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <map>
#include <numeric>
#include <string>
#include <thread>
#include <vector>

#include <opencv2/core.hpp>
#include <opencv2/features.hpp>
#include <opencv2/flann.hpp>
#include <opencv2/imgcodecs.hpp>

#include "dataset.h"
#include "localize.h"
#include "vmap.h"

using namespace vloc;

namespace {

struct Args {
  std::string scene_dir, out, name, mode = "depth";
  int window = 3;            // match frame i against i+1..i+window (subsampled steps)
  double match_ratio = 0.8;
  double epi_px = 3.0;       // Sampson distance bound against the GT relative pose
  double reproj_px = 4.0;    // max reprojection error of a triangulated observation
  double min_angle_deg = 2.0;
  int min_track = 2;
  bool depth_singletons = true;
  int val_every = 0;         // >0: leave validation frames out of the map
  int val_offset = 0;
  int val_gap = 1;
  int threads = 0;
  int max_frames = 0;
  // Depth-to-color registration (depth camera fx=fy=585, cx=320, cy=240).
  double depth_f = 585.0, depth_cx = 320.0, depth_cy = 240.0;
  double dc_tx = -0.025, dc_ty = 0.0, dc_tz = 0.0;  // depth camera origin in color camera frame, m
  std::string pose_file;     // external train poses (e.g. SfM pseudo GT)
  std::string provenance;    // free text recorded in the summary (git SHA)
  FeatureParams feat;
};

struct FrameData {
  std::vector<cv::KeyPoint> kps;
  cv::Mat desc;                  // CV_32F
  std::vector<float> depth;      // per keypoint, meters, 0 = none (color camera z)
  cv::Matx33d Rcw;               // camera-to-world
  cv::Vec3d tcw;
};

// Color intrinsics. 7-Scenes uses f = 525; an SfM pose file brings its own focal.
cv::Matx33d kColorK(525, 0, 320, 0, 525, 240, 0, 0, 1);

int parse_int(const char* s) { return std::atoi(s); }

void usage() {
  std::fprintf(stderr,
               "usage: vloc_build --scene-dir DIR --out MAP.vmap [--name N] [--mode depth|tri|hybrid]\n"
               "  [--window 3] [--match-ratio 0.8] [--epi-px 3] [--reproj-px 4] [--min-angle 2]\n"
               "  [--min-track 2] [--no-depth-singletons] [--contrast 0.01] [--val-every N] [--threads T]\n"
               "  [--max-features 4000] [--no-rootsift] [--dc-tx M] [--dc-ty M] [--dc-tz M]\n"
               "  [--pose-file pgt_train.txt] [--provenance TEXT]\n");
}

bool parse(int argc, char** argv, Args* a) {
  for (int i = 1; i < argc; ++i) {
    std::string k = argv[i];
    auto next = [&]() -> const char* { return i + 1 < argc ? argv[++i] : ""; };
    if (k == "--scene-dir") a->scene_dir = next();
    else if (k == "--out") a->out = next();
    else if (k == "--name") a->name = next();
    else if (k == "--mode") a->mode = next();
    else if (k == "--window") a->window = parse_int(next());
    else if (k == "--match-ratio") a->match_ratio = std::atof(next());
    else if (k == "--epi-px") a->epi_px = std::atof(next());
    else if (k == "--reproj-px") a->reproj_px = std::atof(next());
    else if (k == "--min-angle") a->min_angle_deg = std::atof(next());
    else if (k == "--min-track") a->min_track = parse_int(next());
    else if (k == "--depth-singletons") a->depth_singletons = true;
    else if (k == "--no-depth-singletons") a->depth_singletons = false;
    else if (k == "--val-every") a->val_every = parse_int(next());
    else if (k == "--val-offset") a->val_offset = parse_int(next());
    else if (k == "--val-gap") a->val_gap = parse_int(next());
    else if (k == "--threads") a->threads = parse_int(next());
    else if (k == "--max-frames") a->max_frames = parse_int(next());
    else if (k == "--max-features") a->feat.max_features = parse_int(next());
    else if (k == "--contrast") a->feat.contrast_threshold = std::atof(next());
    else if (k == "--no-rootsift") a->feat.root_sift = false;
    else if (k == "--dc-tx") a->dc_tx = std::atof(next());
    else if (k == "--dc-ty") a->dc_ty = std::atof(next());
    else if (k == "--dc-tz") a->dc_tz = std::atof(next());
    else if (k == "--pose-file") a->pose_file = next();
    else if (k == "--provenance") a->provenance = next();
    else { std::fprintf(stderr, "unknown arg %s\n", k.c_str()); return false; }
  }
  if (a->scene_dir.empty() || a->out.empty()) return false;
  if (a->mode != "tri" && a->mode != "depth" && a->mode != "hybrid") return false;
  if (a->threads <= 0) a->threads = int(std::thread::hardware_concurrency());
  if (a->name.empty()) a->name = std::filesystem::path(a->scene_dir).filename().string();
  return true;
}

template <typename F>
void parallel_for(int n, int threads, F&& fn) {
  std::atomic<int> next{0};
  std::vector<std::thread> ts;
  for (int t = 0; t < threads; ++t)
    ts.emplace_back([&]() {
      for (int i; (i = next.fetch_add(1)) < n;) fn(i);
    });
  for (auto& t : ts) t.join();
}

// Registers a 16-bit depth image (mm, 0 or 65535 invalid) into the color camera with a
// z-buffer, then reads a depth for each keypoint (nearest valid within 1 px, smallest z).
std::vector<float> keypoint_depths(const cv::Mat& depth_mm, const std::vector<cv::KeyPoint>& kps,
                                   const Args& a) {
  const int W = 640, H = 480;
  cv::Mat reg(H, W, CV_32F, cv::Scalar(0));
  for (int v = 0; v < depth_mm.rows; ++v) {
    const uint16_t* row = depth_mm.ptr<uint16_t>(v);
    for (int u = 0; u < depth_mm.cols; ++u) {
      const uint16_t d = row[u];
      if (d == 0 || d == 65535) continue;
      const double z = d * 1e-3;
      double X = (u - a.depth_cx) * z / a.depth_f + a.dc_tx;
      double Y = (v - a.depth_cy) * z / a.depth_f + a.dc_ty;
      double Z = z + a.dc_tz;
      if (Z <= 0.1) continue;
      int uc = int(std::lround(kColorK(0, 0) * X / Z + kColorK(0, 2)));
      int vc = int(std::lround(kColorK(1, 1) * Y / Z + kColorK(1, 2)));
      if (uc < 0 || vc < 0 || uc >= W || vc >= H) continue;
      float& r = reg.at<float>(vc, uc);
      if (r == 0 || Z < r) r = float(Z);
    }
  }
  std::vector<float> out(kps.size(), 0.f);
  for (size_t i = 0; i < kps.size(); ++i) {
    int u = int(std::lround(kps[i].pt.x)), v = int(std::lround(kps[i].pt.y));
    float best = 0;
    float c = (u >= 0 && v >= 0 && u < W && v < H) ? reg.at<float>(v, u) : 0.f;
    if (c > 0) { out[i] = c; continue; }
    for (int dv = -1; dv <= 1; ++dv)
      for (int du = -1; du <= 1; ++du) {
        int x = u + du, y = v + dv;
        if (x < 0 || y < 0 || x >= W || y >= H) continue;
        float z = reg.at<float>(y, x);
        if (z > 0 && (best == 0 || z < best)) best = z;
      }
    out[i] = best;
  }
  return out;
}

struct UF {
  std::vector<int> p;
  explicit UF(int n) : p(n) { std::iota(p.begin(), p.end(), 0); }
  int find(int x) { while (p[x] != x) x = p[x] = p[p[x]]; return x; }
  void unite(int a, int b) { a = find(a); b = find(b); if (a != b) p[std::max(a, b)] = std::min(a, b); }
};

// World-to-camera projection matrix.
cv::Matx34d proj(const FrameData& f) {
  cv::Matx33d Rwc = f.Rcw.t();
  cv::Vec3d twc = -(Rwc * f.tcw);
  cv::Matx34d P;
  for (int r = 0; r < 3; ++r) {
    for (int c = 0; c < 3; ++c) P(r, c) = Rwc(r, c);
    P(r, 3) = twc[r];
  }
  return kColorK * P;
}

bool triangulate(const std::vector<cv::Matx34d>& Ps, const std::vector<cv::Point2f>& xs, cv::Vec3d* X) {
  const int n = int(Ps.size());
  cv::Mat A(2 * n, 4, CV_64F);
  for (int i = 0; i < n; ++i) {
    for (int c = 0; c < 4; ++c) {
      A.at<double>(2 * i, c) = xs[i].x * Ps[i](2, c) - Ps[i](0, c);
      A.at<double>(2 * i + 1, c) = xs[i].y * Ps[i](2, c) - Ps[i](1, c);
    }
  }
  cv::Mat w, u, vt;
  cv::SVD::compute(A, w, u, vt, cv::SVD::MODIFY_A);
  double h = vt.at<double>(3, 3);
  if (std::abs(h) < 1e-12) return false;
  *X = cv::Vec3d(vt.at<double>(3, 0) / h, vt.at<double>(3, 1) / h, vt.at<double>(3, 2) / h);
  return true;
}

double reproj_err(const cv::Matx34d& P, const cv::Vec3d& X, const cv::Point2f& x, double* depth) {
  cv::Vec3d p = P * cv::Vec4d(X[0], X[1], X[2], 1.0);
  *depth = p[2];
  if (p[2] <= 0) return 1e9;
  double du = p[0] / p[2] - x.x, dv = p[1] / p[2] - x.y;
  return std::sqrt(du * du + dv * dv);
}

}  // namespace

int main(int argc, char** argv) {
  Args a;
  if (!parse(argc, argv, &a)) { usage(); return 2; }
  auto T0 = std::chrono::steady_clock::now();
  PoseFile pf;
  if (!a.pose_file.empty()) {
    if (!read_pose_file(a.pose_file, &pf)) { std::fprintf(stderr, "cannot read %s\n", a.pose_file.c_str()); return 1; }
    kColorK(0, 0) = kColorK(1, 1) = pf.focal;
  }
  std::vector<Frame> all = list_frames(a.scene_dir, "train", a.pose_file.empty() ? nullptr : &pf);
  std::vector<Frame> frames;
  for (auto& f : all)
    if (!is_val_excluded(f, a.val_every, a.val_offset, a.val_gap)) frames.push_back(f);
  if (a.max_frames > 0 && int(frames.size()) > a.max_frames) frames.resize(a.max_frames);
  const int nf = int(frames.size());
  if (nf == 0) { std::fprintf(stderr, "no training frames under %s\n", a.scene_dir.c_str()); return 1; }
  std::fprintf(stderr, "[build] %d map frames (of %zu train), mode=%s\n", nf, all.size(), a.mode.c_str());

  const bool need_depth = a.mode != "tri";
  std::vector<FrameData> fd(nf);
  cv::setNumThreads(1);
  std::atomic<int> failed{0};
  parallel_for(nf, a.threads, [&](int i) {
    cv::Mat gray = cv::imread(frames[i].stem + ".color.jpg", cv::IMREAD_GRAYSCALE);
    if (gray.empty()) { failed++; return; }
    extract_features(gray, a.feat, &fd[i].kps, &fd[i].desc);
    for (int r = 0; r < 3; ++r)
      for (int c = 0; c < 3; ++c) fd[i].Rcw(r, c) = frames[i].pose(r, c);
    fd[i].tcw = cv::Vec3d(frames[i].pose(0, 3), frames[i].pose(1, 3), frames[i].pose(2, 3));
    if (need_depth) {
      cv::Mat d = cv::imread(frames[i].stem + ".depth.png", cv::IMREAD_UNCHANGED);
      if (!d.empty() && d.type() == CV_16U) fd[i].depth = keypoint_depths(d, fd[i].kps, a);
    }
  });
  if (failed) { std::fprintf(stderr, "[build] %d frames failed to load\n", int(failed)); return 1; }
  std::vector<int> base(nf + 1, 0);
  for (int i = 0; i < nf; ++i) base[i + 1] = base[i] + int(fd[i].kps.size());
  std::fprintf(stderr, "[build] features: %d total, %.1fs\n", base[nf],
               std::chrono::duration<double>(std::chrono::steady_clock::now() - T0).count());

  // Pairs: same sequence, within the window.
  std::vector<std::pair<int, int>> pairs;
  for (int i = 0; i < nf; ++i)
    for (int j = i + 1; j < nf && j <= i + a.window; ++j)
      if (frames[j].seq == frames[i].seq) pairs.emplace_back(i, j);
  std::vector<std::vector<std::pair<int, int>>> pm(pairs.size());
  parallel_for(int(pairs.size()), a.threads, [&](int pi) {
    const auto [i, j] = pairs[pi];
    const FrameData &A = fd[i], &B = fd[j];
    if (A.desc.empty() || B.desc.empty()) return;
    // Fundamental matrix from the GT relative pose: x_j^T F x_i = 0.
    cv::Matx33d Rj = B.Rcw.t() * A.Rcw;               // cam i -> cam j rotation
    cv::Vec3d tj = B.Rcw.t() * (A.tcw - B.tcw);       // cam i origin in cam j
    cv::Matx33d tx(0, -tj[2], tj[1], tj[2], 0, -tj[0], -tj[1], tj[0], 0);
    cv::Matx33d Ki = kColorK.inv();
    cv::Matx33d F = Ki.t() * (tx * Rj) * Ki;
    cv::BFMatcher bf(cv::NORM_L2);
    std::vector<std::vector<cv::DMatch>> knn;
    bf.knnMatch(A.desc, B.desc, knn, 2);
    std::vector<int> best_for_b(B.kps.size(), -1);
    std::vector<float> best_d(B.kps.size(), 1e30f);
    for (auto& m : knn) {
      if (m.size() < 2 || m[0].distance > a.match_ratio * m[1].distance) continue;
      const cv::Point2f& x1 = A.kps[m[0].queryIdx].pt;
      const cv::Point2f& x2 = B.kps[m[0].trainIdx].pt;
      cv::Vec3d p1(x1.x, x1.y, 1), p2(x2.x, x2.y, 1);
      cv::Vec3d Fx1 = F * p1, Ftx2 = F.t() * p2;
      double num = p2.dot(Fx1);
      double den = Fx1[0] * Fx1[0] + Fx1[1] * Fx1[1] + Ftx2[0] * Ftx2[0] + Ftx2[1] * Ftx2[1];
      if (den <= 0 || num * num / den > a.epi_px * a.epi_px) continue;
      int b = m[0].trainIdx;
      if (m[0].distance < best_d[b]) { best_d[b] = m[0].distance; best_for_b[b] = m[0].queryIdx; }
    }
    for (size_t b = 0; b < best_for_b.size(); ++b)
      if (best_for_b[b] >= 0) pm[pi].emplace_back(best_for_b[b], int(b));
  });
  UF uf(base[nf]);
  size_t nmatch = 0;
  for (size_t pi = 0; pi < pairs.size(); ++pi)
    for (auto& [qa, qb] : pm[pi]) { uf.unite(base[pairs[pi].first] + qa, base[pairs[pi].second] + qb); nmatch++; }
  std::fprintf(stderr, "[build] %zu pairs, %zu verified matches, %.1fs\n", pairs.size(), nmatch,
               std::chrono::duration<double>(std::chrono::steady_clock::now() - T0).count());

  // Gather tracks.
  std::vector<int> frame_of(base[nf]);
  for (int i = 0; i < nf; ++i)
    for (int k = base[i]; k < base[i + 1]; ++k) frame_of[k] = i;
  std::map<int, std::vector<int>> tracks_map;
  for (int n = 0; n < base[nf]; ++n) tracks_map[uf.find(n)].push_back(n);
  std::vector<std::vector<int>> tracks;
  tracks.reserve(tracks_map.size());
  for (auto& [r, t] : tracks_map) tracks.push_back(std::move(t));
  tracks_map.clear();

  std::vector<cv::Matx34d> P(nf);
  for (int i = 0; i < nf; ++i) P[i] = proj(fd[i]);

  struct Point {
    cv::Vec3d X;
    std::vector<int> obs;  // node ids
    int source;            // 0 tri, 1 depth
  };
  std::vector<Point> pts;
  size_t dropped_conflict = 0, dropped_reproj = 0, dropped_angle = 0, dropped_depth = 0;
  std::vector<double> tri_vs_depth;  // 3D distance where both are available, meters
  std::vector<double> depth_reproj;  // reprojection error of depth points in the other views
  const double min_cos = std::cos(a.min_angle_deg * CV_PI / 180.0);
  auto kp_world = [&](int node) -> std::pair<bool, cv::Vec3d> {
    const int f = frame_of[node], k = node - base[f];
    if (fd[f].depth.empty()) return {false, {}};
    float z = fd[f].depth[k];
    if (z <= 0) return {false, {}};
    const auto& pt = fd[f].kps[k].pt;
    cv::Vec3d Xc((pt.x - kColorK(0, 2)) * z / kColorK(0, 0), (pt.y - kColorK(1, 2)) * z / kColorK(1, 1), z);
    return {true, fd[f].Rcw * Xc + fd[f].tcw};
  };

  for (auto& t : tracks) {
    if (int(t.size()) < std::max(1, a.min_track) && !(a.depth_singletons && need_depth && t.size() == 1)) continue;
    // A track may hit one frame twice; keep the first node per frame.
    std::vector<int> obs;
    {
      std::vector<int> seen;
      bool conflict = false;
      for (int n : t) {
        int f = frame_of[n];
        if (std::find(seen.begin(), seen.end(), f) != seen.end()) { conflict = true; continue; }
        seen.push_back(f);
        obs.push_back(n);
      }
      if (conflict && obs.size() < 2) { dropped_conflict++; continue; }
    }
    // Triangulation candidate.
    bool tri_ok = false;
    cv::Vec3d Xt;
    std::vector<int> tri_obs = obs;
    if (obs.size() >= 2) {
      for (int round = 0; round < 2 && tri_obs.size() >= 2; ++round) {
        std::vector<cv::Matx34d> Ps;
        std::vector<cv::Point2f> xs;
        for (int n : tri_obs) { int f = frame_of[n]; Ps.push_back(P[f]); xs.push_back(fd[f].kps[n - base[f]].pt); }
        if (!triangulate(Ps, xs, &Xt)) break;
        std::vector<int> good;
        for (int n : tri_obs) {
          int f = frame_of[n];
          double z;
          if (reproj_err(P[f], Xt, fd[f].kps[n - base[f]].pt, &z) < a.reproj_px && z > 0.05) good.push_back(n);
        }
        if (good.size() == tri_obs.size()) { tri_ok = true; break; }
        tri_obs = good;
      }
      if (tri_ok) {
        // Parallax: largest angle between viewing rays.
        double best_cos = 1.0;
        for (size_t x = 0; x < tri_obs.size(); ++x)
          for (size_t y = x + 1; y < tri_obs.size(); ++y) {
            cv::Vec3d r1 = Xt - fd[frame_of[tri_obs[x]]].tcw, r2 = Xt - fd[frame_of[tri_obs[y]]].tcw;
            best_cos = std::min(best_cos, r1.dot(r2) / (cv::norm(r1) * cv::norm(r2)));
          }
        if (best_cos > min_cos) { tri_ok = false; dropped_angle++; }
      } else {
        dropped_reproj++;
      }
    }
    // Depth candidate: median of per-observation back-projections.
    bool dep_ok = false;
    cv::Vec3d Xd;
    if (need_depth) {
      std::vector<double> xs, ys, zs;
      for (int n : obs) {
        auto [ok, Xw] = kp_world(n);
        if (ok) { xs.push_back(Xw[0]); ys.push_back(Xw[1]); zs.push_back(Xw[2]); }
      }
      if (!xs.empty()) {
        auto med = [](std::vector<double> v) { std::nth_element(v.begin(), v.begin() + v.size() / 2, v.end()); return v[v.size() / 2]; };
        Xd = cv::Vec3d(med(xs), med(ys), med(zs));
        dep_ok = true;
        // Consistency: reprojection into every observing frame.
        if (obs.size() >= 2) {
          int bad = 0;
          for (int n : obs) {
            int f = frame_of[n];
            double z, e = reproj_err(P[f], Xd, fd[f].kps[n - base[f]].pt, &z);
            depth_reproj.push_back(std::min(e, 1e3));
            if (e > 2 * a.reproj_px) bad++;
          }
          if (bad * 2 > int(obs.size())) { dep_ok = false; dropped_depth++; }
        }
      }
    }
    if (tri_ok && dep_ok) tri_vs_depth.push_back(cv::norm(Xt - Xd));
    if (a.mode == "tri" && tri_ok) pts.push_back({Xt, tri_obs, 0});
    else if (a.mode == "depth" && dep_ok) pts.push_back({Xd, obs, 1});
    else if (a.mode == "hybrid") {
      if (tri_ok) pts.push_back({Xt, tri_obs, 0});
      else if (dep_ok) pts.push_back({Xd, obs, 1});
    }
  }

  MapData m;
  m.name = a.name;
  m.feature = a.feat.root_sift ? "rootsift" : "sift";
  m.dim = 128;
  m.obs_offset.push_back(0);
  size_t ntri = 0;
  double sum_len = 0;
  for (size_t pi = 0; pi < pts.size(); ++pi) {
    const auto& p = pts[pi];
    ntri += p.source == 0;
    for (int c = 0; c < 3; ++c) m.xyz.push_back(float(p.X[c]));
    // Mean descriptor of the track (Active Search keeps per-point mean descriptors too).
    std::vector<double> acc(128, 0.0);
    for (int n : p.obs) {
      const float* d = fd[frame_of[n]].desc.ptr<float>(n - base[frame_of[n]]);
      for (int c = 0; c < 128; ++c) acc[c] += d[c];
    }
    for (int c = 0; c < 128; ++c) m.desc.push_back(uint8_t(std::clamp(std::lround(acc[c] / p.obs.size()), 0L, 255L)));
    m.desc_point.push_back(uint32_t(pi));
    for (int n : p.obs) m.obs_frame.push_back(uint32_t(frame_of[n]));
    m.obs_offset.push_back(uint32_t(m.obs_frame.size()));
    sum_len += p.obs.size();
  }
  for (int i = 0; i < nf; ++i)
    for (int r = 0; r < 3; ++r) {
      for (int c = 0; c < 3; ++c) m.frame_pose.push_back(float(fd[i].Rcw(r, c)));
      m.frame_pose.push_back(float(fd[i].tcw[r]));
    }
  // frame_pose is row-major 3x4 per frame: [R | t] rows.
  std::string err;
  if (!write_vmap(a.out, m, &err)) { std::fprintf(stderr, "[build] %s\n", err.c_str()); return 1; }

  auto median = [](std::vector<double> v) {
    if (v.empty()) return -1.0;  // JSON has no NaN; -1 means "not measured"
    std::nth_element(v.begin(), v.begin() + v.size() / 2, v.end());
    return v[v.size() / 2];
  };
  double secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - T0).count();
  std::string cmd;
  for (int i = 0; i < argc; ++i) { if (i) cmd += ' '; cmd += argv[i]; }
  std::printf("{\"command\":\"%s\",\"provenance\":\"%s\",\"focal\":%.3f,", cmd.c_str(), a.provenance.c_str(), kColorK(0, 0));
  std::printf("\"map\":\"%s\",\"mode\":\"%s\",\"frames\":%d,\"features\":%d,\"tracks\":%zu,"
              "\"points\":%zu,\"points_tri\":%zu,\"mean_track_len\":%.2f,\"dropped_reproj\":%zu,"
              "\"dropped_angle\":%zu,\"dropped_depth\":%zu,\"tri_vs_depth_median_m\":%.4f,"
              "\"tri_vs_depth_n\":%zu,\"depth_reproj_median_px\":%.3f,\"seconds\":%.1f}\n",
              a.out.c_str(), a.mode.c_str(), nf, base[nf], tracks.size(), pts.size(), ntri,
              pts.empty() ? 0.0 : sum_len / pts.size(), dropped_reproj, dropped_angle, dropped_depth,
              median(tri_vs_depth), tri_vs_depth.size(), median(depth_reproj), secs);
  return 0;
}
