/* Corndogs C client transport: CSIL-RPC over a persistent TCP connection.
 *
 * This is Corndogs' own, ready-to-use carrier -- you do not need to write one.
 * Point it at a corndogs server's TCP address, wire it into the generated seam,
 * and call the typed csil_corndogs_* functions from client.gen.h:
 *
 *     corndogs_transport_t *t = corndogs_transport_connect("localhost:5080", 0);
 *     corndogs_transport_start_heartbeat(t, 15.0);   // keep the connection alive
 *     CsilgenTransport seam = corndogs_transport_seam(t);
 *
 *     SubmitTaskRequest req = { .queue = "emails", .current_state = "submitted", ... };
 *     SubmitTaskResponse resp;
 *     CsilCodecArena *owner = NULL;
 *     if (csil_corndogs_submit_task(&seam, &req, &resp, &owner) != 0)
 *         fprintf(stderr, "submit_task failed: %s\n", corndogs_transport_last_error(t));
 *     csil_codec_arena_free(owner);
 *
 * The wire is CSIL-RPC framed with the canonical 4-byte big-endian length prefix
 * over TCP. This carrier owns only the envelope + framing + connection; the
 * generated codec (codec.gen.h) owns (de)serialization -- transport.c reuses its
 * generic CBOR builder/reader (csilc_w_... / csilc_decode) rather than
 * hand-rolling one. Dependency-free: POSIX sockets + pthreads only.
 *
 * One connection, one call in flight at a time (serialized by an internal
 * mutex); the connection re-dials lazily on failure.
 */
#ifndef CORNDOGS_TRANSPORT_H
#define CORNDOGS_TRANSPORT_H

#include <stddef.h>
#include <stdint.h>

#include "client.gen.h" /* CsilgenTransport + the typed csil_corndogs_* call sites */

#ifdef __cplusplus
extern "C" {
#endif

/* Opaque carrier: one persistent TCP connection, dialed lazily and re-dialed on
 * failure. Safe for concurrent use from multiple threads (calls serialize on an
 * internal mutex). */
typedef struct corndogs_transport corndogs_transport_t;

/* Opaque stop handle for a background heartbeat thread. */
typedef struct corndogs_heartbeat corndogs_heartbeat_t;

/* corndogs_transport_connect allocates a transport for addr ("host:port"). The
 * TCP connection itself is dialed lazily on the first call (or explicitly via
 * corndogs_transport_ping). connect_timeout_seconds <= 0 uses the 5s default.
 * Returns NULL on allocation failure or a malformed address. */
corndogs_transport_t *corndogs_transport_connect(const char *addr, double connect_timeout_seconds);

/* corndogs_transport_seam returns a CsilgenTransport bound to t, ready to pass
 * to any generated csil_corndogs_* call (client.gen.h). */
CsilgenTransport corndogs_transport_seam(corndogs_transport_t *t);

/* corndogs_transport_last_error returns the message from the most recently
 * failed call on t ("" if none yet, or after a success). Valid until the next
 * call on t. */
const char *corndogs_transport_last_error(const corndogs_transport_t *t);

/* corndogs_transport_close stops any background heartbeat, closes the
 * connection, and frees t. */
void corndogs_transport_close(corndogs_transport_t *t);

/* --- heartbeat: one-shot, sync, and background forms ---------------------- */

/* corndogs_transport_ping sends a single control-plane heartbeat
 * (CorndogsService/$ping). Returns 0 if the server replied, non-zero if it is
 * unreachable (see corndogs_transport_last_error). Cheap; keeps an idle
 * connection alive and detects a dead server. */
int corndogs_transport_ping(corndogs_transport_t *t);

/* corndogs_transport_run_heartbeat is the SYNCHRONOUS heartbeat: it blocks the
 * calling thread, pinging every interval_seconds (<=0 uses the 15s default),
 * until a ping fails (returns non-zero) or *stop becomes non-zero (returns 0).
 * Pass NULL for stop to run until a ping fails. Run it on a thread you own, or
 * use corndogs_transport_start_heartbeat for the background form. */
int corndogs_transport_run_heartbeat(corndogs_transport_t *t, double interval_seconds,
                                      const volatile int *stop);

/* corndogs_transport_start_heartbeat is the ASYNCHRONOUS heartbeat: it starts a
 * background pthread pinging every interval_seconds and returns a stop handle.
 * Only one background heartbeat runs per transport; calling it again while one
 * is already running just returns a handle for that same heartbeat. Stop it
 * with corndogs_heartbeat_stop (also stopped implicitly by
 * corndogs_transport_close). Returns NULL on failure to start the thread. */
corndogs_heartbeat_t *corndogs_transport_start_heartbeat(corndogs_transport_t *t, double interval_seconds);

/* corndogs_heartbeat_stop stops the background heartbeat, joins its thread,
 * and frees hb. Safe to call exactly once per handle returned by
 * corndogs_transport_start_heartbeat. */
void corndogs_heartbeat_stop(corndogs_heartbeat_t *hb);

#ifdef __cplusplus
}
#endif

#endif /* CORNDOGS_TRANSPORT_H */
