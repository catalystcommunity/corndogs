//! Generated service traits from CSIL specification

use super::types::*;

/// CorndogsService service trait
pub trait CorndogsService {
    type Context;
    /// SubmitTask (request/response).
    fn submit_task(&self, ctx: &Self::Context, input: SubmitTaskRequest) -> Result<SubmitTaskResponse, ServiceError>;
    /// GetTaskStateByID (request/response).
    fn get_task_state_by_id(&self, ctx: &Self::Context, input: GetTaskStateByIDRequest) -> Result<GetTaskStateByIDResponse, ServiceError>;
    /// GetNextTask (request/response).
    fn get_next_task(&self, ctx: &Self::Context, input: GetNextTaskRequest) -> Result<GetNextTaskResponse, ServiceError>;
    /// UpdateTask (request/response).
    fn update_task(&self, ctx: &Self::Context, input: UpdateTaskRequest) -> Result<UpdateTaskResponse, ServiceError>;
    /// CompleteTask (request/response).
    fn complete_task(&self, ctx: &Self::Context, input: CompleteTaskRequest) -> Result<CompleteTaskResponse, ServiceError>;
    /// CancelTask (request/response).
    fn cancel_task(&self, ctx: &Self::Context, input: CancelTaskRequest) -> Result<CancelTaskResponse, ServiceError>;
    /// CleanUpTimedOut (request/response).
    fn clean_up_timed_out(&self, ctx: &Self::Context, input: CleanUpTimedOutRequest) -> Result<CleanUpTimedOutResponse, ServiceError>;
    /// GetQueues (request/response).
    fn get_queues(&self, ctx: &Self::Context, input: GetQueuesRequest) -> Result<GetQueuesResponse, ServiceError>;
    /// GetQueueTaskCounts (request/response).
    fn get_queue_task_counts(&self, ctx: &Self::Context, input: GetQueueTaskCountsRequest) -> Result<GetQueueTaskCountsResponse, ServiceError>;
    /// GetTaskStateCounts (request/response).
    fn get_task_state_counts(&self, ctx: &Self::Context, input: GetTaskStateCountsRequest) -> Result<GetTaskStateCountsResponse, ServiceError>;
    /// GetQueueAndStateCounts (request/response).
    fn get_queue_and_state_counts(&self, ctx: &Self::Context, input: GetQueueAndStateCountsRequest) -> Result<GetQueueAndStateCountsResponse, ServiceError>;
}

