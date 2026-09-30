//! Generated transport-agnostic service clients from CSIL specification

#![allow(async_fn_in_trait)]

use super::client::ClientError;
use super::codec::*;
use super::types::*;

/// The caller-supplied byte carrier: it performs the call named by `(service, op)`
/// with the already-encoded request bytes and returns the response bytes, or an
/// error. The generated client owns (de)serialization via the codec; the carrier
/// only moves bytes, so it can be HTTP, a queue, or an in-process loop.
pub trait AsyncTransport {
    async fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError>;
}

/// Typed client for the CorndogsService service.
pub struct CorndogsAsyncClient<T: AsyncTransport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: AsyncTransport> CorndogsAsyncClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// SubmitTask (request/response).
    pub async fn submit_task(
        &self,
        req: SubmitTaskRequest,
    ) -> Result<SubmitTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "SubmitTask",
                &encode_submit_task_request(&req),
            )
            .await?;
        decode_submit_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetTaskStateByID (request/response).
    pub async fn get_task_state_by_id(
        &self,
        req: GetTaskStateByIDRequest,
    ) -> Result<GetTaskStateByIDResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetTaskStateByID",
                &encode_get_task_state_by_id_request(&req),
            )
            .await?;
        decode_get_task_state_by_id_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetNextTask (request/response).
    pub async fn get_next_task(
        &self,
        req: GetNextTaskRequest,
    ) -> Result<GetNextTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetNextTask",
                &encode_get_next_task_request(&req),
            )
            .await?;
        decode_get_next_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetNextTaskGroup (request/response).
    pub async fn get_next_task_group(
        &self,
        req: GetNextTaskGroupRequest,
    ) -> Result<GetNextTaskGroupResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetNextTaskGroup",
                &encode_get_next_task_group_request(&req),
            )
            .await?;
        decode_get_next_task_group_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// UpdateTask (request/response).
    pub async fn update_task(
        &self,
        req: UpdateTaskRequest,
    ) -> Result<UpdateTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "UpdateTask",
                &encode_update_task_request(&req),
            )
            .await?;
        decode_update_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CompleteTask (request/response).
    pub async fn complete_task(
        &self,
        req: CompleteTaskRequest,
    ) -> Result<CompleteTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "CompleteTask",
                &encode_complete_task_request(&req),
            )
            .await?;
        decode_complete_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CancelTask (request/response).
    pub async fn cancel_task(
        &self,
        req: CancelTaskRequest,
    ) -> Result<CancelTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "CancelTask",
                &encode_cancel_task_request(&req),
            )
            .await?;
        decode_cancel_task_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CleanUpTimedOut (request/response).
    pub async fn clean_up_timed_out(
        &self,
        req: CleanUpTimedOutRequest,
    ) -> Result<CleanUpTimedOutResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "CleanUpTimedOut",
                &encode_clean_up_timed_out_request(&req),
            )
            .await?;
        decode_clean_up_timed_out_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetQueues (request/response).
    pub async fn get_queues(
        &self,
        req: GetQueuesRequest,
    ) -> Result<GetQueuesResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetQueues",
                &encode_get_queues_request(&req),
            )
            .await?;
        decode_get_queues_response(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetQueueTaskCounts (request/response).
    pub async fn get_queue_task_counts(
        &self,
        req: GetQueueTaskCountsRequest,
    ) -> Result<GetQueueTaskCountsResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetQueueTaskCounts",
                &encode_get_queue_task_counts_request(&req),
            )
            .await?;
        decode_get_queue_task_counts_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetTaskStateCounts (request/response).
    pub async fn get_task_state_counts(
        &self,
        req: GetTaskStateCountsRequest,
    ) -> Result<GetTaskStateCountsResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetTaskStateCounts",
                &encode_get_task_state_counts_request(&req),
            )
            .await?;
        decode_get_task_state_counts_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetQueueAndStateCounts (request/response).
    pub async fn get_queue_and_state_counts(
        &self,
        req: GetQueueAndStateCountsRequest,
    ) -> Result<GetQueueAndStateCountsResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetQueueAndStateCounts",
                &encode_get_queue_and_state_counts_request(&req),
            )
            .await?;
        decode_get_queue_and_state_counts_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetServerInfo (request/response).
    pub async fn get_server_info(
        &self,
        req: GetServerInfoRequest,
    ) -> Result<GetServerInfoResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetServerInfo",
                &encode_get_server_info_request(&req),
            )
            .await?;
        decode_get_server_info_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// SubmitKeyedTask (request/response).
    pub async fn submit_keyed_task(
        &self,
        req: SubmitKeyedTaskRequest,
    ) -> Result<SubmitKeyedTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "SubmitKeyedTask",
                &encode_submit_keyed_task_request(&req),
            )
            .await?;
        decode_submit_keyed_task_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// LookupSubmission (request/response).
    pub async fn lookup_submission(
        &self,
        req: LookupSubmissionRequest,
    ) -> Result<LookupSubmissionResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "LookupSubmission",
                &encode_lookup_submission_request(&req),
            )
            .await?;
        decode_lookup_submission_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// ClaimGuardedTask (request/response).
    pub async fn claim_guarded_task(
        &self,
        req: ClaimGuardedTaskRequest,
    ) -> Result<ClaimGuardedTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "ClaimGuardedTask",
                &encode_claim_guarded_task_request(&req),
            )
            .await?;
        decode_claim_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// ClaimGuardedTaskGroup (request/response).
    pub async fn claim_guarded_task_group(
        &self,
        req: ClaimGuardedTaskGroupRequest,
    ) -> Result<ClaimGuardedTaskGroupResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "ClaimGuardedTaskGroup",
                &encode_claim_guarded_task_group_request(&req),
            )
            .await?;
        decode_claim_guarded_task_group_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// UpdateGuardedTask (request/response).
    pub async fn update_guarded_task(
        &self,
        req: UpdateGuardedTaskRequest,
    ) -> Result<UpdateGuardedTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "UpdateGuardedTask",
                &encode_update_guarded_task_request(&req),
            )
            .await?;
        decode_update_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CompleteGuardedTask (request/response).
    pub async fn complete_guarded_task(
        &self,
        req: CompleteGuardedTaskRequest,
    ) -> Result<CompleteGuardedTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "CompleteGuardedTask",
                &encode_complete_guarded_task_request(&req),
            )
            .await?;
        decode_complete_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// CancelGuardedTask (request/response).
    pub async fn cancel_guarded_task(
        &self,
        req: CancelGuardedTaskRequest,
    ) -> Result<CancelGuardedTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "CancelGuardedTask",
                &encode_cancel_guarded_task_request(&req),
            )
            .await?;
        decode_cancel_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// GetGuardedTask (request/response).
    pub async fn get_guarded_task(
        &self,
        req: GetGuardedTaskRequest,
    ) -> Result<GetGuardedTaskResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "GetGuardedTask",
                &encode_get_guarded_task_request(&req),
            )
            .await?;
        decode_get_guarded_task_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// LookupOperation (request/response).
    pub async fn lookup_operation(
        &self,
        req: LookupOperationRequest,
    ) -> Result<LookupOperationResponse, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "CorndogsService",
                "LookupOperation",
                &encode_lookup_operation_request(&req),
            )
            .await?;
        decode_lookup_operation_response(&csil_resp)
            .map_err(|e| ClientError::Transport(e.to_string()))
    }
}
