// localize.h: internal C++ API of the localizer (the C ABI in vloc.h wraps this).
#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include <opencv2/core.hpp>

#include "vloc.h"
#include "vmap.h"

namespace vloc {

struct FeatureParams {
  int max_features = 4000;        // SIFT nfeatures (0 = unlimited)
  double contrast_threshold = 0.01;  // OpenCV default 0.04 gave ~460 kp/frame on 7-Scenes
  bool root_sift = true;          // Arandjelovic and Zisserman RootSIFT
};

// SIFT keypoints and descriptors. Descriptors are CV_32F in SIFT scale (0..255) so they
// can be stored as uint8 losslessly enough and searched with L2.
void extract_features(const cv::Mat& gray, const FeatureParams& p, std::vector<cv::KeyPoint>* kps,
                      cv::Mat* desc);

struct LocalizeParams {
  FeatureParams feat;
  double ratio = 0.9;             // Lowe ratio test, first vs second distinct 3D point
  int knn = 3;                    // neighbours fetched per query descriptor
  int checks = 64;                // kd-forest leaves visited per query
  int match_budget = 0;           // 0 = match every feature; else stop after this many
  int max_matches = 0;            // 0 = all; else keep the best by ratio (prioritised)
  double ransac_px = 12.0;        // inlier threshold, pixels
  int ransac_iters = 5000;
  double ransac_conf = 0.9999;
  int min_inliers = 12;
  bool refine = true;             // Levenberg-Marquardt on inliers
  bool active_search = true;      // 3D-to-2D search around the first pose
  double as_radius_px = 6.0;      // reprojection window for 3D-to-2D search
  double as_ratio = 0.8;          // ratio for 3D-to-2D search
  int pnp = 1;                    // 0 = OpenCV RANSAC + AP3P, 1 = OpenCV USAC MAGSAC
};

// RANSAC PnP + refinement over given correspondences. rvec/tvec are world-to-camera.
bool solve_pose(const std::vector<cv::Point3f>& obj, const std::vector<cv::Point2f>& img,
                const cv::Matx33d& K, const LocalizeParams& p, cv::Mat* rvec, cv::Mat* tvec,
                std::vector<int>* inliers);

// Pose world-to-camera (OpenCV convention) to the C result (camera-to-world).
void fill_pose(const cv::Mat& rvec, const cv::Mat& tvec, vloc_result* out);

// Core entry. Thread-safe on a const Map.
void localize(const Map& map, const uint8_t* image, size_t len, const vloc_intrinsics& K,
              const LocalizeParams& p, vloc_result* out);

// Same, from an already-decoded grayscale image (decode time reported as 0).
void localize_gray(const Map& map, const cv::Mat& gray, const vloc_intrinsics& K,
                   const LocalizeParams& p, vloc_result* out);

// The C++ map behind a C handle (for the eval tool and tests).
const Map& internal_map(const vloc_map* m);

}  // namespace vloc
