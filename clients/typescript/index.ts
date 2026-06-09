// Public entrypoint for the corndogs TypeScript client.
//
//   import { CorndogsClient, CborHttpTransport } from "@catalystcommunity/corndogs-client";
//   const client = new CorndogsClient(new CborHttpTransport("https://corndogs.example.com"));
//   const { task } = await client.submitTask({ queue: "q", payload: new Uint8Array(), priority: 0, ... });
export * from "./gen/types.gen";
export * from "./gen/client.gen";
export { CborHttpTransport, CsilError } from "./transport";
export type { CborHttpTransportOptions } from "./transport";
