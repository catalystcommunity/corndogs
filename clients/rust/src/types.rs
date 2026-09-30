//! Generated types from CSIL specification

#![allow(non_camel_case_types, clippy::large_enum_variant)]

pub type StringInt64Map = std::collections::HashMap<String, i64>;

#[derive(Debug, Clone, PartialEq)]
pub struct Task {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
    pub auto_target_state: String,
    pub submit_time: i64,
    pub update_time: i64,
    pub timeout: i64,
    pub priority: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct TaskDelivery {
    pub task: Task,
    pub payload: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SubmitTaskRequest {
    pub queue: String,
    pub current_state: String,
    pub auto_target_state: String,
    pub timeout: i64,
    pub payload: Vec<u8>,
    pub priority: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SubmitTaskResponse {
    pub task: Option<Task>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetTaskStateByIDRequest {
    pub uuid: String,
    pub queue: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetTaskStateByIDResponse {
    pub task: Option<Task>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetNextTaskRequest {
    pub queue: String,
    pub current_state: String,
    pub override_timeout: i64,
    pub override_current_state: String,
    pub override_auto_target_state: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetNextTaskResponse {
    pub delivery: Option<TaskDelivery>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetNextTaskGroupRequest {
    pub queues: Vec<String>,
    pub current_state: String,
    pub override_timeout: i64,
    pub override_current_state: String,
    pub override_auto_target_state: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetNextTaskGroupResponse {
    pub delivery: Option<TaskDelivery>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CompleteTaskRequest {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CompleteTaskResponse {
    pub task: Option<Task>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct UpdateTaskRequest {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
    pub auto_target_state: String,
    pub timeout: i64,
    pub new_state: String,
    pub payload: Option<Vec<u8>>,
    pub priority: Option<i64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct UpdateTaskResponse {
    pub task: Option<Task>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CancelTaskRequest {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CancelTaskResponse {
    pub task: Option<Task>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CleanUpTimedOutRequest {
    pub at_time: i64,
    pub queue: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CleanUpTimedOutResponse {
    pub timed_out: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueuesRequest {}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueuesResponse {
    pub queues: Vec<String>,
    pub total_task_count: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueueTaskCountsRequest {}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueueTaskCountsResponse {
    pub queue_counts: StringInt64Map,
    pub total_task_count: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetTaskStateCountsRequest {
    pub queue: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetTaskStateCountsResponse {
    pub queue: String,
    pub count: i64,
    pub state_counts: StringInt64Map,
}

#[derive(Debug, Clone, PartialEq)]
pub struct QueueAndStateCounts {
    pub queue: String,
    pub count: i64,
    pub state_counts: StringInt64Map,
}

pub type QueueAndStateCountsMap = std::collections::HashMap<String, QueueAndStateCounts>;

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueueAndStateCountsRequest {}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueueAndStateCountsResponse {
    pub queue_and_state_counts: QueueAndStateCountsMap,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetServerInfoRequest {}

#[derive(Debug, Clone, PartialEq)]
pub struct GetServerInfoResponse {
    pub server_version: String,
    pub features: Vec<String>,
    pub submission_key_policy: String,
    pub task_guard_policy: String,
    pub receipt_retention_seconds: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GuardedTask {
    pub task: Task,
    pub guarded: bool,
    pub revision: i64,
    pub terminal: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SubmissionReceipt {
    pub queue: String,
    pub submission_key: String,
    pub task_uuid: String,
    pub accepted_at: i64,
    pub expires_at: i64,
    pub guarded: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SubmitKeyedTaskRequest {
    pub submission_key: String,
    pub guarded: bool,
    pub queue: String,
    pub current_state: String,
    pub auto_target_state: String,
    pub timeout: i64,
    pub payload: Vec<u8>,
    pub priority: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SubmitKeyedTaskResponse {
    pub receipt: SubmissionReceipt,
    pub replayed: bool,
    pub task: Option<GuardedTask>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct LookupSubmissionRequest {
    pub queue: String,
    pub submission_key: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct LookupSubmissionResponse {
    pub receipt: Option<SubmissionReceipt>,
    pub task: Option<GuardedTask>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ClaimGuardedTaskRequest {
    pub operation_id: String,
    pub queue: String,
    pub current_state: String,
    pub override_timeout: i64,
    pub override_current_state: String,
    pub override_auto_target_state: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GuardedDelivery {
    pub task: GuardedTask,
    pub payload: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ClaimGuardedTaskResponse {
    pub delivery: Option<GuardedDelivery>,
    pub replayed: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ClaimGuardedTaskGroupRequest {
    pub operation_id: String,
    pub queues: Vec<String>,
    pub current_state: String,
    pub override_timeout: i64,
    pub override_current_state: String,
    pub override_auto_target_state: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ClaimGuardedTaskGroupResponse {
    pub delivery: Option<GuardedDelivery>,
    pub replayed: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct UpdateGuardedTaskRequest {
    pub operation_id: String,
    pub uuid: String,
    pub queue: String,
    pub expected_revision: i64,
    pub expected_state: Option<String>,
    pub new_state: String,
    pub auto_target_state: String,
    pub timeout: i64,
    pub payload: Option<Vec<u8>>,
    pub priority: Option<i64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct UpdateGuardedTaskResponse {
    pub task: GuardedTask,
    pub replayed: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CompleteGuardedTaskRequest {
    pub operation_id: String,
    pub uuid: String,
    pub queue: String,
    pub expected_revision: i64,
    pub expected_state: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CompleteGuardedTaskResponse {
    pub task: GuardedTask,
    pub replayed: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CancelGuardedTaskRequest {
    pub operation_id: String,
    pub uuid: String,
    pub queue: String,
    pub expected_revision: i64,
    pub expected_state: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CancelGuardedTaskResponse {
    pub task: GuardedTask,
    pub replayed: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetGuardedTaskRequest {
    pub uuid: String,
    pub queue: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetGuardedTaskResponse {
    pub task: Option<GuardedTask>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct OperationReceipt {
    pub operation_id: String,
    pub op: String,
    pub task_uuid: String,
    pub queue: String,
    pub result_revision: i64,
    pub result_state: String,
    pub at: i64,
    pub expires_at: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct LookupOperationRequest {
    pub operation_id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct LookupOperationResponse {
    pub receipt: Option<OperationReceipt>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ServiceError {
    pub code: u64,
    pub message: String,
}
