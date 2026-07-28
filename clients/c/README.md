# corndogs (C client)

The official C client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — connect and go. Dependency-free: C11 + POSIX sockets +
pthreads only, no CBOR library, no libcurl.

## Consume

C has no package manager, so vendor this directory into your project and put it
on your include path. A single translation unit that includes `client.gen.h`
pulls in the generated types and self-contained CBOR codec; `transport.h` adds
the connection carrier. Build `transport.c` alongside your own sources:

```sh
cc -std=c11 -I. main.c transport.c -pthread -o demo
```

## Usage

```c
#include "client.gen.h"
#include "transport.h"
#include <stdio.h>

int main(void) {
    corndogs_transport_t *tr = corndogs_transport_connect("localhost:5080", 0); // your corndogs server's TCP address
    corndogs_transport_start_heartbeat(tr, 15.0);            // keep the connection alive (background thread)
    CsilgenTransport client = corndogs_transport_seam(tr);   // hand this to any csil_corndogs_* call

    // Submit a task.
    SubmitTaskRequest sub = {
        .queue = "emails", .current_state = "submitted", .auto_target_state = "sending",
        .timeout = -1, .payload = {0}, .priority = 0,
    };
    SubmitTaskResponse sub_resp;
    CsilCodecArena *owner = NULL;
    if (csil_corndogs_submit_task(&client, &sub, &sub_resp, &owner) != 0) {
        fprintf(stderr, "submit_task failed: %s\n", corndogs_transport_last_error(tr));
        return 1;
    }
    csil_codec_arena_free(owner); // frees everything sub_resp borrows

    // Claim the next one off the queue.
    GetNextTaskRequest next = {
        .queue = "emails", .current_state = "submitted",
        .override_timeout = 0, .override_current_state = "", .override_auto_target_state = "",
    };
    GetNextTaskResponse next_resp;
    owner = NULL;
    if (csil_corndogs_get_next_task(&client, &next, &next_resp, &owner) != 0) {
        fprintf(stderr, "get_next_task failed: %s\n", corndogs_transport_last_error(tr));
        return 1;
    }
    printf("claimed task %s\n", next_resp.delivery->task.uuid);
    csil_codec_arena_free(owner);

    corndogs_transport_close(tr); // stops the heartbeat and closes the connection
    return 0;
}
```

Every generated call (`csil_corndogs_submit_task`, `csil_corndogs_get_next_task`,
`csil_corndogs_update_task`, `csil_corndogs_complete_task`, ...) takes the same
`CsilgenTransport *` from `corndogs_transport_seam`, so one transport drives your
whole client. `corndogs_transport_t` is thread-safe: calls from multiple threads
serialize on an internal mutex rather than racing.

## Heartbeat (keep the connection alive)

A one-shot ping, a synchronous (blocking) loop, and a background-thread loop are
all provided:

```c
corndogs_transport_ping(tr);                                   // one-shot: 0 on success

int stop = 0;
corndogs_transport_run_heartbeat(tr, 15.0, &stop);              // sync: blocks, pinging until *stop is set (or a ping fails)

corndogs_heartbeat_t *hb = corndogs_transport_start_heartbeat(tr, 15.0); // async: background pthread
corndogs_heartbeat_stop(hb);                                    // stops it (also stopped implicitly by corndogs_transport_close)
```

## Clustered deployments

Point the client at any node; a write that lands on a follower is transparently
redirected to the leader.

## Notes

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte big-endian length
  prefix. HTTP is not used for RPC — the corndogs server serves RPC on its TCP
  port; HTTP is only for health and Prometheus.
- **Concurrency:** `corndogs_transport_t` serializes calls on the connection
  (one request in flight at a time, guarded by a `pthread_mutex_t`) and re-dials
  lazily on failure — simple and dependency-free.
- **CBOR:** the envelope is built and parsed with the generated codec's own
  generic CBOR primitives (`codec.gen.h`'s `csilc_w_*`/`csilc_decode`), not a
  separate hand-written encoder.
- **Memory:** every `csil_corndogs_*` call fills in a `CsilCodecArena **owner`;
  free it with `csil_codec_arena_free` once you're done reading the response —
  that single call frees every string/bytes/array the response borrows.
- Errors: a service error and a transport failure both surface as a non-zero
  return from the `csil_corndogs_*` call — read the message with
  `corndogs_transport_last_error(tr)`.
