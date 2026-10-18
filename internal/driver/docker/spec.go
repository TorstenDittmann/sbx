package docker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

// These structs follow the Docker Engine API (PascalCase JSON). NanoCpus is
// the spelling the daemon accepts.

type createRequest struct {
	Image      string            `json:"Image"`
	User       string            `json:"User"`
	Env        []string          `json:"Env,omitempty"`
	Entrypoint []string          `json:"Entrypoint"`
	Cmd        []string          `json:"Cmd,omitempty"`
	WorkingDir string            `json:"WorkingDir"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	NetworkMode    string            `json:"NetworkMode"`
	PidMode        string            `json:"PidMode,omitempty"`
	CapDrop        []string          `json:"CapDrop"`
	CapAdd         []string          `json:"CapAdd"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Privileged     bool              `json:"Privileged"`
	Init           bool              `json:"Init"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCpus       int64             `json:"NanoCpus"`
	PidsLimit      int64             `json:"PidsLimit"`
	Runtime        string            `json:"Runtime,omitempty"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	LogConfig      logConfig         `json:"LogConfig"`
	IpcMode        string            `json:"IpcMode"`
}

type logConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type execConfig struct {
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	AttachStdin  bool     `json:"AttachStdin"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
	User         string   `json:"User,omitempty"`
}

func buildCreate(spec driver.SandboxSpec, opt Options, imageEnv []string) createRequest {
	workdir := spec.Workdir
	if workdir == "" {
		workdir = "/workspace"
	}
	env := map[string]string{}
	for _, item := range imageEnv {
		k, v, ok := strings.Cut(item, "=")
		if ok && k != "" {
			env[k] = v
		}
	}
	if env["PATH"] == "" {
		env["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env["HOME"] = workdir
	for k, v := range spec.Env {
		env[k] = v
	}
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[labelManaged] = "true"
	labels[labelID] = spec.ID
	labels[labelBackend] = opt.Name
	disk := spec.DiskMB
	if disk < 16 {
		disk = 16
	}
	tmp := 64
	if disk < tmp {
		tmp = disk
	}
	network := "none"
	if spec.Network == driver.NetFull {
		network = sandboxNetwork
	}
	return createRequest{
		Image:      spec.Image,
		User:       fmt.Sprintf("%d:%d", opt.UID, opt.GID),
		Env:        envSlice(env),
		Entrypoint: []string{"sleep", "infinity"},
		WorkingDir: workdir,
		Labels:     labels,
		HostConfig: hostConfig{
			NetworkMode:    network,
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{},
			SecurityOpt:    []string{"no-new-privileges:true"},
			ReadonlyRootfs: opt.ReadOnlyRootfs,
			Privileged:     false,
			Init:           true,
			Memory:         int64(spec.MemoryMB) * 1024 * 1024,
			MemorySwap:     int64(spec.MemoryMB) * 1024 * 1024,
			NanoCpus:       int64(spec.CPU * 1e9),
			PidsLimit:      int64(spec.PIDs),
			Runtime:        opt.Runtime,
			Tmpfs: map[string]string{
				"/workspace": fmt.Sprintf("rw,nosuid,nodev,size=%dm,uid=%d,gid=%d,mode=755", disk, opt.UID, opt.GID),
				"/tmp":       fmt.Sprintf("rw,nosuid,nodev,size=%dm,uid=%d,gid=%d,mode=1777", tmp, opt.UID, opt.GID),
			},
			LogConfig: logConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "1"}},
			IpcMode:   "private",
		},
	}
}

func envSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// withTimeout runs cmd in the foreground of a fifo so stdin still reaches it.
// Backgrounding the command would point its stdin at /dev/null (job control is
// off), which made file uploads empty. The command is placed in its own
// session when setsid exists, and the watchdog signals only that process
// group. A kill of pid 0 or 1 would hit the sandbox entrypoint (every process
// shares the container's group), which stopped the container on timeout.
func withTimeout(cmd []string, seconds int) []string {
	if seconds <= 0 {
		out := make([]string, len(cmd))
		copy(out, cmd)
		return out
	}
	const script = `secs=$1; shift
case $secs in
  ''|*[!0-9]*|0) exit 96 ;;
esac
fifo=/tmp/sbx-fifo-$$
timerfifo=/tmp/sbx-timer-$$
mkfifo "$fifo" || exit 97
mkfifo "$timerfifo" || exit 97
if command -v setsid >/dev/null 2>&1; then
  sh -c 'exec setsid "$@"' sh "$@" <"$fifo" &
  group=1
else
  "$@" <"$fifo" &
  group=0
fi
pid=$!
case $pid in
  ''|*[!0-9]*|0|1) exit 99 ;;
esac
if [ "$group" -eq 1 ]; then
  target=-$pid
else
  target=$pid
fi
(
  sh -c 'echo $$ >"$1"; sleep "$2"; kill -KILL "$3"' sh "$timerfifo" "$secs" "$target" >/dev/null 2>&1 &
)
read timer <"$timerfifo" || exit 98
case $timer in
  ''|*[!0-9]*|0|1) exit 99 ;;
esac
cat >"$fifo"
wait "$pid"
status=$?
kill -KILL "$timer" 2>/dev/null
rm -f "$fifo" "$timerfifo"
exit $status
`
	out := make([]string, 0, len(cmd)+5)
	out = append(out, "sh", "-c", script, "sh", strconv.Itoa(seconds))
	out = append(out, cmd...)
	return out
}

func splitRef(ref string) (from, tag string) {
	if strings.Contains(ref, "@") {
		return ref, ""
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}
