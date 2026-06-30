//! Generated types from CSIL specification

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
    pub payload: Vec<u8>,
    pub priority: i64,
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
    pub task: Option<Task>,
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
    pub payload: Vec<u8>,
    pub priority: i64,
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
pub struct GetQueuesRequest {
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueuesResponse {
    pub queues: Vec<String>,
    pub total_task_count: i64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueueTaskCountsRequest {
}

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
pub struct GetQueueAndStateCountsRequest {
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetQueueAndStateCountsResponse {
    pub queue_and_state_counts: QueueAndStateCountsMap,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ServiceError {
    pub code: u64,
    pub message: String,
}

