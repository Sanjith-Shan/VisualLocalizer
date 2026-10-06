package api

import (
	"encoding/json"
	"net/http"
)

// Error codes returned in the JSON error body. Stable: clients may switch on them.
const (
	CodeBadName           = "BAD_MAP_NAME"
	CodeMapNotFound       = "MAP_NOT_FOUND"
	CodeEmptyBody         = "EMPTY_BODY"
	CodeImageTooLarge     = "IMAGE_TOO_LARGE"
	CodeUnsupportedMedia  = "UNSUPPORTED_MEDIA_TYPE"
	CodeBadImage          = "BAD_IMAGE"
	CodeBadImageSize      = "BAD_IMAGE_SIZE"
	CodeMissingIntrinsics = "MISSING_INTRINSICS"
	CodeBadIntrinsics     = "BAD_INTRINSICS"
	CodeBadDeadline       = "BAD_DEADLINE"
	CodeOverloaded        = "OVERLOADED"
	CodeDeadlineExceeded  = "DEADLINE_EXCEEDED"
	CodeShuttingDown      = "SHUTTING_DOWN"
	CodeCoreError         = "CORE_ERROR"
	CodeMapTooLarge       = "MAP_TOO_LARGE"
	CodeBadMapHeader      = "BAD_MAP_HEADER"
	CodeMapLoadFailed     = "MAP_LOAD_FAILED"
	CodeInternal          = "INTERNAL"
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	var b errorBody
	b.Error.Code, b.Error.Message = code, msg
	b.RequestID = RequestID(r.Context())
	if sw, ok := w.(*statusWriter); ok {
		sw.code = code
	}
	writeJSON(w, status, b)
}
