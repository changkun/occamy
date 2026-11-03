#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# shape emulates a link on the client connections of the server whose
# network namespace it runs in, which needs NET_ADMIN there:
#
#   shape PORT DELAY_US RATE_KBIT LOSS_PERCENT LIMIT_PACKETS
#   shape clear
#
# Packets to clients, from PORT, wait DELAY_US, are serialised at
# RATE_KBIT (0 is unlimited) and dropped at random with LOSS_PERCENT;
# packets from clients, to PORT, wait DELAY_US and are serialised at
# RATE_KBIT, through an ifb device, and none are dropped. Each queue
# holds at most LIMIT_PACKETS, the delayed ones included. Packets to and
# from the stack's own network, such as a server's backends, pass
# untouched. Segmentation offload is off while it shapes, so that a
# loss drops one packet rather than a whole segment of them. Having
# shaped, it prints the namespace's TCP congestion control, which the
# server's connections use:
#
#   congestion cubic
#
# Every call clears what an earlier one set first, and clear only that.
set -eu

dev=eth0
ifb=ifbshape

clear_link() {
	tc qdisc del dev "$dev" root 2>/dev/null || true
	tc qdisc del dev "$dev" ingress 2>/dev/null || true
	ip link del "$ifb" 2>/dev/null || true
	ethtool -K "$dev" tso on gso on 2>/dev/null || true
}

clear_link
if [ "${1:-}" = clear ]; then
	exit 0
fi
if [ $# -ne 5 ]; then
	echo "usage: shape PORT DELAY_US RATE_KBIT LOSS_PERCENT LIMIT_PACKETS | shape clear" >&2
	exit 2
fi
port=$1 delay=$2 rate=$3 loss=$4 limit=$5

# The stack's own network: the address of dev with its prefix, which u32
# masks to the network.
local_net=$(ip -o -4 addr show dev "$dev" | awk '{ print $4; exit }')
if [ -z "$local_net" ]; then
	echo "shape: $dev has no IPv4 address" >&2
	exit 1
fi

link="delay ${delay}us limit $limit"
if [ "$rate" != 0 ]; then
	link="$link rate ${rate}kbit"
fi

ethtool -K "$dev" tso off gso off

# To clients: a prio qdisc whose every priority goes to band 1, and two
# filters, so that only packets from PORT to outside the stack's network
# reach band 3 and its netem.
tc qdisc add dev "$dev" root handle 1: prio bands 3 priomap 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0
# shellcheck disable=SC2086 # link is a list of netem's words
tc qdisc add dev "$dev" parent 1:3 handle 30: netem $link loss "${loss}%"
tc filter add dev "$dev" parent 1: prio 1 protocol ip u32 match ip dst "$local_net" flowid 1:1
tc filter add dev "$dev" parent 1: prio 2 protocol ip u32 match ip sport "$port" 0xffff flowid 1:3

# From clients: the same choice on ingress, redirected to an ifb device
# whose root is the other direction's netem, without loss.
ip link add "$ifb" type ifb
ip link set "$ifb" up
# shellcheck disable=SC2086
tc qdisc add dev "$ifb" root netem $link
tc qdisc add dev "$dev" handle ffff: ingress
tc filter add dev "$dev" parent ffff: prio 1 protocol ip u32 match ip src "$local_net" action pass
tc filter add dev "$dev" parent ffff: prio 2 protocol ip u32 match ip dport "$port" 0xffff \
	action mirred egress redirect dev "$ifb"

echo "congestion $(cat /proc/sys/net/ipv4/tcp_congestion_control)"
