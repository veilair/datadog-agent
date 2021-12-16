#ifndef __TLS_H
#define __TLS_H

#include "tracer.h"
#include "tls-types.h"
#include "tls-maps.h"
#include "ip.h"
#include "http.h"

#include <uapi/linux/ptrace.h>

// TLS classification generate an HTTPStats event
// that will include the tag and the latency of the connection
//
// Latency connection = time from the 1st packet (SYN) to the 1st payload packet (TLS_APP)
static __always_inline void http_enqueue_with_tags(skb_info_t *skb_info, u64 tags, u64 started, u64 classified) {
    http_transaction_t new_entry = { 0 };
    bpf_map_update_elem(&http_in_flight, &skb_info->tup, &new_entry, BPF_NOEXIST);
    http_transaction_t *http = bpf_map_lookup_elem(&http_in_flight, &skb_info->tup);
    if (!http) {
        return;
    }
    http->request_started = started;
    http->response_last_seen = classified;
    http->response_status_code = 200;
    http->tags |= tags;
    http_enqueue(http, &skb_info->tup);
}

static __always_inline void tls_cleanup(skb_info_t *skb_info) {
    bpf_map_delete_elem(&tls_in_flight, &skb_info->tup);
}

static __always_inline int isTLS(tls_header_t *hdr, struct __sk_buff* skb, u32 offset) {
    if (skb->len - offset < TLS_HEADER_SIZE) {
        return 0;
    }
    __u8 app = load_byte(skb, offset);
    if ((app != TLS_HANDSHAKE) &&
        (app != TLS_APPLICATION_DATA)) {
            return 0;
    }
    hdr->app = app;
    
    __u16 version = load_half(skb, offset + 1);
    if ((version != SSL_VERSION20) &&
        (version != SSL_VERSION30) &&
        (version != TLS_VERSION10) &&
        (version != TLS_VERSION11) &&
        (version != TLS_VERSION12) &&
        (version != TLS_VERSION13)) {
            return 0;
    }
    hdr->version = version;
    
    __u16 length = load_half(skb, offset + 3);
    hdr->length = length;
    __u16 skblen = skb->len - offset - TLS_HEADER_SIZE;
    if (skblen < length) {
        return 0;
    }
    if (skblen == length) {
        return 1;
    }
    return 1;
}

/*
   proto_tls() parsing TLS packets until
    o we see TLS_APPLICATION_DATA packets
    o TLS_MAX_PACKET_CLASSIFIER is reached
 */
SEC("socket/proto_tls")
int socket__proto_tls(struct __sk_buff* skb) {
    skb_info_t skb_info;
    if (!read_conn_tuple_skb(skb, &skb_info)) {
        return 0;
    }
    if (skb->len - skb_info.data_off == 0) {
        return 0;
    }

    if (!is_ephemeral_port(skb_info.tup.sport)) {
        flip_tuple(&skb_info.tup);
    }
    tls_session_t *tls = NULL;
    tls_session_t new_entry = { 0 };
    bpf_map_update_elem(&tls_in_flight, &skb_info.tup, &new_entry, BPF_NOEXIST);
    tls = bpf_map_lookup_elem(&tls_in_flight, &skb_info.tup);
    if (tls == NULL) {
        return 0;
    }
    if (tls->packets == 0) {
        tls->started = bpf_ktime_get_ns();
    }
    /* cnx classified or not */
    if (tls->packets > TLS_MAX_PACKET_CLASSIFIER) {
        return 0;
    }
    if (tls->isTLS == 1 && tls->handshake_done == 1) {
        return 0;
    }
    tls->packets++;

    /* check packet content */
    tls_header_t tls_hdr;
    if (!isTLS(&tls_hdr, skb, skb_info.data_off)) {
        return 0;
    }

    /* we got TLS */
    if (tls_hdr.app == TLS_APPLICATION_DATA) {
        tls->handshake_done = 1;
        tls->classified = bpf_ktime_get_ns();
        http_enqueue_with_tags(&skb_info, TLS, tls->started, tls->classified);
    }
    __builtin_memcpy(&tls->tup, &skb_info.tup, sizeof(conn_tuple_t));
    tls->isTLS = 1;

    return 0;
}

#endif
