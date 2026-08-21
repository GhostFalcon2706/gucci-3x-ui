// Package shaper enforces per-client bandwidth tiers with the Linux traffic
// control subsystem (tc / HTB).
//
// Xray-core has no per-user rate limiter, so a real speed cap has to be applied
// by the kernel on the server that carries the traffic. The shaper builds one
// HTB class per speed level on the egress interface and steers each online
// client's IP into the class of its tier; addresses of unlimited clients are
// simply never classified and keep the full line rate.
//
// It requires CAP_NET_ADMIN and the iproute2 tooling. On hosts that do not
// grant them — most notably managed container platforms such as Railway, Fly or
// Heroku, where the network namespace is not yours to configure — Detect
// reports the exact reason and the panel surfaces it in the client editor
// instead of pretending a limit is in force.
package shaper

import (
	"bufio"
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capability describes whether this host can shape traffic.
type Capability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Interface string `json:"interface,omitempty"`
}

const (
	// rootHandle is the qdisc handle the shaper owns. Nothing else on the host
	// is touched: if a root qdisc already exists and is not ours, the shaper
	// refuses to run rather than replacing the operator's own shaping.
	rootHandle = "1:"
	// classBase is added to the speed level to build the class minor id, so
	// level 1 becomes 1:11, level 2 becomes 1:12 and so on.
	classBase = 10
	// linkRate is the ceiling of the root class — effectively "unlimited".
	linkRate = "10gbit"
	// commandTimeout bounds every tc invocation.
	commandTimeout = 5 * time.Second
)

var (
	mu        sync.Mutex
	installed bool
	lastPlan  string
	lastIface string
)

// capNetAdmin is bit 12 of the effective capability mask.
const capNetAdmin = 12

func hasNetAdmin() bool {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		hex := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
		mask, err := strconv.ParseUint(hex, 16, 64)
		if err != nil {
			return false
		}
		return mask&(1<<capNetAdmin) != 0
	}
	return false
}

// DefaultInterface returns the interface carrying the default route.
func DefaultInterface() (string, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "Iface" {
			continue
		}
		// Destination 00000000 marks the default route.
		if fields[1] == "00000000" {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("shaper: no default route found")
}

// Detect reports whether per-client speed limits can be enforced here.
func Detect() Capability {
	if runtime.GOOS != "linux" {
		return Capability{Reason: "traffic shaping needs Linux traffic control (tc); this host runs " + runtime.GOOS}
	}
	if _, err := exec.LookPath("tc"); err != nil {
		return Capability{Reason: "the tc binary (iproute2 package) is not installed"}
	}
	if !hasNetAdmin() {
		return Capability{Reason: "the panel process lacks CAP_NET_ADMIN, so it cannot program the kernel's traffic control (typical for managed container platforms such as Railway)"}
	}
	iface, err := DefaultInterface()
	if err != nil {
		return Capability{Reason: "no default-route interface to attach the shaper to"}
	}
	return Capability{Available: true, Interface: iface}
}

func run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tc", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tc %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func rateArg(mbps float64) string {
	if mbps >= 1 {
		return strconv.FormatFloat(mbps, 'f', -1, 64) + "mbit"
	}
	return strconv.FormatFloat(mbps*1000, 'f', -1, 64) + "kbit"
}

// Install creates (or re-creates) the root qdisc and one class per speed level.
// ladder[i] is the ceiling in Mbit/s for level i; index 0 is ignored because
// level 0 means unlimited.
func Install(iface string, ladder []float64) error {
	mu.Lock()
	defer mu.Unlock()
	return installLocked(iface, ladder)
}

func installLocked(iface string, ladder []float64) error {
	if installed && lastIface == iface {
		return nil
	}
	existing, _ := run("qdisc", "show", "dev", iface)
	if strings.Contains(existing, "qdisc htb 1:") {
		// Ours (or an identical layout) — start from a clean slate.
		_, _ = run("qdisc", "del", "dev", iface, "root")
	} else if strings.Contains(existing, "qdisc") && !strings.Contains(existing, "noqueue") &&
		!strings.Contains(existing, "pfifo_fast") && !strings.Contains(existing, "mq ") &&
		!strings.Contains(existing, "fq_codel") {
		return fmt.Errorf("shaper: %s already has a custom root qdisc, refusing to replace it", iface)
	} else {
		_, _ = run("qdisc", "del", "dev", iface, "root")
	}

	if _, err := run("qdisc", "add", "dev", iface, "root", "handle", "1:", "htb", "default", "1"); err != nil {
		return err
	}
	if _, err := run("class", "add", "dev", iface, "parent", "1:", "classid", "1:1", "htb", "rate", linkRate, "ceil", linkRate); err != nil {
		return err
	}
	for level := 1; level < len(ladder); level++ {
		mbps := ladder[level]
		if mbps <= 0 {
			continue
		}
		classID := fmt.Sprintf("1:%d", classBase+level)
		if _, err := run("class", "add", "dev", iface, "parent", "1:1", "classid", classID,
			"htb", "rate", rateArg(mbps), "ceil", rateArg(mbps), "burst", "15k"); err != nil {
			return err
		}
		// Fair queueing inside the tier so one greedy connection cannot starve
		// the client's other flows.
		_, _ = run("qdisc", "add", "dev", iface, "parent", classID, "handle", fmt.Sprintf("%d:", classBase+level), "fq_codel")
	}
	installed = true
	lastIface = iface
	lastPlan = ""
	return nil
}

// Plan maps a client address to its speed level. Level 0 entries are ignored.
type Plan map[netip.Addr]int

func planKey(p Plan) string {
	keys := make([]string, 0, len(p))
	for addr, level := range p {
		if level <= 0 {
			continue
		}
		keys = append(keys, addr.String()+"="+strconv.Itoa(level))
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// Apply reconciles the kernel filters with the desired plan. It is a no-op when
// nothing changed since the previous call, so it is safe to run on a tick.
func Apply(iface string, ladder []float64, plan Plan) error {
	mu.Lock()
	defer mu.Unlock()

	key := planKey(plan)
	if installed && lastIface == iface && key == lastPlan {
		return nil
	}
	if err := installLocked(iface, ladder); err != nil {
		return err
	}

	// Filters are cheap to rebuild and rebuilding avoids having to diff kernel
	// state; the traffic already in flight keeps its class until the new filter
	// set is in place a few milliseconds later.
	_, _ = run("filter", "del", "dev", iface, "parent", "1:")

	addrs := make([]netip.Addr, 0, len(plan))
	for addr := range plan {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Less(addrs[j]) })

	var firstErr error
	for _, addr := range addrs {
		level := plan[addr]
		if level <= 0 || level >= len(ladder) || ladder[level] <= 0 {
			continue
		}
		proto := "ip"
		matchField := "ip dst"
		if addr.Is6() {
			proto = "ipv6"
			matchField = "ip6 dst"
		}
		classID := fmt.Sprintf("1:%d", classBase+level)
		// Egress from the server towards the client is the "download" the user
		// perceives, so the destination address is the client's.
		args := []string{
			"filter", "add", "dev", iface, "protocol", proto, "parent", "1:", "prio", "1",
			"u32", "match",
		}
		args = append(args, strings.Fields(matchField)...)
		args = append(args, addr.String()+"/"+maxBits(addr), "flowid", classID)
		if _, err := run(args...); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	lastPlan = key
	return nil
}

func maxBits(addr netip.Addr) string {
	if addr.Is6() {
		return "128"
	}
	return "32"
}

// Teardown removes everything the shaper installed. Used when the feature is
// switched off so the host is left exactly as it was found.
func Teardown(iface string) {
	mu.Lock()
	defer mu.Unlock()
	if !installed {
		return
	}
	_, _ = run("qdisc", "del", "dev", iface, "root")
	installed = false
	lastPlan = ""
	lastIface = ""
}
