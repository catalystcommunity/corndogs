// CBOR-over-HTTP transport for the generated CorndogsClient.
//
// The generated client (client.gen.ts) is transport-agnostic: it calls
// ServiceTransport.call(service, method, req) and leaves the wire to us. This is
// the corndogs wire: CBOR bytes in the POST body to
//   POST {baseUrl}/v1alpha1/{service}/{method}
// with content-type/accept application/cbor. service is the lowercased service
// name ("corndogs"); method is the operation name as written in CSIL
// ("SubmitTask"). Map/field keys on the wire are the CSIL field names verbatim.
import { encode, decode } from "cbor-x";
import type { ServiceTransport } from "./gen/client.gen";

export class CsilError extends Error {
  constructor(public readonly code: number, message: string) {
    super(message);
    this.name = "CsilError";
  }
}

export interface CborHttpTransportOptions {
  /** Extra headers (e.g. Authorization). */
  headers?: Record<string, string>;
  /** Override fetch (tests / non-browser runtimes). */
  fetch?: typeof fetch;
}

export class CborHttpTransport implements ServiceTransport {
  private readonly baseUrl: string;
  private readonly opts: CborHttpTransportOptions;

  constructor(baseUrl: string, opts: CborHttpTransportOptions = {}) {
    this.baseUrl = baseUrl.replace(/\/$/, "");
    this.opts = opts;
  }

  async call<TReq, TRes>(
    service: string,
    method: string,
    req: TReq,
    opts?: { signal?: AbortSignal },
  ): Promise<TRes> {
    const doFetch = this.opts.fetch ?? fetch;
    const res = await doFetch(`${this.baseUrl}/v1alpha1/${service}/${method}`, {
      method: "POST",
      headers: {
        "content-type": "application/cbor",
        accept: "application/cbor",
        ...this.opts.headers,
      },
      body: encode(req),
      signal: opts?.signal,
    });

    const buf = new Uint8Array(await res.arrayBuffer());
    if (!res.ok) {
      // Error responses carry a CBOR ServiceError { code, message }.
      try {
        const err = decode(buf) as { code: number; message: string };
        throw new CsilError(err.code, err.message);
      } catch (e) {
        if (e instanceof CsilError) throw e;
        throw new CsilError(res.status, `corndogs request failed: ${res.status}`);
      }
    }
    return decode(buf) as TRes;
  }
}
