/* Corndogs C client transport implementation: see transport.h for the public
 * surface and usage. This file owns dialing, the 4-byte length-prefix framing,
 * the CSIL-RPC envelope, and the heartbeat threads.
 *
 * The feature-test macro exposes getaddrinfo/socket under a strict `-std=c11`;
 * it must precede the first system header, so it leads the file.
 */
#define _POSIX_C_SOURCE 200112L

#include "transport.h"

#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>

/* Reuse the generated package's generic CBOR builder (csilc_w_...) and reader
 * (csilc_decode / csilc_map_get / csilc_as_...) rather than hand-rolling a CBOR
 * encoder: codec.gen.h already exposes a full, self-contained canonical CBOR
 * codec for exactly this purpose. */
#include "codec.gen.h"

#define CORNDOGS_TAG_ENCODED_CBOR 24u    /* RFC 8949 tag 24: embedded encoded CBOR */
#define CORNDOGS_MAX_FRAME (1025u << 20)  /* payload maximum plus envelope allowance */
#define CORNDOGS_DEFAULT_DIAL_TIMEOUT 5.0
#define CORNDOGS_DEFAULT_HB_INTERVAL 15.0
#define CORNDOGS_CONTROL_SERVICE "CorndogsService"
#define CORNDOGS_OP_PING "$ping" /* control-plane heartbeat op; never collides with app ops */

struct corndogs_transport {
    char *host;
    char *port;
    double connect_timeout;

    pthread_mutex_t mu;    /* guards fd, next_id, last_error; serializes calls (one in flight) */
    int fd;                /* -1 when not connected */
    uint64_t next_id;
    char last_error[256];

    pthread_mutex_t hb_mu; /* guards hb_active/hb_stop/hb_thread */
    int hb_active;
    volatile int hb_stop;
    pthread_t hb_thread;
};

struct corndogs_heartbeat {
    corndogs_transport_t *t;
};

/* ---- small helpers --------------------------------------------------------- */

static void corndogs_set_error(corndogs_transport_t *t, const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(t->last_error, sizeof t->last_error, fmt, ap);
    va_end(ap);
}

/* addr is "host:port"; split on the last ':' (good enough for hostnames/IPv4;
 * bracketed IPv6 literals are not handled, matching the other language
 * carriers). */
static int split_addr(const char *addr, char **host, char **port) {
    const char *colon = strrchr(addr, ':');
    if (!colon || colon == addr || colon[1] == '\0') return -1;
    size_t hlen = (size_t)(colon - addr);
    char *h = (char *)malloc(hlen + 1);
    if (!h) return -1;
    memcpy(h, addr, hlen);
    h[hlen] = '\0';
    char *p = (char *)malloc(strlen(colon + 1) + 1);
    if (!p) { free(h); return -1; }
    strcpy(p, colon + 1);
    *host = h;
    *port = p;
    return 0;
}

/* dial connects a fresh socket to t->host:t->port with a connect timeout, sets
 * TCP_NODELAY, and stores it in t->fd. Caller holds t->mu. */
static int dial(corndogs_transport_t *t) {
    struct addrinfo hints, *ai, *rp;
    memset(&hints, 0, sizeof hints);
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;
    int gai = getaddrinfo(t->host, t->port, &hints, &ai);
    if (gai != 0) {
        corndogs_set_error(t, "corndogs: resolve %s:%s: %s", t->host, t->port, gai_strerror(gai));
        return -1;
    }

    int fd = -1;
    for (rp = ai; rp; rp = rp->ai_next) {
        fd = socket(rp->ai_family, rp->ai_socktype, rp->ai_protocol);
        if (fd < 0) continue;

        int flags = fcntl(fd, F_GETFL, 0);
        fcntl(fd, F_SETFL, flags | O_NONBLOCK);

        int rc = connect(fd, rp->ai_addr, rp->ai_addrlen);
        if (rc == 0) {
            fcntl(fd, F_SETFL, flags);
            break;
        }
        if (errno == EINPROGRESS) {
            double tmo = t->connect_timeout > 0 ? t->connect_timeout : CORNDOGS_DEFAULT_DIAL_TIMEOUT;
            struct timeval tv;
            tv.tv_sec = (time_t)tmo;
            tv.tv_usec = (suseconds_t)((tmo - (double)tv.tv_sec) * 1e6);
            fd_set wfds;
            FD_ZERO(&wfds);
            FD_SET(fd, &wfds);
            int sel = select(fd + 1, NULL, &wfds, NULL, &tv);
            int soerr = 0;
            socklen_t slen = sizeof soerr;
            if (sel > 0 && getsockopt(fd, SOL_SOCKET, SO_ERROR, &soerr, &slen) == 0 && soerr == 0) {
                fcntl(fd, F_SETFL, flags);
                break;
            }
        }
        close(fd);
        fd = -1;
    }
    freeaddrinfo(ai);

    if (fd < 0) {
        corndogs_set_error(t, "corndogs: connect %s:%s failed", t->host, t->port);
        return -1;
    }
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    t->fd = fd;
    return 0;
}

static int ensure_conn_locked(corndogs_transport_t *t) {
    if (t->fd >= 0) return 0;
    return dial(t);
}

/* teardown_locked closes the current connection (if any) so the next call
 * re-dials. Caller holds t->mu. */
static void teardown_locked(corndogs_transport_t *t) {
    if (t->fd >= 0) {
        close(t->fd);
        t->fd = -1;
    }
}

static int write_full(int fd, const uint8_t *buf, size_t n) {
    size_t off = 0;
    while (off < n) {
        ssize_t w = write(fd, buf + off, n - off);
        if (w < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (w == 0) return -1;
        off += (size_t)w;
    }
    return 0;
}

static int read_full(int fd, uint8_t *buf, size_t n) {
    size_t off = 0;
    while (off < n) {
        ssize_t r = read(fd, buf + off, n - off);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (r == 0) return -1; /* peer closed */
        off += (size_t)r;
    }
    return 0;
}

/* --- framing: 4-byte big-endian length prefix + payload, the canonical CSIL
 * StreamCarrier framing (identical to the Go and Python carriers). --- */

static int write_frame(int fd, const uint8_t *b, size_t n) {
    if (n > CORNDOGS_MAX_FRAME) return -1;
    uint8_t prefix[4] = {
        (uint8_t)(n >> 24), (uint8_t)(n >> 16), (uint8_t)(n >> 8), (uint8_t)n
    };
    if (write_full(fd, prefix, 4)) return -1;
    return write_full(fd, b, n);
}

static int read_frame(int fd, uint8_t **out, size_t *out_len) {
    uint8_t prefix[4];
    if (read_full(fd, prefix, 4)) return -1;
    uint32_t n = ((uint32_t)prefix[0] << 24) | ((uint32_t)prefix[1] << 16) |
                 ((uint32_t)prefix[2] << 8) | (uint32_t)prefix[3];
    if (n > CORNDOGS_MAX_FRAME) return -1;
    uint8_t *buf = (uint8_t *)malloc(n ? n : 1);
    if (!buf) return -1;
    if (n && read_full(fd, buf, n)) {
        free(buf);
        return -1;
    }
    *out = buf;
    *out_len = n;
    return 0;
}

/* ---- the CsilgenTransport seam --------------------------------------------- */

/* corndogs_transport_call implements the CsilgenTransport byte seam
 * (client.gen.h): build the request envelope (CBOR map { v, service, op, id,
 * payload:tag24(req) }) with the generated codec's generic CBOR builder, send
 * one frame, wait for the correlated reply, and hand back the inner payload
 * bytes. One call in flight per transport at a time. */
static int corndogs_transport_call(void *self, const char *service, const char *op,
                                    const uint8_t *req, size_t req_len,
                                    uint8_t **resp, size_t *resp_len) {
    corndogs_transport_t *t = (corndogs_transport_t *)self;
    *resp = NULL;
    *resp_len = 0;

    pthread_mutex_lock(&t->mu);
    t->last_error[0] = '\0';

    if (ensure_conn_locked(t)) {
        pthread_mutex_unlock(&t->mu);
        return -1;
    }

    uint64_t id = ++t->next_id;

    csilc_buf env;
    csilc_buf_init(&env);
    int enc_err =
        csilc_w_map_head(&env, 5) ||
        csilc_w_text(&env, "v", 1) || csilc_w_uint(&env, 1) ||
        csilc_w_text(&env, "service", 7) || csilc_w_text(&env, service, strlen(service)) ||
        csilc_w_text(&env, "op", 2) || csilc_w_text(&env, op, strlen(op)) ||
        csilc_w_text(&env, "id", 2) || csilc_w_uint(&env, id) ||
        csilc_w_text(&env, "payload", 7) || csilc_w_tag(&env, CORNDOGS_TAG_ENCODED_CBOR) ||
        csilc_w_bytes(&env, req, req_len);
    if (enc_err) {
        csilc_buf_dispose(&env);
        corndogs_set_error(t, "corndogs: encode request envelope failed");
        pthread_mutex_unlock(&t->mu);
        return -1;
    }

    if (write_frame(t->fd, env.data, env.len)) {
        csilc_buf_dispose(&env);
        corndogs_set_error(t, "corndogs: write failed: %s", strerror(errno));
        teardown_locked(t);
        pthread_mutex_unlock(&t->mu);
        return -1;
    }
    csilc_buf_dispose(&env);

    uint8_t *frame = NULL;
    size_t frame_len = 0;
    if (read_frame(t->fd, &frame, &frame_len)) {
        corndogs_set_error(t, "corndogs: connection closed");
        teardown_locked(t);
        pthread_mutex_unlock(&t->mu);
        return -1;
    }

    CsilCodecArena *arena = NULL;
    const csilc_value *root = NULL;
    if (csilc_decode(frame, frame_len, &arena, &root) || !root) {
        free(frame);
        corndogs_set_error(t, "corndogs: decode response envelope failed");
        pthread_mutex_unlock(&t->mu);
        return -1;
    }
    free(frame);

    /* A non-zero transport status is a transport-level failure (no payload). */
    int64_t status = 0;
    csilc_as_i64(csilc_map_get(root, "status"), &status);
    if (status != 0) {
        char *msg = NULL;
        if (!csilc_get_text(csilc_map_get(root, "error"), &msg)) msg = "";
        corndogs_set_error(t, "corndogs: transport status %lld: %s", (long long)status, msg);
        csil_codec_arena_free(arena);
        pthread_mutex_unlock(&t->mu);
        return -1;
    }

    const csilc_value *pv = csilc_map_get(root, "payload");
    if (!pv) {
        /* control replies (e.g. $pong) may carry no payload */
        csil_codec_arena_free(arena);
        pthread_mutex_unlock(&t->mu);
        return 0;
    }
    if (pv->kind == CSILC_TAG) pv = pv->as.tag.content;
    uint8_t *inner = NULL;
    size_t inner_len = 0;
    if (!csilc_get_bytes(pv, &inner, &inner_len)) {
        corndogs_set_error(t, "corndogs: response payload is not a byte string");
        csil_codec_arena_free(arena);
        pthread_mutex_unlock(&t->mu);
        return -1;
    }

    /* A typed application error rides as a status-0 "ServiceError" variant. */
    char *variant = NULL;
    if (csilc_get_text(csilc_map_get(root, "variant"), &variant) &&
        strcmp(variant, "ServiceError") == 0) {
        ServiceError se;
        CsilCodecArena *seo = NULL;
        if (csil_decode_ServiceError(inner, inner_len, &se, &seo) == 0) {
            corndogs_set_error(t, "corndogs: service error %llu: %s",
                                (unsigned long long)se.code, se.message ? se.message : "");
            csil_codec_arena_free(seo);
        } else {
            corndogs_set_error(t, "corndogs: undecodable ServiceError payload");
        }
        csil_codec_arena_free(arena);
        pthread_mutex_unlock(&t->mu);
        return 1;
    }

    uint8_t *out = (uint8_t *)malloc(inner_len ? inner_len : 1);
    if (!out) {
        corndogs_set_error(t, "corndogs: out of memory");
        csil_codec_arena_free(arena);
        pthread_mutex_unlock(&t->mu);
        return -1;
    }
    if (inner_len) memcpy(out, inner, inner_len);
    *resp = out;
    *resp_len = inner_len;

    csil_codec_arena_free(arena);
    pthread_mutex_unlock(&t->mu);
    return 0;
}

/* ---- public lifecycle ------------------------------------------------------ */

corndogs_transport_t *corndogs_transport_connect(const char *addr, double connect_timeout_seconds) {
    if (!addr) return NULL;
    char *host = NULL, *port = NULL;
    if (split_addr(addr, &host, &port)) return NULL;

    corndogs_transport_t *t = (corndogs_transport_t *)calloc(1, sizeof *t);
    if (!t) {
        free(host);
        free(port);
        return NULL;
    }
    t->host = host;
    t->port = port;
    t->connect_timeout = connect_timeout_seconds > 0 ? connect_timeout_seconds : CORNDOGS_DEFAULT_DIAL_TIMEOUT;
    t->fd = -1;
    t->next_id = 0;
    pthread_mutex_init(&t->mu, NULL);
    pthread_mutex_init(&t->hb_mu, NULL);
    return t;
}

CsilgenTransport corndogs_transport_seam(corndogs_transport_t *t) {
    CsilgenTransport seam;
    seam.call = corndogs_transport_call;
    seam.self = t;
    return seam;
}

const char *corndogs_transport_last_error(const corndogs_transport_t *t) {
    return t ? t->last_error : "";
}

/* stop_heartbeat signals the background heartbeat (if any) to stop and joins
 * it. Shared by corndogs_heartbeat_stop and corndogs_transport_close. */
static void stop_heartbeat(corndogs_transport_t *t) {
    pthread_mutex_lock(&t->hb_mu);
    int active = t->hb_active;
    pthread_t th = t->hb_thread;
    if (active) t->hb_stop = 1;
    pthread_mutex_unlock(&t->hb_mu);
    if (!active) return;
    pthread_join(th, NULL);
    pthread_mutex_lock(&t->hb_mu);
    t->hb_active = 0;
    pthread_mutex_unlock(&t->hb_mu);
}

void corndogs_transport_close(corndogs_transport_t *t) {
    if (!t) return;
    stop_heartbeat(t);

    pthread_mutex_lock(&t->mu);
    teardown_locked(t);
    pthread_mutex_unlock(&t->mu);

    pthread_mutex_destroy(&t->mu);
    pthread_mutex_destroy(&t->hb_mu);
    free(t->host);
    free(t->port);
    free(t);
}

/* ---- heartbeat -------------------------------------------------------------- */

int corndogs_transport_ping(corndogs_transport_t *t) {
    uint8_t *resp = NULL;
    size_t resp_len = 0;
    int rc = corndogs_transport_call(t, CORNDOGS_CONTROL_SERVICE, CORNDOGS_OP_PING, NULL, 0, &resp, &resp_len);
    free(resp);
    return rc;
}

int corndogs_transport_run_heartbeat(corndogs_transport_t *t, double interval_seconds,
                                      const volatile int *stop) {
    double interval = interval_seconds > 0 ? interval_seconds : CORNDOGS_DEFAULT_HB_INTERVAL;
    const double poll = 0.1; /* seconds; how often to notice `stop` while waiting */
    for (;;) {
        double waited = 0.0;
        while (waited < interval) {
            if (stop && *stop) return 0;
            double chunk = (interval - waited) < poll ? (interval - waited) : poll;
            struct timespec ts;
            ts.tv_sec = (time_t)chunk;
            ts.tv_nsec = (long)((chunk - (double)ts.tv_sec) * 1e9);
            nanosleep(&ts, NULL);
            waited += chunk;
        }
        if (stop && *stop) return 0;
        if (corndogs_transport_ping(t)) return -1;
    }
}

struct hb_args {
    corndogs_transport_t *t;
    double interval;
};

static void *hb_thread_main(void *arg) {
    struct hb_args *a = (struct hb_args *)arg;
    corndogs_transport_run_heartbeat(a->t, a->interval, &a->t->hb_stop);
    free(a);
    return NULL;
}

corndogs_heartbeat_t *corndogs_transport_start_heartbeat(corndogs_transport_t *t, double interval_seconds) {
    if (!t) return NULL;

    pthread_mutex_lock(&t->hb_mu);
    if (t->hb_active) {
        pthread_mutex_unlock(&t->hb_mu);
        corndogs_heartbeat_t *hb = (corndogs_heartbeat_t *)malloc(sizeof *hb);
        if (hb) hb->t = t;
        return hb;
    }

    struct hb_args *a = (struct hb_args *)malloc(sizeof *a);
    if (!a) {
        pthread_mutex_unlock(&t->hb_mu);
        return NULL;
    }
    a->t = t;
    a->interval = interval_seconds;
    t->hb_stop = 0;
    if (pthread_create(&t->hb_thread, NULL, hb_thread_main, a)) {
        free(a);
        pthread_mutex_unlock(&t->hb_mu);
        return NULL;
    }
    t->hb_active = 1;
    pthread_mutex_unlock(&t->hb_mu);

    corndogs_heartbeat_t *hb = (corndogs_heartbeat_t *)malloc(sizeof *hb);
    if (hb) hb->t = t;
    return hb;
}

void corndogs_heartbeat_stop(corndogs_heartbeat_t *hb) {
    if (!hb) return;
    corndogs_transport_t *t = hb->t;
    free(hb);
    if (!t) return;
    stop_heartbeat(t);
}
