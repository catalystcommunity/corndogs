//! Generated transport-agnostic service clients from CSIL specification

#![allow(async_fn_in_trait)]

use super::types::*;
use super::codec::*;
use super::client::ClientError;

/// The caller-supplied byte carrier: it performs the call named by `(service, op)`
/// with the already-encoded request bytes and returns the response bytes, or an
/// error. The generated client owns (de)serialization via the codec; the carrier
/// only moves bytes, so it can be HTTP, a queue, or an in-process loop.
pub trait AsyncTransport {
    async fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError>;
}

/// Typed client for the CorndogsService service.
pub struct CorndogsAsyncClient<T: AsyncTransport> {
    transport: T,
}

impl<T: AsyncTransport> CorndogsAsyncClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// SubmitTask (request/response).
    pub async fn submit_task(&self, req: SubmitTaskRequest) -> Result<SubmitTaskResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "SubmitTask", &encode_submit_task_request(&req)).await?;
        decode_submit_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetTaskStateByID (request/response).
    pub async fn get_task_state_by_id(&self, req: GetTaskStateByIDRequest) -> Result<GetTaskStateByIDResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "GetTaskStateByID", &encode_get_task_state_by_id_request(&req)).await?;
        decode_get_task_state_by_id_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetNextTask (request/response).
    pub async fn get_next_task(&self, req: GetNextTaskRequest) -> Result<GetNextTaskResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "GetNextTask", &encode_get_next_task_request(&req)).await?;
        decode_get_next_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// UpdateTask (request/response).
    pub async fn update_task(&self, req: UpdateTaskRequest) -> Result<UpdateTaskResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "UpdateTask", &encode_update_task_request(&req)).await?;
        decode_update_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CompleteTask (request/response).
    pub async fn complete_task(&self, req: CompleteTaskRequest) -> Result<CompleteTaskResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "CompleteTask", &encode_complete_task_request(&req)).await?;
        decode_complete_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CancelTask (request/response).
    pub async fn cancel_task(&self, req: CancelTaskRequest) -> Result<CancelTaskResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "CancelTask", &encode_cancel_task_request(&req)).await?;
        decode_cancel_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CleanUpTimedOut (request/response).
    pub async fn clean_up_timed_out(&self, req: CleanUpTimedOutRequest) -> Result<CleanUpTimedOutResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "CleanUpTimedOut", &encode_clean_up_timed_out_request(&req)).await?;
        decode_clean_up_timed_out_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetQueues (request/response).
    pub async fn get_queues(&self, req: GetQueuesRequest) -> Result<GetQueuesResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "GetQueues", &encode_get_queues_request(&req)).await?;
        decode_get_queues_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetQueueTaskCounts (request/response).
    pub async fn get_queue_task_counts(&self, req: GetQueueTaskCountsRequest) -> Result<GetQueueTaskCountsResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "GetQueueTaskCounts", &encode_get_queue_task_counts_request(&req)).await?;
        decode_get_queue_task_counts_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetTaskStateCounts (request/response).
    pub async fn get_task_state_counts(&self, req: GetTaskStateCountsRequest) -> Result<GetTaskStateCountsResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "GetTaskStateCounts", &encode_get_task_state_counts_request(&req)).await?;
        decode_get_task_state_counts_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetQueueAndStateCounts (request/response).
    pub async fn get_queue_and_state_counts(&self, req: GetQueueAndStateCountsRequest) -> Result<GetQueueAndStateCountsResponse, ClientError> {
        let csil_resp = self.transport.call("corndogs", "GetQueueAndStateCounts", &encode_get_queue_and_state_counts_request(&req)).await?;
        decode_get_queue_and_state_counts_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }
}

