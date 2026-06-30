import type { CancelTaskRequest, CancelTaskResponse, CleanUpTimedOutRequest, CleanUpTimedOutResponse, CompleteTaskRequest, CompleteTaskResponse, GetNextTaskRequest, GetNextTaskResponse, GetQueueAndStateCountsRequest, GetQueueAndStateCountsResponse, GetQueueTaskCountsRequest, GetQueueTaskCountsResponse, GetQueuesRequest, GetQueuesResponse, GetTaskStateByIDRequest, GetTaskStateByIDResponse, GetTaskStateCountsRequest, GetTaskStateCountsResponse, SubmitTaskRequest, SubmitTaskResponse, UpdateTaskRequest, UpdateTaskResponse } from "./types.gen";
export interface AsyncServiceTransport {
    call(service: string, op: string, req: Uint8Array): Promise<Uint8Array>;
}
export declare class CorndogsAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    submitTask(req: SubmitTaskRequest): Promise<SubmitTaskResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getTaskStateByID(req: GetTaskStateByIDRequest): Promise<GetTaskStateByIDResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getNextTask(req: GetNextTaskRequest): Promise<GetNextTaskResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    updateTask(req: UpdateTaskRequest): Promise<UpdateTaskResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    completeTask(req: CompleteTaskRequest): Promise<CompleteTaskResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    cancelTask(req: CancelTaskRequest): Promise<CancelTaskResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    cleanUpTimedOut(req: CleanUpTimedOutRequest): Promise<CleanUpTimedOutResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getQueues(req: GetQueuesRequest): Promise<GetQueuesResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getQueueTaskCounts(req: GetQueueTaskCountsRequest): Promise<GetQueueTaskCountsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getTaskStateCounts(req: GetTaskStateCountsRequest): Promise<GetTaskStateCountsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getQueueAndStateCounts(req: GetQueueAndStateCountsRequest): Promise<GetQueueAndStateCountsResponse>;
}
export declare class AsyncApiClient {
    readonly corndogs: CorndogsAsyncClient;
    constructor(t: AsyncServiceTransport);
}
