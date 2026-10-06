package vloc

// checkCoreHeader validates the core's map file header. The core owns the format; until
// it documents a magic and version this accepts any non-empty file and leaves full
// validation to vloc_map_load, which reports a reason on failure.
func checkCoreHeader(head []byte) error {
	if len(head) == 0 {
		return errEmpty
	}
	return nil
}
