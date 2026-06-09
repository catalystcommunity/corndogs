//! Generated transport-agnostic service clients from CSIL specification

use super::types::*;

/// Error from a generated client call: a structured error the service returned,
/// or a transport-level failure. The caller-supplied `Transport` decides how an
/// error response maps onto `Service`.
#[derive(Debug, Clone)]
pub enum ClientError {
    Service { code: i64, message: String },
    Transport(String),
}

impl std::fmt::Display for ClientError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ClientError::Service { code, message } => write!(f, "service error {code}: {message}"),
            ClientError::Transport(msg) => write!(f, "transport error: {msg}"),
        }
    }
}

impl std::error::Error for ClientError {}

/// The wire is the caller's concern: an implementation encodes `req` (CBOR over
/// HTTP, say), performs the call named by `(service, method)`, and decodes the
/// response into `Res`, or yields a `ClientError`.
pub trait Transport {
    fn call<Req, Res>(&self, service: &str, method: &str, req: &Req) -> Result<Res, ClientError>
    where
        Req: serde::Serialize,
        Res: serde::de::DeserializeOwned;
}

/// Typed client for the CorndogsService service.
pub struct CorndogsClient<T: Transport> {
    transport: T,
}

impl<T: Transport> CorndogsClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// SubmitTask (request/response).
    pub fn submit_task(&self, req: SubmitTaskRequest) -> Result<SubmitTaskResponse, ClientError> {
        self.transport.call("corndogs", "SubmitTask", &req)
    }

    /// GetTaskStateByID (request/response).
    pub fn get_task_state_by_id(&self, req: GetTaskStateByIDRequest) -> Result<GetTaskStateByIDResponse, ClientError> {
        self.transport.call("corndogs", "GetTaskStateByID", &req)
    }

    /// GetNextTask (request/response).
    pub fn get_next_task(&self, req: GetNextTaskRequest) -> Result<GetNextTaskResponse, ClientError> {
        self.transport.call("corndogs", "GetNextTask", &req)
    }

    /// UpdateTask (request/response).
    pub fn update_task(&self, req: UpdateTaskRequest) -> Result<UpdateTaskResponse, ClientError> {
        self.transport.call("corndogs", "UpdateTask", &req)
    }

    /// CompleteTask (request/response).
    pub fn complete_task(&self, req: CompleteTaskRequest) -> Result<CompleteTaskResponse, ClientError> {
        self.transport.call("corndogs", "CompleteTask", &req)
    }

    /// CancelTask (request/response).
    pub fn cancel_task(&self, req: CancelTaskRequest) -> Result<CancelTaskResponse, ClientError> {
        self.transport.call("corndogs", "CancelTask", &req)
    }

    /// CleanUpTimedOut (request/response).
    pub fn clean_up_timed_out(&self, req: CleanUpTimedOutRequest) -> Result<CleanUpTimedOutResponse, ClientError> {
        self.transport.call("corndogs", "CleanUpTimedOut", &req)
    }

    /// GetQueues (request/response).
    pub fn get_queues(&self, req: GetQueuesRequest) -> Result<GetQueuesResponse, ClientError> {
        self.transport.call("corndogs", "GetQueues", &req)
    }

    /// GetQueueTaskCounts (request/response).
    pub fn get_queue_task_counts(&self, req: GetQueueTaskCountsRequest) -> Result<GetQueueTaskCountsResponse, ClientError> {
        self.transport.call("corndogs", "GetQueueTaskCounts", &req)
    }

    /// GetTaskStateCounts (request/response).
    pub fn get_task_state_counts(&self, req: GetTaskStateCountsRequest) -> Result<GetTaskStateCountsResponse, ClientError> {
        self.transport.call("corndogs", "GetTaskStateCounts", &req)
    }

    /// GetQueueAndStateCounts (request/response).
    pub fn get_queue_and_state_counts(&self, req: GetQueueAndStateCountsRequest) -> Result<GetQueueAndStateCountsResponse, ClientError> {
        self.transport.call("corndogs", "GetQueueAndStateCounts", &req)
    }
}

