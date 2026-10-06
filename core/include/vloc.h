// vloc.h: C ABI of the localization core. The Go service links this through cgo.
// This header is the contract between core/ and service/. Change it only with both sides.
#ifndef VLOC_H
#define VLOC_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct vloc_map vloc_map;

typedef struct {
  double fx, fy, cx, cy;
} vloc_intrinsics;

// Pose is camera-to-world (the camera's position and orientation in map coordinates),
// the same convention as 7-Scenes frame-*.pose.txt.
typedef struct {
  int ok;                 // 1 if a pose was found, 0 otherwise
  double qw, qx, qy, qz;  // rotation camera-to-world, unit quaternion
  double tx, ty, tz;      // camera center in world coordinates, meters
  int num_keypoints;      // features extracted from the query
  int num_matches;        // 2D-3D correspondences fed to RANSAC
  int num_inliers;        // RANSAC inliers of the final pose
  double ms_decode, ms_extract, ms_match, ms_pose;  // per-stage wall time
  char err[256];          // reason when ok == 0
} vloc_result;

typedef struct {
  int num_points;         // 3D points in the map
  int descriptor_dim;     // e.g. 128 for SIFT
  char feature[32];       // feature type name, e.g. "sift"
  char name[128];         // map name (scene)
} vloc_map_info;

// Load a map file built by the map builder. Returns 0 on success.
int vloc_map_load(const char* path, vloc_map** out, char* err, size_t errlen);
void vloc_map_free(vloc_map* map);
int vloc_map_get_info(const vloc_map* map, vloc_map_info* out);

// Localize one encoded image (JPEG or PNG bytes). Thread-safe: many goroutines may call
// this concurrently on the same map. Returns 0 if the call ran (check out->ok for success).
int vloc_localize(const vloc_map* map, const uint8_t* image, size_t image_len,
                  const vloc_intrinsics* K, vloc_result* out);

#ifdef __cplusplus
}
#endif

#endif  // VLOC_H
