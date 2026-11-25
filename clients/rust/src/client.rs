//! Generated transport-agnostic service clients from CSIL specification

use super::codec::*;
use super::types::*;

/// Error from a generated client call: a structured error the service returned,
/// or a transport-level failure. The caller-supplied `Transport` decides how an
/// error response maps onto `Service`. `Encode` means the request has no valid
/// wire form, so the client did not call the transport.
#[derive(Debug, Clone)]
pub enum ClientError {
    Service { code: i64, message: String },
    Transport(String),
    Encode(String),
    ApplicationServiceError(Box<ServiceError>),
    Decode(String),
}

impl std::fmt::Display for ClientError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ClientError::Service { code, message } => write!(f, "service error {code}: {message}"),
            Self::Encode(msg) => write!(f, "invalid request: {msg}"),
            Self::ApplicationServiceError(e) => write!(f, "application error: {e:?}"),
            Self::Decode(msg) => write!(f, "invalid reply: {msg}"),
            ClientError::Transport(msg) => write!(f, "transport error: {msg}"),
        }
    }
}

impl std::error::Error for ClientError {}

/// A status-zero CSIL-RPC reply: `variant` names the declared output arm that
/// `payload` encodes. A transport returns it from `call_reply`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WireReply {
    pub variant: Option<String>,
    pub payload: Vec<u8>,
}

/// The caller-supplied byte carrier: it performs the call named by `(service, op)`
/// with the already-encoded request bytes and returns the response bytes, or an
/// error. The generated client owns (de)serialization via the codec; the carrier
/// only moves bytes, so it can be HTTP, a queue, or an in-process loop.
pub trait Transport {
    fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError>;

    /// Perform a call whose operation declares several reply arms and return
    /// the status-zero reply `variant` with its payload, so the client can
    /// return the success arm or the declared error arm.
    fn call_reply(&self, service: &str, op: &str, req: &[u8]) -> Result<WireReply, ClientError>;
}
impl From<CsilCborError> for ClientError {
    fn from(error: CsilCborError) -> Self {
        Self::Decode(error.0)
    }
}

/// Resolve a status-zero RPC reply into its success payload or its declared error
/// arm. Generated clients call it with the reply from `Transport::call_reply`.
pub fn resolve_rpc_reply(
    service: &str,
    op: &str,
    variant: Option<&str>,
    payload: Vec<u8>,
) -> Result<Vec<u8>, ClientError> {
    match (service, op) {
        ("CorndogsService", "SubmitTask") => match variant {
            Some("SubmitTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetTaskStateByID") => match variant {
            Some("GetTaskStateByIDResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetNextTask") => match variant {
            Some("GetNextTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetNextTaskGroup") => match variant {
            Some("GetNextTaskGroupResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "UpdateTask") => match variant {
            Some("UpdateTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "CompleteTask") => match variant {
            Some("CompleteTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "CancelTask") => match variant {
            Some("CancelTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "CleanUpTimedOut") => match variant {
            Some("CleanUpTimedOutResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetQueues") => match variant {
            Some("GetQueuesResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetQueueTaskCounts") => match variant {
            Some("GetQueueTaskCountsResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetTaskStateCounts") => match variant {
            Some("GetTaskStateCountsResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetQueueAndStateCounts") => match variant {
            Some("GetQueueAndStateCountsResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetServerInfo") => match variant {
            Some("GetServerInfoResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "SubmitKeyedTask") => match variant {
            Some("SubmitKeyedTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "LookupSubmission") => match variant {
            Some("LookupSubmissionResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "ClaimGuardedTask") => match variant {
            Some("ClaimGuardedTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "ClaimGuardedTaskGroup") => match variant {
            Some("ClaimGuardedTaskGroupResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "UpdateGuardedTask") => match variant {
            Some("UpdateGuardedTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "CompleteGuardedTask") => match variant {
            Some("CompleteGuardedTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "CancelGuardedTask") => match variant {
            Some("CancelGuardedTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "GetGuardedTask") => match variant {
            Some("GetGuardedTaskResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        ("CorndogsService", "LookupOperation") => match variant {
            Some("LookupOperationResponse") => Ok(payload),
            Some("ServiceError") => {
                let value = decode_service_error(&payload)?;
                let value = Box::new(value);
                let error = ClientError::ApplicationServiceError(value);
                Err(error)
            }
            _ => Err(ClientError::Decode(format!(
                "invalid reply variant {variant:?}"
            ))),
        },
        _ => Ok(payload),
    }
}

/// Typed client for the CorndogsService service.
pub struct CorndogsClient<T: Transport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: Transport> CorndogsClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// SubmitTask (request/response).
    pub fn submit_task(&self, req: SubmitTaskRequest) -> Result<SubmitTaskResponse, ClientError> {
        let csil_req = match encode_submit_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply = self
            .transport
            .call_reply("CorndogsService", "SubmitTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "SubmitTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_submit_task_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetTaskStateByID (request/response).
    pub fn get_task_state_by_id(
        &self,
        req: GetTaskStateByIDRequest,
    ) -> Result<GetTaskStateByIDResponse, ClientError> {
        let csil_req = match encode_get_task_state_by_id_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "GetTaskStateByID", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetTaskStateByID",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_task_state_by_id_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetNextTask (request/response).
    pub fn get_next_task(
        &self,
        req: GetNextTaskRequest,
    ) -> Result<GetNextTaskResponse, ClientError> {
        let csil_req = match encode_get_next_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply = self
            .transport
            .call_reply("CorndogsService", "GetNextTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetNextTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_next_task_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetNextTaskGroup (request/response).
    pub fn get_next_task_group(
        &self,
        req: GetNextTaskGroupRequest,
    ) -> Result<GetNextTaskGroupResponse, ClientError> {
        let csil_req = match encode_get_next_task_group_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "GetNextTaskGroup", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetNextTaskGroup",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_next_task_group_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// UpdateTask (request/response).
    pub fn update_task(&self, req: UpdateTaskRequest) -> Result<UpdateTaskResponse, ClientError> {
        let csil_req = match encode_update_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply = self
            .transport
            .call_reply("CorndogsService", "UpdateTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "UpdateTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_update_task_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// CompleteTask (request/response).
    pub fn complete_task(
        &self,
        req: CompleteTaskRequest,
    ) -> Result<CompleteTaskResponse, ClientError> {
        let csil_req = match encode_complete_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply = self
            .transport
            .call_reply("CorndogsService", "CompleteTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "CompleteTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_complete_task_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// CancelTask (request/response).
    pub fn cancel_task(&self, req: CancelTaskRequest) -> Result<CancelTaskResponse, ClientError> {
        let csil_req = match encode_cancel_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply = self
            .transport
            .call_reply("CorndogsService", "CancelTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "CancelTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_cancel_task_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// CleanUpTimedOut (request/response).
    pub fn clean_up_timed_out(
        &self,
        req: CleanUpTimedOutRequest,
    ) -> Result<CleanUpTimedOutResponse, ClientError> {
        let csil_req = match encode_clean_up_timed_out_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "CleanUpTimedOut", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "CleanUpTimedOut",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_clean_up_timed_out_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetQueues (request/response).
    pub fn get_queues(&self, req: GetQueuesRequest) -> Result<GetQueuesResponse, ClientError> {
        let csil_req = match encode_get_queues_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply = self
            .transport
            .call_reply("CorndogsService", "GetQueues", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetQueues",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_queues_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetQueueTaskCounts (request/response).
    pub fn get_queue_task_counts(
        &self,
        req: GetQueueTaskCountsRequest,
    ) -> Result<GetQueueTaskCountsResponse, ClientError> {
        let csil_req = match encode_get_queue_task_counts_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "GetQueueTaskCounts", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetQueueTaskCounts",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_queue_task_counts_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetTaskStateCounts (request/response).
    pub fn get_task_state_counts(
        &self,
        req: GetTaskStateCountsRequest,
    ) -> Result<GetTaskStateCountsResponse, ClientError> {
        let csil_req = match encode_get_task_state_counts_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "GetTaskStateCounts", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetTaskStateCounts",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_task_state_counts_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetQueueAndStateCounts (request/response).
    pub fn get_queue_and_state_counts(
        &self,
        req: GetQueueAndStateCountsRequest,
    ) -> Result<GetQueueAndStateCountsResponse, ClientError> {
        let csil_req = match encode_get_queue_and_state_counts_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "GetQueueAndStateCounts", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetQueueAndStateCounts",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_queue_and_state_counts_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetServerInfo (request/response).
    pub fn get_server_info(
        &self,
        req: GetServerInfoRequest,
    ) -> Result<GetServerInfoResponse, ClientError> {
        let csil_req = match encode_get_server_info_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "GetServerInfo", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetServerInfo",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_server_info_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// SubmitKeyedTask (request/response).
    pub fn submit_keyed_task(
        &self,
        req: SubmitKeyedTaskRequest,
    ) -> Result<SubmitKeyedTaskResponse, ClientError> {
        let csil_req = match encode_submit_keyed_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "SubmitKeyedTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "SubmitKeyedTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_submit_keyed_task_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// LookupSubmission (request/response).
    pub fn lookup_submission(
        &self,
        req: LookupSubmissionRequest,
    ) -> Result<LookupSubmissionResponse, ClientError> {
        let csil_req = match encode_lookup_submission_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "LookupSubmission", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "LookupSubmission",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_lookup_submission_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// ClaimGuardedTask (request/response).
    pub fn claim_guarded_task(
        &self,
        req: ClaimGuardedTaskRequest,
    ) -> Result<ClaimGuardedTaskResponse, ClientError> {
        let csil_req = match encode_claim_guarded_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "ClaimGuardedTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "ClaimGuardedTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_claim_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// ClaimGuardedTaskGroup (request/response).
    pub fn claim_guarded_task_group(
        &self,
        req: ClaimGuardedTaskGroupRequest,
    ) -> Result<ClaimGuardedTaskGroupResponse, ClientError> {
        let csil_req = match encode_claim_guarded_task_group_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "ClaimGuardedTaskGroup", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "ClaimGuardedTaskGroup",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_claim_guarded_task_group_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// UpdateGuardedTask (request/response).
    pub fn update_guarded_task(
        &self,
        req: UpdateGuardedTaskRequest,
    ) -> Result<UpdateGuardedTaskResponse, ClientError> {
        let csil_req = match encode_update_guarded_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "UpdateGuardedTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "UpdateGuardedTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_update_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// CompleteGuardedTask (request/response).
    pub fn complete_guarded_task(
        &self,
        req: CompleteGuardedTaskRequest,
    ) -> Result<CompleteGuardedTaskResponse, ClientError> {
        let csil_req = match encode_complete_guarded_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "CompleteGuardedTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "CompleteGuardedTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_complete_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// CancelGuardedTask (request/response).
    pub fn cancel_guarded_task(
        &self,
        req: CancelGuardedTaskRequest,
    ) -> Result<CancelGuardedTaskResponse, ClientError> {
        let csil_req = match encode_cancel_guarded_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "CancelGuardedTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "CancelGuardedTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_cancel_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// GetGuardedTask (request/response).
    pub fn get_guarded_task(
        &self,
        req: GetGuardedTaskRequest,
    ) -> Result<GetGuardedTaskResponse, ClientError> {
        let csil_req = match encode_get_guarded_task_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "GetGuardedTask", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "GetGuardedTask",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_get_guarded_task_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }

    /// LookupOperation (request/response).
    pub fn lookup_operation(
        &self,
        req: LookupOperationRequest,
    ) -> Result<LookupOperationResponse, ClientError> {
        let csil_req = match encode_lookup_operation_request(&req) {
            Ok(csil_bytes) => csil_bytes,
            Err(e) => return Err(ClientError::Encode(e.to_string())),
        };
        let csil_reply =
            self.transport
                .call_reply("CorndogsService", "LookupOperation", &csil_req)?;
        let csil_resp = resolve_rpc_reply(
            "CorndogsService",
            "LookupOperation",
            csil_reply.variant.as_deref(),
            csil_reply.payload,
        )?;
        decode_lookup_operation_response(&csil_resp).map_err(|e| ClientError::Decode(e.to_string()))
    }
}
