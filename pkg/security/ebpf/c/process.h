#ifndef _PROCESS_H_
#define _PROCESS_H_

#include <linux/tty.h>
#include <linux/sched.h>

#include "container.h"
#include "span.h"

struct proc_cache_t {
    struct container_context_t container;
    struct file_t executable;

    u64 exec_timestamp;
    char tty_name[TTY_NAME_LEN];
    char comm[TASK_COMM_LEN];
};

static __attribute__((always_inline)) u32 copy_tty_name(char src[TTY_NAME_LEN], char dst[TTY_NAME_LEN]) {
    if (src[0] == 0) {
        return 0;
    }

#pragma unroll
    for (int i = 0; i < TTY_NAME_LEN; i++)
    {
        dst[i] = src[i];
    }
    return TTY_NAME_LEN;
}

void __attribute__((always_inline)) copy_proc_cache_except_comm(struct proc_cache_t* src, struct proc_cache_t* dst) {
    copy_container_id(src->container.container_id, dst->container.container_id);
    dst->executable = src->executable;
    dst->exec_timestamp = src->exec_timestamp;
    copy_tty_name(src->tty_name, dst->tty_name);
}

void __attribute__((always_inline)) copy_proc_cache(struct proc_cache_t *src, struct proc_cache_t *dst) {
    copy_proc_cache_except_comm(src, dst);
    bpf_probe_read(dst->comm, TASK_COMM_LEN, src->comm);
    return;
}

struct bpf_map_def SEC("maps/proc_cache") proc_cache = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u32),
    .value_size = sizeof(struct proc_cache_t),
    .max_entries = 4096,
    .pinning = 0,
    .namespace = "",
};

static void __attribute__((always_inline)) fill_container_context(struct proc_cache_t *entry, struct container_context_t *context) {
    if (entry) {
        copy_container_id(entry->container.container_id, context->container_id);
    }
}

struct credentials_t {
    u32 uid;
    u32 gid;
    u32 euid;
    u32 egid;
    u32 fsuid;
    u32 fsgid;
    u64 cap_effective;
    u64 cap_permitted;
};

void __attribute__((always_inline)) copy_credentials(struct credentials_t* src, struct credentials_t* dst) {
    *dst = *src;
}

struct pid_cache_t {
    u32 cookie;
    u32 ppid;
    u64 fork_timestamp;
    u64 exit_timestamp;
    struct credentials_t credentials;
};

void __attribute__((always_inline)) copy_pid_cache_except_exit_ts(struct pid_cache_t* src, struct pid_cache_t* dst) {
    dst->cookie = src->cookie;
    dst->ppid = src->ppid;
    dst->fork_timestamp = src->fork_timestamp;
    dst->credentials = src->credentials;
}

struct bpf_map_def SEC("maps/pid_cache") pid_cache = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u32),
    .value_size = sizeof(struct pid_cache_t),
    .max_entries = 4096,
    .pinning = 0,
    .namespace = "",
};

// defined in exec.h
struct proc_cache_t *get_proc_from_cookie(u32 cookie);

struct proc_cache_t * __attribute__((always_inline)) get_proc_cache(u32 tgid) {
    struct proc_cache_t *entry = NULL;

    struct pid_cache_t *pid_entry = (struct pid_cache_t *) bpf_map_lookup_elem(&pid_cache, &tgid);
    if (pid_entry) {
        // Select the cache entry
        u32 cookie = pid_entry->cookie;
        entry = get_proc_from_cookie(cookie);
    }
    return entry;
}

struct bpf_map_def SEC("maps/netns_cache") netns_cache = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u32),
    .value_size = sizeof(u32),
    .max_entries = 40960,
    .pinning = 0,
    .namespace = "",
};

static struct proc_cache_t * __attribute__((always_inline)) fill_process_context(struct process_context_t *data) {
    // Pid & Tid
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = pid_tgid >> 32;

    // https://github.com/iovisor/bcc/blob/master/docs/reference_guide.md#4-bpf_get_current_pid_tgid
    data->pid = tgid;
    data->tid = pid_tgid;

    u32 *netns = bpf_map_lookup_elem(&netns_cache, &data->tid);
    if (netns != NULL) {
        data->netns = *netns;
    }

    return get_proc_cache(tgid);
}

__attribute__((always_inline)) u32 get_netns_from_net(struct net *net) {
    struct ns_common ns;
    // TODO: add variable offset
    bpf_probe_read(&ns, sizeof(ns), &net->ns);
    return ns.inum;
}

__attribute__((always_inline)) u32 get_netns_from_sock(struct sock *sk) {
    struct sock_common *common = (void *)sk;
    struct net *net = NULL;
    // TODO: add variable offset
    bpf_probe_read(&net, sizeof(net), &common->skc_net);
    return get_netns_from_net(net);
}

__attribute__((always_inline)) u32 get_netns_from_socket(struct socket *socket) {
    struct sock *sk = NULL;
    // TODO: add variable offset
    bpf_probe_read(&sk, sizeof(sk), &socket->sk);
    return get_netns_from_sock(sk);
}

struct namespace_switch_event_t {
    struct kevent_t event;
    struct process_context_t process;
    struct span_context_t span;
    struct container_context_t container;
    struct syscall_t syscall;
};

SEC("kprobe/switch_task_namespaces")
int kprobe_switch_task_namespaces(struct pt_regs *ctx) {
    struct nsproxy *new_ns = (struct nsproxy *)PT_REGS_PARM2(ctx);
    if (new_ns == NULL) {
        return 0;
    }

    struct net *net;
    bpf_probe_read(&net, sizeof(net), &new_ns->net_ns);
    if (net == NULL) {
        return 0;
    }

    u32 netns = get_netns_from_net(net);
    u32 tid = bpf_get_current_pid_tgid();
    bpf_map_update_elem(&netns_cache, &tid, &netns, BPF_ANY);

    struct namespace_switch_event_t evt = {};
    struct proc_cache_t *entry = fill_process_context(&evt.process);
    fill_container_context(entry, &evt.container);
    fill_span_context(&evt.span);

    send_event(ctx, EVENT_NAMESPACE_SWITCH, evt);
    return 0;
}

#endif
