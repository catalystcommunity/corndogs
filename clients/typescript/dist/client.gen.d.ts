import type { CancelTaskRequest, CancelTaskResponse, CleanUpTimedOutRequest, CleanUpTimedOutResponse, CompleteTaskRequest, CompleteTaskResponse, GetNextTaskRequest, GetNextTaskResponse, GetQueueAndStateCountsRequest, GetQueueAndStateCountsResponse, GetQueueTaskCountsRequest, GetQueueTaskCountsResponse, GetQueuesRequest, GetQueuesResponse, GetTaskStateByIDRequest, GetTaskStateByIDResponse, GetTaskStateCountsRequest, GetTaskStateCountsResponse, SubmitTaskRequest, SubmitTaskResponse, UpdateTaskRequest, UpdateTaskResponse } from "./types.gen";
export interface ServiceTransport {
    call(service: string, op: string, req: Uint8Array): Uint8Array;
}
export declare class CorndogsClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    submitTask(req: SubmitTaskRequest): SubmitTaskResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getTaskStateByID(req: GetTaskStateByIDRequest): GetTaskStateByIDResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getNextTask(req: GetNextTaskRequest): GetNextTaskResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    updateTask(req: UpdateTaskRequest): UpdateTaskResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    completeTask(req: CompleteTaskRequest): CompleteTaskResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    cancelTask(req: CancelTaskRequest): CancelTaskResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    cleanUpTimedOut(req: CleanUpTimedOutRequest): CleanUpTimedOutResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getQueues(req: GetQueuesRequest): GetQueuesResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getQueueTaskCounts(req: GetQueueTaskCountsRequest): GetQueueTaskCountsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getTaskStateCounts(req: GetTaskStateCountsRequest): GetTaskStateCountsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getQueueAndStateCounts(req: GetQueueAndStateCountsRequest): GetQueueAndStateCountsResponse;
}
export declare class ApiClient {
    readonly corndogs: CorndogsClient;
    constructor(t: ServiceTransport);
}
