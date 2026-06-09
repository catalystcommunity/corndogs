//! Generated types from CSIL specification

use serde::{Deserialize, Serialize};

pub type StringInt64Map = std::collections::HashMap<String, i64>;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Task {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
    pub auto_target_state: String,
    pub submit_time: i64,
    pub update_time: i64,
    pub timeout: i64,
    #[serde(with = "serde_bytes")]
    pub payload: Vec<u8>,
    pub priority: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SubmitTaskRequest {
    pub queue: String,
    pub current_state: String,
    pub auto_target_state: String,
    pub timeout: i64,
    #[serde(with = "serde_bytes")]
    pub payload: Vec<u8>,
    pub priority: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SubmitTaskResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task: Option<Task>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetTaskStateByIDRequest {
    pub uuid: String,
    pub queue: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetTaskStateByIDResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task: Option<Task>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetNextTaskRequest {
    pub queue: String,
    pub current_state: String,
    pub override_timeout: i64,
    pub override_current_state: String,
    pub override_auto_target_state: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetNextTaskResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task: Option<Task>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CompleteTaskRequest {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CompleteTaskResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task: Option<Task>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct UpdateTaskRequest {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
    pub auto_target_state: String,
    pub timeout: i64,
    pub new_state: String,
    #[serde(with = "serde_bytes")]
    pub payload: Vec<u8>,
    pub priority: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct UpdateTaskResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task: Option<Task>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CancelTaskRequest {
    pub uuid: String,
    pub queue: String,
    pub current_state: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CancelTaskResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task: Option<Task>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CleanUpTimedOutRequest {
    pub at_time: i64,
    pub queue: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CleanUpTimedOutResponse {
    pub timed_out: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetQueuesRequest {
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetQueuesResponse {
    pub queues: Vec<String>,
    pub total_task_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetQueueTaskCountsRequest {
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetQueueTaskCountsResponse {
    pub queue_counts: StringInt64Map,
    pub total_task_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetTaskStateCountsRequest {
    pub queue: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetTaskStateCountsResponse {
    pub queue: String,
    pub count: i64,
    pub state_counts: StringInt64Map,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct QueueAndStateCounts {
    pub queue: String,
    pub count: i64,
    pub state_counts: StringInt64Map,
}

pub type QueueAndStateCountsMap = std::collections::HashMap<String, QueueAndStateCounts>;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetQueueAndStateCountsRequest {
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GetQueueAndStateCountsResponse {
    pub queue_and_state_counts: QueueAndStateCountsMap,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ServiceError {
    pub code: u64,
    pub message: String,
}

