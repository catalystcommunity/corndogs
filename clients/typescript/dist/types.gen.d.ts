export type StringInt64Map = Record<string, number>;
/**
 * A task as returned by the service. Response-only shape.
 */
export interface Task {
    uuid: string;
    queue: string;
    currentState: string;
    autoTargetState: string;
    submitTime: number;
    updateTime: number;
    timeout: number;
    payload: Uint8Array;
    priority: number;
}
export interface SubmitTaskRequest {
    queue: string;
    currentState: string;
    autoTargetState: string;
    timeout: number;
    payload: Uint8Array;
    priority: number;
}
export interface SubmitTaskResponse {
    task?: Task;
}
export interface GetTaskStateByIDRequest {
    uuid: string;
    queue: string;
}
export interface GetTaskStateByIDResponse {
    task?: Task;
}
export interface GetNextTaskRequest {
    queue: string;
    currentState: string;
    overrideTimeout: number;
    overrideCurrentState: string;
    overrideAutoTargetState: string;
}
export interface GetNextTaskResponse {
    task?: Task;
}
export interface CompleteTaskRequest {
    uuid: string;
    queue: string;
    currentState: string;
}
export interface CompleteTaskResponse {
    task?: Task;
}
export interface UpdateTaskRequest {
    uuid: string;
    queue: string;
    currentState: string;
    autoTargetState: string;
    timeout: number;
    newState: string;
    payload: Uint8Array;
    priority: number;
}
export interface UpdateTaskResponse {
    task?: Task;
}
export interface CancelTaskRequest {
    uuid: string;
    queue: string;
    currentState: string;
}
export interface CancelTaskResponse {
    task?: Task;
}
export interface CleanUpTimedOutRequest {
    atTime: number;
    queue: string;
}
export interface CleanUpTimedOutResponse {
    timedOut: number;
}
export interface GetQueuesRequest {
}
export interface GetQueuesResponse {
    queues: string[];
    totalTaskCount: number;
}
export interface GetQueueTaskCountsRequest {
}
export interface GetQueueTaskCountsResponse {
    queueCounts: StringInt64Map;
    totalTaskCount: number;
}
export interface GetTaskStateCountsRequest {
    queue: string;
}
export interface GetTaskStateCountsResponse {
    queue: string;
    count: number;
    stateCounts: StringInt64Map;
}
export interface QueueAndStateCounts {
    queue: string;
    count: number;
    stateCounts: StringInt64Map;
}
export type QueueAndStateCountsMap = Record<string, QueueAndStateCounts>;
export interface GetQueueAndStateCountsRequest {
}
export interface GetQueueAndStateCountsResponse {
    queueAndStateCounts: QueueAndStateCountsMap;
}
export interface ServiceError {
    code: number;
    message: string;
}
