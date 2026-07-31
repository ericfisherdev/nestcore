// Package app implements the media bounded context's shared use cases:
// PhotoService (upload/delete/list/serve, generic over a caller-chosen
// PhotoClass) and ReaperService (reclaiming orphaned object-store bytes,
// generic over a caller-supplied set of PhotoClass sources). Both depend
// only on the ports declared in media/domain; implementations live in
// media/adapter.
package app
