package fetch

// PausedAction is the Fetch.requestPaused allow/deny decision.
type PausedAction string

const (
	PausedContinue PausedAction = "continue"
	PausedFail     PausedAction = "fail"
)

// DecideFetchPaused decides continue vs fail for a CDP Fetch.requestPaused event.
//
// Rules (host cdp-fetch.ts):
//   - non-Document resource → continue
//   - Document in a non-main frame (iframe) → continue
//   - main-frame Document → AssertFetchURLAllowed-style sync parse/host/IP guard
//
// DNS is not performed here (unit-testable without network). The CDP capturer
// additionally runs AssertFetchURLAllowed with DNS before continueRequest.
func DecideFetchPaused(resourceType, frameID, mainFrameID, requestURL string) PausedAction {
	if resourceType != "" && resourceType != "Document" {
		return PausedContinue
	}
	if frameID != "" && mainFrameID != "" && frameID != mainFrameID {
		return PausedContinue
	}
	if _, reason, _ := ParseFetchURL(requestURL); reason != "" {
		return PausedFail
	}
	return PausedContinue
}

// IsMainFrameDocument reports whether a paused request is a main-frame Document.
func IsMainFrameDocument(resourceType, frameID, mainFrameID string) bool {
	if resourceType != "" && resourceType != "Document" {
		return false
	}
	if frameID != "" && mainFrameID != "" && frameID != mainFrameID {
		return false
	}
	return true
}
