//! Generated service traits from CSIL specification

use super::types::*;

/// CorndogsService service trait
pub trait CorndogsService {
    type Context;
    #[allow(clippy::result_large_err)]
    /// SubmitTask (request/response).
    fn submit_task(
        &self,
        ctx: &Self::Context,
        input: SubmitTaskRequest,
    ) -> Result<SubmitTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetTaskStateByID (request/response).
    fn get_task_state_by_id(
        &self,
        ctx: &Self::Context,
        input: GetTaskStateByIDRequest,
    ) -> Result<GetTaskStateByIDResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetNextTask (request/response).
    fn get_next_task(
        &self,
        ctx: &Self::Context,
        input: GetNextTaskRequest,
    ) -> Result<GetNextTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetNextTaskGroup (request/response).
    fn get_next_task_group(
        &self,
        ctx: &Self::Context,
        input: GetNextTaskGroupRequest,
    ) -> Result<GetNextTaskGroupResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// UpdateTask (request/response).
    fn update_task(
        &self,
        ctx: &Self::Context,
        input: UpdateTaskRequest,
    ) -> Result<UpdateTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// CompleteTask (request/response).
    fn complete_task(
        &self,
        ctx: &Self::Context,
        input: CompleteTaskRequest,
    ) -> Result<CompleteTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// CancelTask (request/response).
    fn cancel_task(
        &self,
        ctx: &Self::Context,
        input: CancelTaskRequest,
    ) -> Result<CancelTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// CleanUpTimedOut (request/response).
    fn clean_up_timed_out(
        &self,
        ctx: &Self::Context,
        input: CleanUpTimedOutRequest,
    ) -> Result<CleanUpTimedOutResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetQueues (request/response).
    fn get_queues(
        &self,
        ctx: &Self::Context,
        input: GetQueuesRequest,
    ) -> Result<GetQueuesResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetQueueTaskCounts (request/response).
    fn get_queue_task_counts(
        &self,
        ctx: &Self::Context,
        input: GetQueueTaskCountsRequest,
    ) -> Result<GetQueueTaskCountsResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetTaskStateCounts (request/response).
    fn get_task_state_counts(
        &self,
        ctx: &Self::Context,
        input: GetTaskStateCountsRequest,
    ) -> Result<GetTaskStateCountsResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetQueueAndStateCounts (request/response).
    fn get_queue_and_state_counts(
        &self,
        ctx: &Self::Context,
        input: GetQueueAndStateCountsRequest,
    ) -> Result<GetQueueAndStateCountsResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetServerInfo (request/response).
    fn get_server_info(
        &self,
        ctx: &Self::Context,
        input: GetServerInfoRequest,
    ) -> Result<GetServerInfoResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// SubmitKeyedTask (request/response).
    fn submit_keyed_task(
        &self,
        ctx: &Self::Context,
        input: SubmitKeyedTaskRequest,
    ) -> Result<SubmitKeyedTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// LookupSubmission (request/response).
    fn lookup_submission(
        &self,
        ctx: &Self::Context,
        input: LookupSubmissionRequest,
    ) -> Result<LookupSubmissionResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// ClaimGuardedTask (request/response).
    fn claim_guarded_task(
        &self,
        ctx: &Self::Context,
        input: ClaimGuardedTaskRequest,
    ) -> Result<ClaimGuardedTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// ClaimGuardedTaskGroup (request/response).
    fn claim_guarded_task_group(
        &self,
        ctx: &Self::Context,
        input: ClaimGuardedTaskGroupRequest,
    ) -> Result<ClaimGuardedTaskGroupResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// UpdateGuardedTask (request/response).
    fn update_guarded_task(
        &self,
        ctx: &Self::Context,
        input: UpdateGuardedTaskRequest,
    ) -> Result<UpdateGuardedTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// CompleteGuardedTask (request/response).
    fn complete_guarded_task(
        &self,
        ctx: &Self::Context,
        input: CompleteGuardedTaskRequest,
    ) -> Result<CompleteGuardedTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// CancelGuardedTask (request/response).
    fn cancel_guarded_task(
        &self,
        ctx: &Self::Context,
        input: CancelGuardedTaskRequest,
    ) -> Result<CancelGuardedTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// GetGuardedTask (request/response).
    fn get_guarded_task(
        &self,
        ctx: &Self::Context,
        input: GetGuardedTaskRequest,
    ) -> Result<GetGuardedTaskResponse, ServiceError>;
    #[allow(clippy::result_large_err)]
    /// LookupOperation (request/response).
    fn lookup_operation(
        &self,
        ctx: &Self::Context,
        input: LookupOperationRequest,
    ) -> Result<LookupOperationResponse, ServiceError>;
}
