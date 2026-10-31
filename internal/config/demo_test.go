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
	"slices"
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

// TestDemoBind checks where the demo stack publishes occamyd. occamy-v1
// is on the loopback interface, since the demo user's password is
// public, unless OCCAMY_BIND names another address, from the shell or
// make's command line. docker/.env cannot widen it under make run or
// make bench, which pass OCCAMY_BIND either way. The v0 service, whose
// login signs a token for any host with a public secret, and occamy-v1's
// debug port, with no authentication, stay on the loopback interface
// whatever OCCAMY_BIND and OCCAMY_DEBUG say.
func TestDemoBind(t *testing.T) {
	raw, err := os.ReadFile("../../docker/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	// published returns the host address of each container port that
	// occamy and occamy-v1 publish when compose reads env, over dotenv
	// as docker/.env: the process's variables win over the file's.
	published := func(t *testing.T, dotenv, env map[string]string) map[string]string {
		t.Helper()
		vars := maps.Clone(dotenv)
		if vars == nil {
			vars = map[string]string{}
		}
		maps.Copy(vars, env)
		got := map[string]string{}
		for _, name := range []string{"occamy", "occamy-v1"} {
			for _, p := range compose.Services[name].Ports {
				p := composeInterpolate(t, p, vars)
				parts := strings.Split(p, ":")
				if len(parts) != 3 {
					t.Fatalf("%s publishes %q, not host:port:port", name, p)
				}
				got[name+":"+parts[2]] = parts[0]
			}
		}
		return got
	}
	// want is where the ports are published with occamy-v1 on v1.
	want := func(v1 string) map[string]string {
		return map[string]string{"occamy:5636": "127.0.0.1", "occamy-v1:5637": v1, "occamy-v1:5638": "127.0.0.1"}
	}
	check := func(t *testing.T, dotenv, env map[string]string, v1 string) {
		t.Helper()
		if got := published(t, dotenv, env); !maps.Equal(got, want(v1)) {
			t.Errorf("published on %v, want %v", got, want(v1))
		}
	}

	t.Run("compose up", func(t *testing.T) {
		check(t, nil, nil, "127.0.0.1")
		// By hand, compose takes OCCAMY_BIND from docker/.env, and still
		// keeps the v0 service and the debug port on the loopback
		// interface.
		check(t, map[string]string{"OCCAMY_BIND": "0.0.0.0"}, nil, "0.0.0.0")
	})
	for _, target := range []string{"run", "bench"} {
		for _, debug := range []string{"", "1"} {
			name := "make " + target
			if debug != "" {
				name += " OCCAMY_DEBUG=" + debug
			}
			t.Run(name, func(t *testing.T) {
				args := []string{"OCCAMY_DEBUG=" + debug}
				check(t, nil, makeEnv(t, target, args, nil), "127.0.0.1")
				for _, addr := range []string{"0.0.0.0", "192.0.2.10"} {
					// docker/.env cannot widen it.
					check(t, map[string]string{"OCCAMY_BIND": addr}, makeEnv(t, target, args, nil), "127.0.0.1")
					// The shell and the command line can, for occamy-v1.
					check(t, nil, makeEnv(t, target, args, map[string]string{"OCCAMY_BIND": addr}), addr)
					check(t, nil, makeEnv(t, target, append(args, "OCCAMY_BIND="+addr), nil), addr)
				}
			})
		}
	}
}

// TestDemoProject checks that the demo stack's compose project is
// occamy, not the compose file's directory, docker, which other
// checkouts and other compose files share, and so make clean's compose
// down: the project that docker-compose.yaml names, which the bench
// stack keeps, and the one that make bench and the harness read CPU
// time from. COMPOSE_PROJECT_NAME still names another.
func TestDemoProject(t *testing.T) {
	for _, file := range []string{"docker-compose.yaml", "docker-compose.bench.yaml"} {
		raw, err := os.ReadFile("../../docker/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var compose struct {
			Name *string `yaml:"name"`
		}
		if err := yaml.Unmarshal(raw, &compose); err != nil {
			t.Fatal(err)
		}
		switch {
		case file == "docker-compose.yaml" && (compose.Name == nil || *compose.Name != "occamy"):
			t.Errorf("%s names the project %v, want occamy", file, compose.Name)
		case file != "docker-compose.yaml" && compose.Name != nil && *compose.Name != "occamy":
			t.Errorf("%s names the project %q, want occamy or none", file, *compose.Name)
		}
	}
	src, err := os.ReadFile("../../bench/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `flag.String("project", "occamy",`) {
		t.Error(`bench's -project does not default to occamy`)
	}
	for _, tt := range []struct{ shell, want string }{{"", "occamy"}, {"occamy-try", "occamy-try"}} {
		lines := makeDryRun(t, "bench", nil, map[string]string{"COMPOSE_PROJECT_NAME": tt.shell})
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, "go run ./bench ") {
				found = true
				if !strings.Contains(line, " -project "+tt.want+" ") {
					t.Errorf("with COMPOSE_PROJECT_NAME=%q, make bench runs %q, want -project %s", tt.shell, line, tt.want)
				}
			}
		}
		if !found {
			t.Errorf("make -n bench runs no harness: %q", lines)
		}
	}
}

// TestMakeClean checks that make clean removes Occamy's own containers
// and images and no other of the engine's, which other projects on the
// host may share: the demo stack's project through compose, and the
// untagged images that make build labels. It compares whole commands,
// so that a wider removal anywhere in them fails it.
func TestMakeClean(t *testing.T) {
	var label string
	for _, line := range makeDryRun(t, "build", nil, nil) {
		if _, rest, ok := strings.Cut(line, " --label "); ok {
			label, _, _ = strings.Cut(rest, " ")
		}
	}
	if label == "" {
		t.Fatalf("make build labels no image: %q", makeDryRun(t, "build", nil, nil))
	}
	want := []string{
		"cd docker && engine compose down --remove-orphans --volumes --rmi local",
		"engine image prune -f --filter label=" + label,
	}
	if got := makeDryRun(t, "clean", nil, nil); !slices.Equal(got, want) {
		t.Errorf("make clean runs\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestCompiledBinaryIgnored checks that git ignores the occamyd that
// make compile leaves at the repository's root.
func TestCompiledBinaryIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	out := strings.Join(makeDryRun(t, "compile", nil, nil), "\n")
	if !strings.Contains(out, "-o occamyd") {
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

// makeDryRun returns the commands that make -n target prints, with
// CONTAINER_ENGINE=engine and args on make's command line, from a shell
// with shell's variables. It skips the test without make.
func makeDryRun(t *testing.T, target string, args []string, shell map[string]string) []string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make")
	}
	cmd := exec.Command("make", append([]string{"-s", "-n", "-C", "../..", target, "CONTAINER_ENGINE=engine"}, args...)...)
	// The shell is shell's, not the test's, which may hold the variables
	// that the tests set.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "OCCAMY_BIND" && k != "OCCAMY_DEBUG" && k != "COMPOSE_PROJECT_NAME" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	for k, v := range shell {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("make -n %s %s: %v", target, strings.Join(args, " "), err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// makeEnv returns the variables that make target, with args on its
// command line, passes to compose from a shell with shell's variables,
// over them: the assignments before the compose command that make -n
// prints.
func makeEnv(t *testing.T, target string, args []string, shell map[string]string) map[string]string {
	t.Helper()
	lines := makeDryRun(t, target, args, shell)
	env := maps.Clone(shell)
	if env == nil {
		env = map[string]string{}
	}
	for _, line := range lines {
		rest, ok := strings.CutPrefix(line, "cd docker && ")
		if !ok {
			continue
		}
		for _, word := range strings.Fields(rest) {
			k, v, ok := strings.Cut(word, "=")
			if !ok {
				if word != "engine" {
					t.Fatalf("make -n %s printed %q, where the compose command should follow the variables", target, line)
				}
				break
			}
			env[k] = v
		}
		return env
	}
	t.Fatalf("make -n %s printed %q, no cd docker && ... compose", target, lines)
	return nil
}

// makeRunEnv returns the variables that make run passes to compose with
// OCCAMY_DEBUG set to debug, from a shell with shell's variables, over
// them.
func makeRunEnv(t *testing.T, debug string, shell map[string]string) map[string]string {
	t.Helper()
	return makeEnv(t, "run", []string{"OCCAMY_DEBUG=" + debug}, shell)
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
