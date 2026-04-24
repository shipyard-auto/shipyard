package logs

import "log/slog"

// Source names. These end up in the JSONL "source" field and pick the
// subdirectory under the logs root.
const (
	SourceCron    = "cron"
	SourceService = "service"
	SourceFairway = "fairway"
	SourceCrew    = "crew"
)

// Entity types. Stored in the "entity_type" field.
const (
	EntityCronJob = "cron_job"
	EntityService = "service"
	EntityAgent   = "agent"
)

// Event names. Stored in the "event" field. Use these constants instead of
// string literals at call sites so typos surface at compile time.
const (
	// Cron lifecycle.
	EventCronJobCreated      = "cron_job_created"
	EventCronJobUpdated      = "cron_job_updated"
	EventCronJobEnabled      = "cron_job_enabled"
	EventCronJobDisabled     = "cron_job_disabled"
	EventCronJobDeleted      = "cron_job_deleted"
	EventCronJobRunStarted   = "cron_job_run_started"
	EventCronJobRunFinished  = "cron_job_run_finished"
	EventCronJobRunFailed    = "cron_job_run_failed"

	// Service lifecycle.
	EventServiceCreated   = "service_created"
	EventServiceUpdated   = "service_updated"
	EventServiceEnabled   = "service_enabled"
	EventServiceDisabled  = "service_disabled"
	EventServiceDeleted   = "service_deleted"
	EventServiceStarted   = "service_started"
	EventServiceStopped   = "service_stopped"
	EventServiceRestarted = "service_restarted"

	// HTTP / fairway.
	EventHTTPRequest    = "http_request"
	EventAsyncDispatch  = "async_dispatch_finished"

	// Crew runs.
	EventRunStart     = "run_start"
	EventRunEnd       = "run_end"
	EventToolCallStart = "tool_call_start"
	EventToolCallEnd   = "tool_call_end"
	EventRunError      = "run_error"
)

// Attribute keys. Centralizing them as constants prevents key drift across
// call sites and keeps Loki/Grafana label discovery deterministic.
const (
	KeyTraceID    = "trace_id"
	KeyRunID      = "run_id"
	KeyEntityType = "entity_type"
	KeyEntityID   = "entity_id"
	KeyEntityName = "entity_name"
	KeyDurationMs = "duration_ms"
	KeyError      = "error"
	KeyErrorKind  = "error_kind"

	KeyHTTPMethod      = "http_method"
	KeyHTTPPath        = "http_path"
	KeyHTTPStatus      = "http_status"
	KeyHTTPRemoteAddr  = "http_remote_addr"
	KeyHTTPResponseSz  = "http_response_bytes"

	KeyRouteAction   = "route_action"
	KeyRouteTarget   = "route_target"
	KeyRouteExitCode = "route_exit_code"

	KeyAuthType   = "auth_type"
	KeyAuthResult = "auth_result"

	KeyToolName     = "tool_name"
	KeyToolProtocol = "tool_protocol"
	KeyToolOK       = "tool_ok"

	KeyTokensInput  = "tokens_input"
	KeyTokensOutput = "tokens_output"

	KeyHostname       = "hostname"
	KeyPID            = "pid"
	KeyServiceVersion = "service_version"
	KeySource         = "source"

	// Reserved keys produced by slog itself; aliased here so call sites do
	// not depend on stdlib internals.
	KeyTimestamp = "ts"
	KeyEvent     = "event"
	KeyMessage   = "message"
	KeyLevel     = "level"
)

// EntityAttrs returns the attribute slice describing a managed entity. Pass
// the result via slog.Logger.LogAttrs after spreading with `...`.
func EntityAttrs(entityType, id, name string) []slog.Attr {
	attrs := make([]slog.Attr, 0, 3)
	if entityType != "" {
		attrs = append(attrs, slog.String(KeyEntityType, entityType))
	}
	if id != "" {
		attrs = append(attrs, slog.String(KeyEntityID, id))
	}
	if name != "" {
		attrs = append(attrs, slog.String(KeyEntityName, name))
	}
	return attrs
}

// HTTPAttrs returns the standard set of attrs for an HTTP request log line.
func HTTPAttrs(method, path string, status, responseBytes int, remoteAddr string, durationMs int64) []slog.Attr {
	return []slog.Attr{
		slog.String(KeyHTTPMethod, method),
		slog.String(KeyHTTPPath, path),
		slog.Int(KeyHTTPStatus, status),
		slog.Int(KeyHTTPResponseSz, responseBytes),
		slog.String(KeyHTTPRemoteAddr, remoteAddr),
		slog.Int64(KeyDurationMs, durationMs),
	}
}

// ToolAttrs returns the standard attrs for a crew tool call.
func ToolAttrs(name, protocol string, ok bool) []slog.Attr {
	return []slog.Attr{
		slog.String(KeyToolName, name),
		slog.String(KeyToolProtocol, protocol),
		slog.Bool(KeyToolOK, ok),
	}
}
