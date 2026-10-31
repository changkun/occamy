// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/backend/vnc/vnctest"
	"changkun.de/x/occamy/internal/config"
)

// TestDemoV1File checks that the demo stack's v1 file loads, with the
// backends that its connections compare (tests/parity_test.go): ssh
// on the native SSH backend, vnc-native and rdp-native on their
// protocols' default, native, and every other connection on the backend
// it names. It also checks the image's web client, and that its user's
// password is the one docker/occamy-v1.yaml and tests/ give.
func TestDemoV1File(t *testing.T) {
	raw, err := os.ReadFile("../../docker/occamy-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// What the compose service gives it: a tmpfs for the signing key,
	// the tigervnc service's CA, and the SSH, VNC and RDP passwords.
	ca, err := vnctest.SelfSigned("tigervnc")
	if err != nil {
		t.Fatal(err)
	}
	caDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(caDir, "ca.pem"), ca.PEM, 0o600); err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.NewReplacer("/run/occamy/", t.TempDir()+"/", "/run/occamy-vnc-ca/", caDir+"/").Replace(string(raw)))
	t.Setenv("OCCAMY_DEMO_SSH_PASSWORD", "occamy")
	t.Setenv("OCCAMY_DEMO_VNC_PASSWORD", "vncpassword")
	t.Setenv("OCCAMY_DEMO_PLAIN_PASSWORD", "plainpassword")
	t.Setenv("OCCAMY_DEMO_RDP_PASSWORD", "occamy")
	f, err := config.ParseV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]string{}
	for _, c := range f.Connections {
		backends[c.ID] = c.Backend
	}
	// Every connection but ssh, vnc-native and rdp-native names its
	// backend, so that the comparison does not change with a protocol's
	// default; vnc-native and rdp-native check the defaults, and ssh has
	// only the native backend.
	want := map[string]string{
		"ssh":        config.BackendNative,
		"rdp-native": config.BackendNative, "rdp-guacd": config.BackendGuacd,
	}
	for _, id := range []string{"vnc", "vnc-none", "vnc-de", "vnc-fr", "tigervnc", "tigervnc-prompt", "tigervnc-plain", "tigervnc-none", "tigervnc-de", "tigervnc-fr"} {
		want[id+"-native"] = config.BackendNative
		want[id+"-guacd"] = config.BackendGuacd
	}
	if !maps.Equal(backends, want) {
		t.Errorf("backends %v, want %v", backends, want)
	}
	if n := strings.Count(string(raw), "\n    backend: "); n != len(want)-3 {
		t.Errorf("%d connections name their backend, want %d: all but ssh, vnc-native and rdp-native", n, len(want)-3)
	}
	// The image's build of the web client, where the Dockerfile puts it.
	if f.WebClient != "client/occamy-web/dist" {
		t.Errorf("web_client %q, want the image's client/occamy-web/dist", f.WebClient)
	}
	if len(f.Auth.Users) != 1 || bcrypt.CompareHashAndPassword([]byte(f.Auth.Users[0].PasswordHash), []byte("occamy")) != nil {
		t.Errorf("users %+v, want demo with the password occamy", f.Auth.Users)
	}
	// The debug listener is off but where make run OCCAMY_DEBUG=1 sets
	// OCCAMY_V1_DEBUG_ADDRESS (docker-compose.yaml passes it on, empty
	// by default).
	for _, address := range []string{"", "0.0.0.0:5638"} {
		t.Setenv("OCCAMY_V1_DEBUG_ADDRESS", address)
		f, err := config.ParseV1(raw)
		if err != nil {
			t.Fatal(err)
		}
		if f.Debug.Address != address {
			t.Errorf("with OCCAMY_V1_DEBUG_ADDRESS=%q, the debug listener is on %q", address, f.Debug.Address)
		}
	}
}

// TestDemoV0Files checks the demo stack's v0 files: occamy.yaml, in
// release mode, which serves no pprof, and occamy-debug.yaml, which make
// run OCCAMY_DEBUG=1 mounts in its place for the pressure test, the same
// but in debug mode.
func TestDemoV0Files(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile("../../docker/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	release, debug := read("occamy.yaml"), read("occamy-debug.yaml")
	for _, tt := range []struct {
		name, raw string
		debug     bool
	}{
		{"occamy.yaml", release, false},
		{"occamy-debug.yaml", debug, true},
	} {
		c, err := config.Parse([]byte(tt.raw))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if c.Debug() != tt.debug {
			t.Errorf("%s: debug mode %v, want %v", tt.name, c.Debug(), tt.debug)
		}
	}
	// The settings, past the comments on top, but for the mode.
	settings := func(raw string) string {
		_, body, _ := strings.Cut(raw, "\n---\n")
		var lines []string
		for line := range strings.SplitSeq(body, "\n") {
			if !strings.HasPrefix(line, "mode:") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	if settings(release) != settings(debug) {
		t.Errorf("occamy-debug.yaml differs from occamy.yaml in more than the mode:\n%s\n---\n%s", settings(release), settings(debug))
	}
}

// TestDemoDebugGating checks that the demo stack's debug routes, which
// have no authentication, are on only after make run OCCAMY_DEBUG=1:
// off with compose up and no variables, and off with make run from a
// shell that holds the variables that would turn them on, as a leftover
// export or docker/.env would. It reads docker-compose.yaml as compose
// does, with the variables that make run passes it, and checks that the
// ports of both routes are published on the loopback interface only.
func TestDemoDebugGating(t *testing.T) {
	raw, err := os.ReadFile("../../docker/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Ports       []string          `yaml:"ports"`
			Volumes     []string          `yaml:"volumes"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	v0, v1 := compose.Services["occamy"], compose.Services["occamy-v1"]
	// The variables that turn the routes on, as make run OCCAMY_DEBUG=1
	// sets them.
	dirty := map[string]string{"OCCAMY_V0_CONF": "occamy-debug.yaml", "OCCAMY_V1_DEBUG_ADDRESS": "0.0.0.0:5638"}
	for _, tt := range []struct {
		name string
		env  func(t *testing.T) map[string]string
		on   bool
	}{
		{"compose up", func(*testing.T) map[string]string { return nil }, false},
		{"make run from a shell that would turn them on", func(t *testing.T) map[string]string { return makeRunEnv(t, "", dirty) }, false},
		{"make run OCCAMY_DEBUG=1", func(t *testing.T) map[string]string { return makeRunEnv(t, "1", nil) }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := tt.env(t)
			// The v0 service's file, in debug mode or in release mode.
			var conf string
			for _, v := range v0.Volumes {
				host, target, _ := strings.Cut(composeInterpolate(t, v, env), ":")
				if strings.HasPrefix(target, "/occamy/conf.yaml") {
					conf = strings.TrimPrefix(host, "./")
				}
			}
			b, err := os.ReadFile("../../docker/" + conf)
			if err != nil {
				t.Fatalf("the v0 service's file: %v", err)
			}
			c, err := config.Parse(b)
			if err != nil {
				t.Fatalf("%s: %v", conf, err)
			}
			if c.Debug() != tt.on {
				t.Errorf("the v0 service runs %s, in debug mode: %v, want %v", conf, c.Debug(), tt.on)
			}
			// occamy-v1's debug listener, from the variable its v1 file
			// names (TestDemoV1File).
			address := composeInterpolate(t, v1.Environment["OCCAMY_V1_DEBUG_ADDRESS"], env)
			if (address != "") != tt.on {
				t.Errorf("occamy-v1's debug listener is on %q, want it on: %v", address, tt.on)
			}
			if tt.on && !strings.HasSuffix(address, ":5638") {
				t.Errorf("occamy-v1's debug listener is on %q, not on the port published for it, 5638", address)
			}
		})
	}
	// Both routes are published on the loopback interface only: all of
	// the v0 service, which serves pprof on its own port, and occamy-v1's
	// debug port.
	for _, p := range v0.Ports {
		if p := composeInterpolate(t, p, nil); !strings.HasPrefix(p, "127.0.0.1:") {
			t.Errorf("the v0 service is published on %q, beyond the loopback interface", p)
		}
	}
	published := false
	for _, p := range v1.Ports {
		p := composeInterpolate(t, p, nil)
		if strings.HasSuffix(p, ":5638") {
			published = true
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("occamy-v1's debug listener is published on %q, beyond the loopback interface", p)
			}
		}
	}
	if !published {
		t.Errorf("occamy-v1 publishes no port 5638 for its debug listener: %q", v1.Ports)
	}
}

// TestMakeClean checks that make clean removes Occamy's own containers
// and images and no other of the engine's, which other projects on the
// host may share: the demo stack through compose, and only the images
// that make build labels, which it labels.
func TestMakeClean(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make")
	}
	dryRun := func(target string) []string {
		out, err := exec.Command("make", "-s", "-n", "-C", "../..", target, "CONTAINER_ENGINE=engine").Output()
		if err != nil {
			t.Fatalf("make -n %s: %v", target, err)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	var label string
	for _, line := range dryRun("build") {
		if _, rest, ok := strings.Cut(line, " --label "); ok {
			label, _, _ = strings.Cut(rest, " ")
		}
	}
	if label == "" {
		t.Fatalf("make build labels no image: %q", dryRun("build"))
	}
	for _, line := range dryRun("clean") {
		fields := strings.Fields(line)
		switch {
		case strings.HasPrefix(line, "cd docker && engine compose down"):
			// The stack's own project.
		case len(fields) > 2 && fields[0] == "engine" && fields[1] == "image" && fields[2] == "prune":
			if !strings.Contains(line, "--filter label="+label) {
				t.Errorf("make clean prunes beyond the images that make build labels %s: %q", label, line)
			}
		default:
			t.Errorf("make clean runs %q, which this test does not know to remove only Occamy's own", line)
		}
	}
}

// TestCompiledBinaryIgnored checks that git ignores the occamyd that
// make compile leaves at the repository's root.
func TestCompiledBinaryIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	out, err := exec.Command("make", "-s", "-n", "-C", "../..", "compile").Output()
	if err != nil {
		t.Fatalf("make -n compile: %v", err)
	}
	if !strings.Contains(string(out), "-o occamyd") {
		t.Fatalf("make compile builds %q, not ./occamyd", out)
	}
	cmd := exec.Command("git", "check-ignore", "-q", "--no-index", "occamyd")
	cmd.Dir = "../.."
	switch err := cmd.Run(); {
	case err == nil:
	case cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1:
		t.Error("git does not ignore the occamyd that make compile builds")
	default:
		t.Skipf("git check-ignore: %v, not in a git checkout?", err)
	}
}

// makeRunEnv returns the variables that make run passes to compose with
// OCCAMY_DEBUG set to debug, from a shell with shell's variables, over
// them: the assignments before the compose command that make -n prints.
func makeRunEnv(t *testing.T, debug string, shell map[string]string) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make")
	}
	cmd := exec.Command("make", "-s", "-n", "-C", "../..", "run", "OCCAMY_DEBUG="+debug, "CONTAINER_ENGINE=engine")
	cmd.Env = os.Environ()
	for k, v := range shell {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("make -n run: %v", err)
	}
	env := maps.Clone(shell)
	if env == nil {
		env = map[string]string{}
	}
	_, line, ok := strings.Cut(string(out), "&& ")
	if !ok {
		t.Fatalf("make -n run printed %q, not cd docker && ... compose up", out)
	}
	for _, word := range strings.Fields(line) {
		k, v, ok := strings.Cut(word, "=")
		if !ok {
			if word != "engine" {
				t.Fatalf("make -n run printed %q, where the compose command should follow the variables", out)
			}
			break
		}
		env[k] = v
	}
	return env
}

// composeInterpolate replaces ${NAME} and ${NAME:-default} in s as
// compose does, from env, and fails the test on any other $.
func composeInterpolate(t *testing.T, s string, env map[string]string) string {
	t.Helper()
	out := composeVariable.ReplaceAllStringFunc(s, func(m string) string {
		sub := composeVariable.FindStringSubmatch(m)
		if v := env[sub[1]]; v != "" || sub[2] == "" {
			return v
		}
		return sub[3]
	})
	if strings.Contains(out, "$") {
		t.Fatalf("%q: a $ that the test does not read as compose does", s)
	}
	return out
}

var composeVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)
