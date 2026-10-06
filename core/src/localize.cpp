#include "localize.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstring>
#include <numeric>
#include <unordered_map>

#include <opencv2/features.hpp>
#include <opencv2/geometry.hpp>
#include <opencv2/imgcodecs.hpp>

namespace vloc {

namespace {

using Clock = std::chrono::steady_clock;
double ms_since(Clock::time_point t0) {
  return std::chrono::duration<double, std::milli>(Clock::now() - t0).count();
}

void set_err(vloc_result* out, const char* msg) {
  std::snprintf(out->err, sizeof(out->err), "%s", msg);
}

struct Match2D3D {
  int q;        // query keypoint
  uint32_t p;   // map point
  float dist;
  float ratio;
};

// 2D-to-3D: every query descriptor searches the kd-forest. The second neighbour for the
// ratio test is the nearest descriptor of a different 3D point.
std::vector<Match2D3D> match_2d3d(const Map& map, const cv::Mat& qdesc, const std::vector<cv::KeyPoint>& kps,
                                  const LocalizeParams& p) {
  std::vector<Match2D3D> out;
  if (qdesc.empty() || !map.index) return out;
  const int k = std::max(2, std::min(p.knn, int(map.data.num_desc())));
  // Prioritised search (in the spirit of Active Search's early termination): visit query
  // features strongest first, in chunks, and stop once `match_budget` matches are found.
  std::vector<int> order(qdesc.rows);
  std::iota(order.begin(), order.end(), 0);
  const bool budgeted = p.match_budget > 0;
  if (budgeted)
    std::stable_sort(order.begin(), order.end(), [&](int a, int b) { return kps[a].response > kps[b].response; });
  const int chunk = budgeted ? 256 : qdesc.rows;
  std::unordered_map<uint32_t, size_t> best;  // point -> position in out
  for (int c0 = 0; c0 < qdesc.rows; c0 += chunk) {
    if (budgeted && int(out.size()) >= p.match_budget) break;
    const int n = std::min(chunk, qdesc.rows - c0);
    cv::Mat q(n, qdesc.cols, CV_32F);
    for (int r = 0; r < n; ++r) qdesc.row(order[c0 + r]).copyTo(q.row(r));
    cv::Mat idx(n, k, CV_32S), dst(n, k, CV_32F);
    // cv::flann::Index::knnSearch is not const-qualified but the kd-tree search keeps all
    // of its state on the stack, so concurrent searches on one index are safe (tested).
    const_cast<cv::flann::Index&>(*map.index).knnSearch(q, idx, dst, k, cv::flann::SearchParams(p.checks));
    for (int r = 0; r < n; ++r) {
      const int i = order[c0 + r];
      const int* ii = idx.ptr<int>(r);
      const float* dd = dst.ptr<float>(r);  // squared L2
      if (ii[0] < 0) continue;
      const uint32_t p1 = map.data.desc_point[ii[0]];
      float d2 = -1.f;
      for (int j = 1; j < k; ++j) {
        if (ii[j] < 0) break;
        if (map.data.desc_point[ii[j]] != p1) { d2 = dd[j]; break; }
      }
      if (d2 <= 0.f) continue;
      const float ratio = std::sqrt(dd[0] / d2);
      if (ratio > p.ratio) continue;
      Match2D3D m{i, p1, std::sqrt(dd[0]), ratio};
      auto it = best.find(p1);
      if (it == best.end()) {
        best.emplace(p1, out.size());
        out.push_back(m);
      } else if (m.dist < out[it->second].dist) {
        out[it->second] = m;
      }
    }
  }
  if (p.max_matches > 0 && int(out.size()) > p.max_matches) {
    std::partial_sort(out.begin(), out.begin() + p.max_matches, out.end(),
                      [](const Match2D3D& a, const Match2D3D& b) { return a.ratio < b.ratio; });
    out.resize(p.max_matches);
  }
  return out;
}

struct PoseEstimate {
  bool ok = false;
  cv::Mat rvec, tvec;
  std::vector<int> inliers;  // indices into the match list
};

PoseEstimate estimate_pose(const std::vector<cv::Point3f>& obj, const std::vector<cv::Point2f>& img,
                           const cv::Matx33d& K, const LocalizeParams& p) {
  PoseEstimate e;
  if (obj.size() < 4) return e;
  cv::Mat rvec, tvec;
  std::vector<int> inl;
  bool ok;
  if (p.pnp == 1) {
    cv::UsacParams up;
    up.sampler = cv::SAMPLING_UNIFORM;
    up.score = cv::SCORE_METHOD_MAGSAC;
    up.loMethod = cv::LOCAL_OPTIM_SIGMA;
    up.maxIterations = p.ransac_iters;
    up.confidence = p.ransac_conf;
    up.threshold = p.ransac_px;
    up.randomGeneratorState = 0;
    ok = cv::solvePnPRansac(obj, img, K, cv::noArray(), rvec, tvec, inl, up);
  } else {
    // Fixed RNG state per call: cv::theRNG() is thread-local, reset it so a frame's result
    // does not depend on which thread ran it or what ran before.
    cv::theRNG().state = 0x9e3779b97f4a7c15ull;
    ok = cv::solvePnPRansac(obj, img, K, cv::noArray(), rvec, tvec, false, p.ransac_iters,
                            float(p.ransac_px), p.ransac_conf, inl, cv::SOLVEPNP_AP3P);
  }
  if (!ok || int(inl.size()) < p.min_inliers) return e;
  if (p.refine) {
    std::vector<cv::Point3f> o;
    std::vector<cv::Point2f> m;
    for (int i : inl) { o.push_back(obj[i]); m.push_back(img[i]); }
    cv::solvePnPRefineLM(o, m, K, cv::noArray(), rvec, tvec);
    // Re-score all correspondences with the refined pose.
    std::vector<cv::Point2f> proj;
    cv::projectPoints(obj, rvec, tvec, K, cv::noArray(), proj);
    cv::Matx33d R;
    cv::Rodrigues(rvec, R);
    cv::Vec3d t(tvec.at<double>(0), tvec.at<double>(1), tvec.at<double>(2));
    inl.clear();
    const double th2 = p.ransac_px * p.ransac_px;
    for (size_t i = 0; i < obj.size(); ++i) {
      cv::Vec3d X = R * cv::Vec3d(obj[i].x, obj[i].y, obj[i].z) + t;
      if (X[2] <= 0) continue;
      cv::Point2f d = proj[i] - img[i];
      if (d.dot(d) < th2) inl.push_back(int(i));
    }
    if (int(inl.size()) < p.min_inliers) return e;
  }
  e.ok = true;
  e.rvec = rvec;
  e.tvec = tvec;
  e.inliers = std::move(inl);
  return e;
}

// Active Search style 3D-to-2D step: take the map frames that see the most inlier
// points, project every point those frames see into the query with the current pose,
// and match each against the query descriptors inside a small window.
void search_3d2d(const Map& map, const std::vector<cv::KeyPoint>& kps, const cv::Mat& qdesc,
                 const cv::Matx33d& K, const PoseEstimate& pose, const std::vector<Match2D3D>& matches,
                 const LocalizeParams& p, std::vector<Match2D3D>* added) {
  const auto& d = map.data;
  const size_t nf = d.num_frames();
  if (nf == 0) return;
  std::vector<int> votes(nf, 0);
  for (int i : pose.inliers) {
    uint32_t pt = matches[i].p;
    for (uint32_t o = d.obs_offset[pt]; o < d.obs_offset[pt + 1]; ++o) votes[d.obs_frame[o]]++;
  }
  std::vector<int> frames(nf);
  std::iota(frames.begin(), frames.end(), 0);
  const int top = std::min<int>(10, int(nf));
  std::partial_sort(frames.begin(), frames.begin() + top, frames.end(),
                    [&](int a, int b) { return votes[a] > votes[b]; });

  // Grid over query keypoints.
  const int cell = 16;
  int W = 0, H = 0;
  for (const auto& kp : kps) { W = std::max(W, int(kp.pt.x) + 1); H = std::max(H, int(kp.pt.y) + 1); }
  const int gw = W / cell + 1, gh = H / cell + 1;
  std::vector<std::vector<int>> grid(size_t(gw) * gh);
  for (int i = 0; i < int(kps.size()); ++i)
    grid[size_t(int(kps[i].pt.y) / cell) * gw + int(kps[i].pt.x) / cell].push_back(i);

  std::vector<char> used_q(kps.size(), 0);
  std::unordered_map<uint32_t, char> done;
  for (const auto& m : matches) { done[m.p] = 1; }
  for (int i : pose.inliers) used_q[matches[i].q] = 1;

  cv::Matx33d R;
  cv::Rodrigues(pose.rvec, R);
  cv::Vec3d t(pose.tvec.at<double>(0), pose.tvec.at<double>(1), pose.tvec.at<double>(2));
  const double r = p.as_radius_px, r2 = r * r;
  const int dim = d.dim;
  for (int fi = 0; fi < top; ++fi) {
    const int f = frames[fi];
    if (votes[f] == 0) break;
    for (uint32_t o = map.frame_offset[f]; o < map.frame_offset[f + 1]; ++o) {
      const uint32_t pt = map.frame_points[o];
      if (!done.emplace(pt, 1).second) continue;
      cv::Vec3d X = R * cv::Vec3d(d.xyz[3 * pt], d.xyz[3 * pt + 1], d.xyz[3 * pt + 2]) + t;
      if (X[2] <= 0.05) continue;
      const double u = K(0, 0) * X[0] / X[2] + K(0, 2), v = K(1, 1) * X[1] / X[2] + K(1, 2);
      if (u < 0 || v < 0 || u >= W || v >= H) continue;
      const float* pd = map.desc_f32.ptr<float>(int(map.point_desc[pt]));
      float b1 = 1e30f, b2 = 1e30f;
      int bi = -1;
      const int cx0 = std::max(0, int((u - r) / cell)), cx1 = std::min(gw - 1, int((u + r) / cell));
      const int cy0 = std::max(0, int((v - r) / cell)), cy1 = std::min(gh - 1, int((v + r) / cell));
      for (int cy = cy0; cy <= cy1; ++cy)
        for (int cx = cx0; cx <= cx1; ++cx)
          for (int qi : grid[size_t(cy) * gw + cx]) {
            const double du = kps[qi].pt.x - u, dv = kps[qi].pt.y - v;
            if (du * du + dv * dv > r2) continue;
            const float* qd = qdesc.ptr<float>(qi);
            float s = 0.f;
            for (int c = 0; c < dim; ++c) { float e = qd[c] - pd[c]; s += e * e; }
            if (s < b1) { b2 = b1; b1 = s; bi = qi; } else if (s < b2) { b2 = s; }
          }
      if (bi < 0 || used_q[bi]) continue;
      // With a single candidate in the window, compare against a loose absolute bound.
      const float ratio = b2 < 1e29f ? std::sqrt(b1 / b2) : 0.f;
      if (ratio > p.as_ratio) continue;
      if (b2 >= 1e29f && std::sqrt(b1) > 250.f) continue;
      used_q[bi] = 1;
      added->push_back({bi, pt, std::sqrt(b1), ratio});
    }
  }
}

}  // namespace

void extract_features(const cv::Mat& gray, const FeatureParams& p, std::vector<cv::KeyPoint>* kps,
                      cv::Mat* desc) {
  auto sift = cv::SIFT::create(p.max_features, 3, p.contrast_threshold);
  cv::Mat d;
  sift->detectAndCompute(gray, cv::noArray(), *kps, d);
  if (d.type() != CV_32F) d.convertTo(d, CV_32F);
  if (p.root_sift && !d.empty()) {
    for (int i = 0; i < d.rows; ++i) {
      float* r = d.ptr<float>(i);
      double s = 0;
      for (int c = 0; c < d.cols; ++c) s += r[c];
      if (s <= 0) continue;
      for (int c = 0; c < d.cols; ++c) r[c] = std::min(255.f, float(std::sqrt(r[c] / s) * 512.0));
    }
  }
  // Quantise like the map storage so query and map descriptors live on the same grid.
  for (int i = 0; i < d.rows; ++i) {
    float* r = d.ptr<float>(i);
    for (int c = 0; c < d.cols; ++c) r[c] = std::round(std::clamp(r[c], 0.f, 255.f));
  }
  *desc = d;
}

bool solve_pose(const std::vector<cv::Point3f>& obj, const std::vector<cv::Point2f>& img,
                const cv::Matx33d& K, const LocalizeParams& p, cv::Mat* rvec, cv::Mat* tvec,
                std::vector<int>* inliers) {
  PoseEstimate e = estimate_pose(obj, img, K, p);
  if (!e.ok) return false;
  *rvec = e.rvec; *tvec = e.tvec; *inliers = e.inliers;
  return true;
}

void fill_pose(const cv::Mat& rvec, const cv::Mat& tvec, vloc_result* out) {
  cv::Matx33d R;
  cv::Rodrigues(rvec, R);
  cv::Matx33d Rc = R.t();  // camera-to-world rotation
  cv::Vec3d t(tvec.at<double>(0), tvec.at<double>(1), tvec.at<double>(2));
  cv::Vec3d C = -(Rc * t);
  // Rotation matrix to quaternion (Shepperd).
  double tr = Rc(0, 0) + Rc(1, 1) + Rc(2, 2), qw, qx, qy, qz;
  if (tr > 0) {
    double s = std::sqrt(tr + 1.0) * 2;
    qw = 0.25 * s; qx = (Rc(2, 1) - Rc(1, 2)) / s; qy = (Rc(0, 2) - Rc(2, 0)) / s; qz = (Rc(1, 0) - Rc(0, 1)) / s;
  } else if (Rc(0, 0) > Rc(1, 1) && Rc(0, 0) > Rc(2, 2)) {
    double s = std::sqrt(1.0 + Rc(0, 0) - Rc(1, 1) - Rc(2, 2)) * 2;
    qw = (Rc(2, 1) - Rc(1, 2)) / s; qx = 0.25 * s; qy = (Rc(0, 1) + Rc(1, 0)) / s; qz = (Rc(0, 2) + Rc(2, 0)) / s;
  } else if (Rc(1, 1) > Rc(2, 2)) {
    double s = std::sqrt(1.0 + Rc(1, 1) - Rc(0, 0) - Rc(2, 2)) * 2;
    qw = (Rc(0, 2) - Rc(2, 0)) / s; qx = (Rc(0, 1) + Rc(1, 0)) / s; qy = 0.25 * s; qz = (Rc(1, 2) + Rc(2, 1)) / s;
  } else {
    double s = std::sqrt(1.0 + Rc(2, 2) - Rc(0, 0) - Rc(1, 1)) * 2;
    qw = (Rc(1, 0) - Rc(0, 1)) / s; qx = (Rc(0, 2) + Rc(2, 0)) / s; qy = (Rc(1, 2) + Rc(2, 1)) / s; qz = 0.25 * s;
  }
  double n = std::sqrt(qw * qw + qx * qx + qy * qy + qz * qz);
  if (qw < 0) n = -n;
  out->qw = qw / n; out->qx = qx / n; out->qy = qy / n; out->qz = qz / n;
  out->tx = C[0]; out->ty = C[1]; out->tz = C[2];
}

void localize_gray(const Map& map, const cv::Mat& gray, const vloc_intrinsics& Kin,
                   const LocalizeParams& p, vloc_result* out) {
  auto t0 = Clock::now();
  std::vector<cv::KeyPoint> kps;
  cv::Mat qdesc;
  FeatureParams fp = p.feat;
  fp.root_sift = map.data.feature == "rootsift";  // query must match how the map was built
  extract_features(gray, fp, &kps, &qdesc);
  out->num_keypoints = int(kps.size());
  out->ms_extract = ms_since(t0);

  t0 = Clock::now();
  std::vector<Match2D3D> matches = match_2d3d(map, qdesc, kps, p);
  out->num_matches = int(matches.size());
  out->ms_match = ms_since(t0);

  t0 = Clock::now();
  const cv::Matx33d K(Kin.fx, 0, Kin.cx, 0, Kin.fy, Kin.cy, 0, 0, 1);
  auto build = [&](const std::vector<Match2D3D>& ms, std::vector<cv::Point3f>* obj,
                   std::vector<cv::Point2f>* img) {
    obj->clear(); img->clear();
    for (const auto& m : ms) {
      const float* x = &map.data.xyz[3 * size_t(m.p)];
      obj->emplace_back(x[0], x[1], x[2]);
      img->push_back(kps[m.q].pt);
    }
  };
  std::vector<cv::Point3f> obj;
  std::vector<cv::Point2f> img;
  build(matches, &obj, &img);
  PoseEstimate e = estimate_pose(obj, img, K, p);
  if (e.ok && p.active_search) {
    std::vector<Match2D3D> added;
    search_3d2d(map, kps, qdesc, K, e, matches, p, &added);
    if (!added.empty()) {
      // Keep the 2D-3D inliers plus the new 3D-2D matches and re-estimate.
      std::vector<Match2D3D> ms;
      for (int i : e.inliers) ms.push_back(matches[i]);
      ms.insert(ms.end(), added.begin(), added.end());
      build(ms, &obj, &img);
      PoseEstimate e2 = estimate_pose(obj, img, K, p);
      if (e2.ok && e2.inliers.size() >= e.inliers.size()) e = std::move(e2);
      out->num_matches = int(matches.size() + added.size());
    }
  }
  out->ms_pose = ms_since(t0);
  if (!e.ok) {
    out->ok = 0;
    out->num_inliers = 0;
    set_err(out, matches.size() < 4 ? "too few matches" : "no pose with enough inliers");
    return;
  }
  out->ok = 1;
  out->num_inliers = int(e.inliers.size());
  fill_pose(e.rvec, e.tvec, out);
}

void localize(const Map& map, const uint8_t* image, size_t len, const vloc_intrinsics& K,
              const LocalizeParams& p, vloc_result* out) {
  std::memset(out, 0, sizeof(*out));
  auto t0 = Clock::now();
  cv::Mat gray;
  if (image && len > 0) {
    cv::Mat buf(1, int(len), CV_8U, const_cast<uint8_t*>(image));
    gray = cv::imdecode(buf, cv::IMREAD_GRAYSCALE);
  }
  out->ms_decode = ms_since(t0);
  if (gray.empty()) {
    set_err(out, "image decode failed");
    return;
  }
  localize_gray(map, gray, K, p, out);
}

}  // namespace vloc
