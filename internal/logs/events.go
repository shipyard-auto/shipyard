package logs

import "log/slog"

// Source names. These end up in the JSONL "source" field and pick the
// subdirectory under the logs root.
const (
	SourceCron    = "cron"
	SourceService = "service"
	SourceFairway = "fairway"
	SourceCrew    = "crew"
	SourceComms   = "comms"
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
	EventCronJobCreateFailed = "cron_job_create_failed"
	EventCronJobUpdateFailed = "cron_job_update_failed"
	EventCronJobDeleteFailed = "cron_job_delete_failed"

	// Service lifecycle.
	EventServiceCreated       = "service_created"
	EventServiceUpdated       = "service_updated"
	EventServiceEnabled       = "service_enabled"
	EventServiceDisabled      = "service_disabled"
	EventServiceDeleted       = "service_deleted"
	EventServiceStarted       = "service_started"
	EventServiceStopped       = "service_stopped"
	EventServiceRestarted     = "service_restarted"
	EventServiceCreateFailed  = "service_create_failed"
	EventServiceUpdateFailed  = "service_update_failed"
	EventServiceDeleteFailed  = "service_delete_failed"
	EventServiceStartFailed   = "service_start_failed"
	EventServiceStopFailed    = "service_stop_failed"
	EventServiceRestartFailed = "service_restart_failed"

	// HTTP / fairway.
	EventHTTPRequest   = "http_request"
	EventAsyncDispatch = "async_dispatch_finished"

	// Crew runs.
	EventRunStart      = "run_start"
	EventRunEnd        = "run_end"
	EventToolCallStart = "tool_call_start"
	EventToolCallEnd   = "tool_call_end"
	EventRunError      = "run_error"

	// WhatsApp linked-device daemon (shipyard-comms-whatsapp). Message
	// bodies are never logged — only chat ids and counts.
	EventWADaemonStarted    = "wa_daemon_started"
	EventWADaemonStopped    = "wa_daemon_stopped"
	EventWAPaired           = "wa_paired"
	EventWALogout           = "wa_logout"
	EventWAConnected        = "wa_connected"
	EventWADisconnected     = "wa_disconnected"
	EventWALoggedOut        = "wa_logged_out"
	EventWATemporaryBan     = "wa_temporary_ban"
	EventWAStreamReplaced   = "wa_stream_replaced"
	EventWACatalogRefreshed = "wa_catalog_refreshed"
	EventWAHistorySync      = "wa_history_sync"
	EventWAPruned           = "wa_pruned"
	EventWAArchiveError     = "wa_archive_error"
	EventWAStatusError      = "wa_status_error"
	EventWAMessageSent      = "wa_message_sent"
	EventWASendDenied       = "wa_send_denied"
	EventWASendFailed       = "wa_send_failed"
	EventWARPCError         = "wa_rpc_error"
	EventWADeliveryFailed   = "wa_delivery_failed"
	EventWAClientLog        = "wa_client_log"
	EventWAAPIQuery         = "wa_api_query"
	EventWAAPIDenied        = "wa_api_denied"
	EventWAAPIError         = "wa_api_error"
	EventWAMediaStored      = "wa_media_stored"
	EventWAMediaFailed      = "wa_media_failed"

	// Fairway events and webhooks.
	EventEventPublished = "event_published"
	EventWebhookFailed  = "webhook_failed"
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

	KeyHTTPMethod     = "http_method"
	KeyHTTPPath       = "http_path"
	KeyHTTPStatus     = "http_status"
	KeyHTTPRemoteAddr = "http_remote_addr"
	KeyHTTPResponseSz = "http_response_bytes"

	KeyRouteAction   = "route_action"
	KeyRouteTarget   = "route_target"
	KeyRouteExitCode = "route_exit_code"

	// KeyUpstreamHTTPStatus carries the real HTTP status code returned by
	// the upstream when an async http.forward route completes. The
	// http_status field on async_dispatch_finished is hardcoded to 202 (the
	// ack the client received); this attribute records what the upstream
	// actually answered so operators can see whether the fire-and-forget
	// notification succeeded. Only emitted on async http.forward routes.
	KeyUpstreamHTTPStatus = "upstream_http_status"

	KeyAuthType   = "auth_type"
	KeyAuthResult = "auth_result"

	KeyToolName     = "tool_name"
	KeyToolProtocol = "tool_protocol"
	KeyToolOK       = "tool_ok"

	KeyTokensInput  = "tokens_input"
	KeyTokensOutput = "tokens_output"

	KeyChatJID   = "chat_jid"
	KeyCount     = "count"
	KeySizeBytes = "size_bytes"
	// KeyMediaKind is what a WhatsApp send carried: text, image, video,
	// audio or file.
	KeyMediaKind = "media_kind"
	// KeyClientID names the authenticated API client of a signed fairway
	// route.
	KeyClientID = "client_id"

	// Fairway events.
	KeyEventTopic  = "event_topic"
	KeyEventID     = "event_id"
	KeySubscribers = "subscribers"
	KeyAttempts    = "attempts"

	// Subprocess output capture. Emitted on completion events (cron_job_run_*,
	// run_end, future runners) so operators can see the tail of stdout without
	// re-running the job. Paired with KeyOutputTruncated when callers want to
	// expose whether truncation happened. Use logs.Tail(...) with
	// DefaultOutputTailBytes to populate this consistently across sources.
	KeyOutputTail      = "output_tail"
	KeyOutputTruncated = "output_truncated"

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
