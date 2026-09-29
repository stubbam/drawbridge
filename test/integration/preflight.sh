#!/usr/bin/env bash
# Checks that this machine can run the kernel integration tests, and names everything
# that's missing at once (docs/MANUAL_CHECKLIST.md §4). `make test-integration` and CI
# run it before the tests.
set -u

missing=()
need() { missing+=("$1"); }

as_root=()
root=yes
if [ "$(id -u)" != 0 ]; then
	if sudo -n true 2>/dev/null; then
		as_root=(sudo -n)
	else
		root=no
		need "root, or passwordless sudo"
	fi
fi

echo "kernel: $(uname -srm)"
grep '^PRETTY_NAME=' /etc/os-release 2>/dev/null
id

for tool in ip nft sh; do
	if path=$("${as_root[@]}" sh -c "command -v $tool" 2>/dev/null); then
		echo "$tool: $path"
	else
		echo "$tool: missing"
		case $tool in
		ip) need "the ip command (install iproute2)" ;;
		nft) need "the nft command (install nftables)" ;;
		*) need "the $tool command" ;;
		esac
	fi
done

# Effective capabilities of the process that will run the tests. A container started
# without --privileged has neither of these, even as root.
caps=$("${as_root[@]}" sh -c 'grep CapEff /proc/self/status' 2>/dev/null | awk '{print $2}')
echo "CapEff: ${caps:-unknown}"
if [ -n "$caps" ]; then
	caps=$((16#$caps))
	[ $((caps >> 12 & 1)) = 1 ] || need "CAP_NET_ADMIN (run the runner container with --privileged)"
	[ $((caps >> 21 & 1)) = 1 ] || need "CAP_SYS_ADMIN, to create network namespaces (run the runner container with --privileged)"
fi

if [ -d /proc/sys/net/ipv6 ]; then echo "IPv6: yes"; else
	echo "IPv6: no"
	need "IPv6 in the kernel"
fi

# The real test: a throwaway network namespace, with what the tests build in theirs. Module
# names aren't checked, because many are built into others (nft_rt and nft_exthdr are part of
# nf_tables). A container can't load modules, so the host must have loaded them.
ruleset='table inet dbpreflight {
	set allowed { type ipv4_addr; flags interval; elements = { 10.0.0.0/8 } }
	chain input { type filter hook input priority filter; tcp dport 51821 ip saddr != @allowed drop; }
	chain mss { type filter hook forward priority mangle; oifname "wg0" tcp flags syn tcp option maxseg size set rt mtu; }
	chain post { type nat hook postrouting priority srcnat; ip saddr 10.8.0.0/24 masquerade; ip6 saddr fd00::/64 masquerade; }
}'
probe() { # probe <what> <what's needed> <command...>
	local what=$1 needed=$2 out
	shift 2
	if out=$("${as_root[@]}" "$@" 2>&1); then echo "$what: ok"; else
		echo "$what: ${out:-failed}"
		need "$needed"
	fi
}
if [ $root = yes ] && "${as_root[@]}" sh -c "command -v ip" >/dev/null; then
	ns="dbpreflight$$"
	if out=$("${as_root[@]}" ip netns add "$ns" 2>&1); then
		echo "network namespaces: ok"
		probe "WireGuard interfaces" "kernel WireGuard (on the host: sudo modprobe wireguard)" \
			ip -n "$ns" link add wgpreflight type wireguard
		probe "veth pairs" "veth pairs (on the host: sudo modprobe veth)" \
			ip -n "$ns" link add vpreflight0 type veth peer name vpreflight1
		probe "nftables (sets, NAT, MSS clamping)" \
			"nftables NAT (on the host: sudo modprobe -a nf_tables nft_chain_nat nft_masq)" \
			ip netns exec "$ns" nft -f - <<<"$ruleset"
		"${as_root[@]}" ip netns del "$ns"
	else
		echo "network namespaces: $out"
		need "permission to create network namespaces (run the runner container with --privileged)"
	fi
fi

if [ ${#missing[@]} = 0 ]; then
	echo "preflight: ok"
	exit 0
fi
echo
echo "This machine can't run the integration tests. It needs:"
for m in "${missing[@]}"; do
	echo "  - $m"
	[ -n "${GITHUB_ACTIONS:-}" ] && echo "::error::The runner needs $m"
done
echo "See docs/MANUAL_CHECKLIST.md §4."
exit 1
