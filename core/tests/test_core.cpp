#include <gtest/gtest.h>

#include <cmath>
#include <filesystem>
#include <random>
#include <thread>
#include <vector>

#include <opencv2/geometry.hpp>
#include <opencv2/imgcodecs.hpp>
#include <opencv2/imgproc.hpp>

#include "localize.h"
#include "vloc.h"
#include "vmap.h"

using namespace vloc;

namespace {
const cv::Matx33d K(525, 0, 320, 0, 525, 240, 0, 0, 1);

std::string tmp_path(const std::string& name) {
  return (std::filesystem::temp_directory_path() / name).string();
}

// A random textured image: blurred noise plus random filled shapes, so SIFT finds plenty.
cv::Mat synthetic_image(unsigned seed) {
  cv::RNG rng(seed);
  cv::Mat img(480, 640, CV_8U);
  rng.fill(img, cv::RNG::UNIFORM, 0, 255);
  cv::GaussianBlur(img, img, cv::Size(0, 0), 2.0);
  for (int i = 0; i < 400; ++i) {
    cv::Point c(rng.uniform(0, 640), rng.uniform(0, 480));
    int r = rng.uniform(3, 25);
    if (i % 2) cv::circle(img, c, r, cv::Scalar(rng.uniform(0, 255)), -1, cv::LINE_AA);
    else cv::rectangle(img, c, c + cv::Point(r, r * 2 / 3), cv::Scalar(rng.uniform(0, 255)), -1);
  }
  return img;
}
}  // namespace

TEST(Pnp, SyntheticSceneRecoversKnownPose) {
  std::mt19937 g(7);
  std::uniform_real_distribution<double> U(-1.0, 1.0);
  cv::Vec3d rv(0.1, -0.2, 0.05), tv(0.3, -0.1, 2.5);
  cv::Matx33d R;
  cv::Rodrigues(rv, R);
  std::vector<cv::Point3f> obj;
  std::vector<cv::Point2f> img;
  for (int i = 0; i < 300; ++i) {
    cv::Vec3d X(U(g), U(g), U(g) * 0.5);
    cv::Vec3d x = K * (R * X + tv);
    cv::Point2f px(float(x[0] / x[2]), float(x[1] / x[2]));
    if (i % 3 == 0) px = cv::Point2f(float(320 + 300 * U(g)), float(240 + 220 * U(g)));  // 33% outliers
    else px += cv::Point2f(float(0.5 * U(g)), float(0.5 * U(g)));                       // pixel noise
    obj.emplace_back(cv::Point3f(float(X[0]), float(X[1]), float(X[2])));
    img.push_back(px);
  }
  LocalizeParams p;
  p.ransac_px = 4;
  cv::Mat rvec, tvec;
  std::vector<int> inl;
  ASSERT_TRUE(solve_pose(obj, img, K, p, &rvec, &tvec, &inl));
  EXPECT_GE(int(inl.size()), 190);
  cv::Matx33d Re;
  cv::Rodrigues(rvec, Re);
  cv::Matx33d D = Re.t() * R;
  double ang = std::acos(std::clamp((D(0, 0) + D(1, 1) + D(2, 2) - 1) / 2, -1.0, 1.0)) * 180 / CV_PI;
  double dt = cv::norm(cv::Vec3d(tvec.at<double>(0), tvec.at<double>(1), tvec.at<double>(2)) - tv);
  EXPECT_LT(ang, 0.2);
  EXPECT_LT(dt, 0.01);

  // Camera-to-world conversion: centre must be -R^T t.
  vloc_result r{};
  fill_pose(rvec, tvec, &r);
  cv::Vec3d C = -(R.t() * tv);
  EXPECT_NEAR(r.tx, C[0], 0.01);
  EXPECT_NEAR(r.ty, C[1], 0.01);
  EXPECT_NEAR(r.tz, C[2], 0.01);
}

TEST(Vmap, RoundTrip) {
  MapData m;
  m.name = "roundtrip";
  m.feature = "rootsift";
  m.dim = 128;
  std::mt19937 g(1);
  const int np = 50, nf = 3;
  for (int i = 0; i < np * 3; ++i) m.xyz.push_back(float(g() % 1000) / 7.f);
  for (int i = 0; i < np * 2 * 128; ++i) m.desc.push_back(uint8_t(g()));
  for (int i = 0; i < np * 2; ++i) m.desc_point.push_back(uint32_t(i / 2));
  m.obs_offset.push_back(0);
  for (int i = 0; i < np; ++i) {
    for (int k = 0; k <= i % 3; ++k) m.obs_frame.push_back(uint32_t((i + k) % nf));
    m.obs_offset.push_back(uint32_t(m.obs_frame.size()));
  }
  for (int i = 0; i < nf * 12; ++i) m.frame_pose.push_back(float(i) * 0.5f);
  std::string path = tmp_path("vloc_roundtrip.vmap"), err;
  ASSERT_TRUE(write_vmap(path, m, &err)) << err;
  MapData r;
  ASSERT_TRUE(read_vmap(path, &r, &err)) << err;
  EXPECT_EQ(r.name, m.name);
  EXPECT_EQ(r.feature, m.feature);
  EXPECT_EQ(r.dim, m.dim);
  EXPECT_EQ(r.xyz, m.xyz);
  EXPECT_EQ(r.desc, m.desc);
  EXPECT_EQ(r.desc_point, m.desc_point);
  EXPECT_EQ(r.obs_offset, m.obs_offset);
  EXPECT_EQ(r.obs_frame, m.obs_frame);
  EXPECT_EQ(r.frame_pose, m.frame_pose);

  // A truncated file must be rejected, not crash.
  std::filesystem::resize_file(path, 300);
  EXPECT_FALSE(read_vmap(path, &r, &err));
  std::filesystem::remove(path);
}

// Builds a map from a synthetic image on a plane at z = 2 seen by a camera at the origin,
// then localizes a JPEG of the same image concurrently from 8 threads through the C ABI.
TEST(Localize, ConcurrentCallsAreIdenticalAndCorrect) {
  cv::Mat img = synthetic_image(42);
  FeatureParams fp;
  std::vector<cv::KeyPoint> kps;
  cv::Mat desc;
  extract_features(img, fp, &kps, &desc);
  ASSERT_GT(kps.size(), 300u);
  MapData m;
  m.name = "synthetic";
  m.feature = "rootsift";
  m.obs_offset.push_back(0);
  for (size_t i = 0; i < kps.size(); ++i) {
    // Depth varies over the image so the scene is not planar.
    double z = 2.0 + 0.5 * std::sin(kps[i].pt.x / 60.0) * std::cos(kps[i].pt.y / 50.0);
    m.xyz.push_back(float((kps[i].pt.x - 320) * z / 525));
    m.xyz.push_back(float((kps[i].pt.y - 240) * z / 525));
    m.xyz.push_back(float(z));
    for (int c = 0; c < 128; ++c) m.desc.push_back(uint8_t(desc.at<float>(int(i), c)));
    m.desc_point.push_back(uint32_t(i));
    m.obs_frame.push_back(0);
    m.obs_offset.push_back(uint32_t(m.obs_frame.size()));
  }
  for (int r = 0; r < 3; ++r)
    for (int c = 0; c < 4; ++c) m.frame_pose.push_back(r == c ? 1.f : 0.f);
  std::string path = tmp_path("vloc_synth.vmap"), err;
  ASSERT_TRUE(write_vmap(path, m, &err)) << err;

  vloc_map* map = nullptr;
  char e[256];
  ASSERT_EQ(vloc_map_load(path.c_str(), &map, e, sizeof(e)), 0) << e;
  vloc_map_info info;
  ASSERT_EQ(vloc_map_get_info(map, &info), 0);
  EXPECT_EQ(info.num_points, int(kps.size()));
  EXPECT_STREQ(info.feature, "rootsift");

  std::vector<uint8_t> jpg;
  cv::imencode(".jpg", img, jpg, {cv::IMWRITE_JPEG_QUALITY, 95});
  const vloc_intrinsics Kc{525, 525, 320, 240};
  vloc_result ref;
  ASSERT_EQ(vloc_localize(map, jpg.data(), jpg.size(), &Kc, &ref), 0);
  ASSERT_EQ(ref.ok, 1) << ref.err;
  EXPECT_NEAR(ref.tx, 0, 0.01);
  EXPECT_NEAR(ref.ty, 0, 0.01);
  EXPECT_NEAR(ref.tz, 0, 0.01);
  EXPECT_NEAR(std::abs(ref.qw), 1.0, 1e-4);

  const int T = 8, reps = 5;
  std::vector<vloc_result> out(T * reps);
  std::vector<std::thread> ts;
  for (int t = 0; t < T; ++t)
    ts.emplace_back([&, t]() {
      for (int k = 0; k < reps; ++k) vloc_localize(map, jpg.data(), jpg.size(), &Kc, &out[t * reps + k]);
    });
  for (auto& t : ts) t.join();
  for (const auto& r : out) {
    ASSERT_EQ(r.ok, 1);
    EXPECT_EQ(r.num_keypoints, ref.num_keypoints);
    EXPECT_EQ(r.num_matches, ref.num_matches);
    EXPECT_EQ(r.num_inliers, ref.num_inliers);
    EXPECT_EQ(r.qw, ref.qw);
    EXPECT_EQ(r.qx, ref.qx);
    EXPECT_EQ(r.tx, ref.tx);
    EXPECT_EQ(r.ty, ref.ty);
    EXPECT_EQ(r.tz, ref.tz);
  }

  // Garbage bytes fail cleanly.
  uint8_t junk[16] = {1, 2, 3};
  vloc_result bad;
  EXPECT_EQ(vloc_localize(map, junk, sizeof(junk), &Kc, &bad), 0);
  EXPECT_EQ(bad.ok, 0);
  EXPECT_STRNE(bad.err, "");
  vloc_map_free(map);
  std::filesystem::remove(path);
}
