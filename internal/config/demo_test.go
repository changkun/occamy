// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/backend/vnc/vnctest"
	"changkun.de/x/occamy/internal/config"
)

// TestDemoV1File checks that the demo stack's v1 file loads, with the
// connections that a first-time user sees, the real desktop first, and
// that the end-to-end tests' file, which make run E2E=1 serves in its
// place, is the same file with the connections that the tests open
// (tests/) after the demo's, and the limit the pressure test needs. It
// also checks the image's web client, and that its user's password is
// the one docker/occamy-v1.yaml and tests/ give.
func TestDemoV1File(t *testing.T) {
	files := newDemoV1Files(t)
	demo := files.load(t, "occamy-v1.yaml")
	e2e := files.load(t, "occamy-v1-e2e.yaml")
	// The real desktop comes first, for a first-time viewer, then the
	// SSH server.
	wantDemo := []string{"desktop-vnc", "desktop-rdp", "ssh"}
	if ids := connectionIDs(demo); !slices.Equal(ids, wantDemo) {
		t.Errorf("occamy-v1.yaml: connections %q, want %q", ids, wantDemo)
	}
	// The tests' file has the demo's, then each VNC server's security
	// types and keyboard layouts (tests/parity_test.go), and the RDP
	// server.
	wantE2E := append(slices.Clone(wantDemo),
		"vnc", "vnc-none", "tigervnc", "tigervnc-prompt", "tigervnc-plain", "tigervnc-none",
		"vnc-de", "vnc-fr", "tigervnc-de", "tigervnc-fr", "rdp",
	)
	if ids := connectionIDs(e2e); !slices.Equal(ids, wantE2E) {
		t.Errorf("occamy-v1-e2e.yaml: connections %q, want %q", ids, wantE2E)
	}
	// The pressure test opens 60 sessions at once as demo; the demo
	// keeps the default.
	if got := demo.Limits.SessionsPerUser; got != config.DefaultSessionsPerUser {
		t.Errorf("occamy-v1.yaml: sessions_per_user %d, want the default, %d", got, config.DefaultSessionsPerUser)
	}
	if got := e2e.Limits.SessionsPerUser; got != 100 {
		t.Errorf("occamy-v1-e2e.yaml: sessions_per_user %d, want 100 for the pressure test", got)
	}
	// Past its own connections and that limit, the tests' file is the
	// demo's, so that the tests run on what a user of the demo gets.
	for i, c := range demo.Connections {
		if i >= len(e2e.Connections) || !reflect.DeepEqual(e2e.Connections[i], c) {
			t.Errorf("occamy-v1-e2e.yaml's connection %d is not occamy-v1.yaml's %q", i, c.ID)
		}
	}
	rest := *e2e
	rest.Connections, rest.Limits = demo.Connections, demo.Limits
	if !reflect.DeepEqual(&rest, demo) {
		t.Errorf("occamy-v1-e2e.yaml's settings are not occamy-v1.yaml's:\n%+v\nwant\n%+v", rest, *demo)
	}
	for name, f := range map[string]*config.File{"occamy-v1.yaml": demo, "occamy-v1-e2e.yaml": e2e} {
		// The image's build of the web client, where the Dockerfile puts
		// it.
		if f.WebClient != "client/occamy-web/dist" {
			t.Errorf("%s: web_client %q, want the image's client/occamy-web/dist", name, f.WebClient)
		}
		if len(f.Auth.Users) != 1 || bcrypt.CompareHashAndPassword([]byte(f.Auth.Users[0].PasswordHash), []byte("occamy")) != nil {
			t.Errorf("%s: users %+v, want demo with the password occamy", name, f.Auth.Users)
		}
		// The debug listener is off but where make run OCCAMY_DEBUG=1
		// sets OCCAMY_V1_DEBUG_ADDRESS (docker-compose.yaml passes it
		// on, empty by default).
		for _, address := range []string{"", "0.0.0.0:5638"} {
			t.Setenv("OCCAMY_V1_DEBUG_ADDRESS", address)
			f := files.load(t, name)
			if f.Debug.Address != address {
				t.Errorf("%s: with OCCAMY_V1_DEBUG_ADDRESS=%q, the debug listener is on %q", name, address, f.Debug.Address)
			}
		}
	}
}

// demoV1Files loads the v1 files of docker/ with what the compose
// service gives occamyd: a volume for the signing key, the tigervnc
// service's CA, and the SSH, VNC and RDP passwords. The files share the
// volume and the CA, so that they compare equal.
type demoV1Files struct{ paths *strings.Replacer }

func newDemoV1Files(t *testing.T) demoV1Files {
	t.Helper()
	ca, err := vnctest.SelfSigned("tigervnc")
	if err != nil {
		t.Fatal(err)
	}
	caDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(caDir, "ca.pem"), ca.PEM, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCCAMY_DEMO_SSH_PASSWORD", "occamy")
	t.Setenv("OCCAMY_DEMO_VNC_PASSWORD", "vncpassword")
	t.Setenv("OCCAMY_DEMO_PLAIN_PASSWORD", "plainpassword")
	t.Setenv("OCCAMY_DEMO_RDP_PASSWORD", "occamy")
	return demoV1Files{strings.NewReplacer("/var/lib/occamy/", t.TempDir()+"/", "/run/occamy-vnc-ca/", caDir+"/")}
}

// load loads the file name, with OCCAMY_V1_DEBUG_ADDRESS as the test
// set it.
func (d demoV1Files) load(t *testing.T, name string) *config.File {
	t.Helper()
	raw, err := os.ReadFile("../../docker/" + name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := config.ParseV1([]byte(d.paths.Replace(string(raw))))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

// connectionIDs returns the IDs of f's connections, in its order.
func connectionIDs(f *config.File) []string {
	var ids []string
	for _, c := range f.Connections {
		ids = append(ids, c.ID)
	}
	return ids
}

// TestDemoDebugGating checks that the demo stack's debug listener,
// which has no authentication, is on only after make run
// OCCAMY_DEBUG=1: off with compose up and no variables, and off with
// make run from a shell that holds the variable that would turn it on,
// as a leftover export or docker/.env would. It reads docker-compose.yaml
// as compose does, with the variables that make run passes it, and
// checks that the listener's port is published on the loopback
// interface only.
func TestDemoDebugGating(t *testing.T) {
	raw, err := os.ReadFile("../../docker/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Ports       []string          `yaml:"ports"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	v1 := compose.Services["occamy-v1"]
	// The variable that turns the listener on, as make run OCCAMY_DEBUG=1
	// sets it.
	dirty := map[string]string{"OCCAMY_V1_DEBUG_ADDRESS": "0.0.0.0:5638"}
	for _, tt := range []struct {
		name string
		env  func(t *testing.T) map[string]string
		on   bool
	}{
		{"compose up", func(*testing.T) map[string]string { return nil }, false},
		{"make run from a shell that would turn it on", func(t *testing.T) map[string]string { return makeRunEnv(t, "", dirty) }, false},
		{"make run OCCAMY_DEBUG=1", func(t *testing.T) map[string]string { return makeRunEnv(t, "1", nil) }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := tt.env(t)
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
	// The stack publishes occamy-v1 alone.
	for name, svc := range compose.Services {
		if name != "occamy-v1" && len(svc.Ports) > 0 {
			t.Errorf("%s publishes %q", name, svc.Ports)
		}
	}
}

// TestDemoBind checks where the demo stack publishes occamyd. occamy-v1
// is on the loopback interface, since the demo user's password is
// public, unless OCCAMY_BIND names another address, from the shell or
// make's command line. docker/.env cannot widen it under make run or
// make bench, which pass OCCAMY_BIND either way. occamy-v1's debug port,
// with no authentication, stays on the loopback interface whatever
// OCCAMY_BIND and OCCAMY_DEBUG say.
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
	// occamy-v1 publishes when compose reads env, over dotenv as
	// docker/.env: the process's variables win over the file's.
	published := func(t *testing.T, dotenv, env map[string]string) map[string]string {
		t.Helper()
		vars := maps.Clone(dotenv)
		if vars == nil {
			vars = map[string]string{}
		}
		maps.Copy(vars, env)
		got := map[string]string{}
		for _, name := range []string{"occamy-v1"} {
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
		return map[string]string{"occamy-v1:5637": v1, "occamy-v1:5638": "127.0.0.1"}
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
		// keeps the debug port on the loopback interface.
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

// TestBenchRuntime checks that make bench keeps the v1 debug listeners
// off, and that make bench-compare turns them on and has the harness read
// occamyd's runtime from their ports while it collects the comparison's
// plan, resuming its results.
func TestBenchRuntime(t *testing.T) {
	if env := makeEnv(t, "bench", nil, nil); env["OCCAMY_V1_DEBUG_ADDRESS"] != "" {
		t.Errorf("make bench: OCCAMY_V1_DEBUG_ADDRESS=%q, want it off", env["OCCAMY_V1_DEBUG_ADDRESS"])
	}
	if env := makeEnv(t, "bench-compare", nil, nil); env["OCCAMY_V1_DEBUG_ADDRESS"] != "0.0.0.0:5638" {
		t.Errorf("make bench-compare: OCCAMY_V1_DEBUG_ADDRESS=%q, want 0.0.0.0:5638", env["OCCAMY_V1_DEBUG_ADDRESS"])
	}
	lines := makeDryRun(t, "bench-compare", []string{"OCCAMY_V1_DEBUG_PORT=6638", "OCCAMY_V1_CUBIC_DEBUG_PORT=6643"}, nil)
	// make -n prints the harness's command as the Makefile breaks it.
	joined := strings.ReplaceAll(strings.Join(lines, "\n"), "\\\n", " ")
	var harness string
	for _, line := range strings.Split(joined, "\n") {
		if strings.HasPrefix(line, "go run ./bench ") {
			harness = strings.Join(strings.Fields(line), " ")
		}
	}
	for _, want := range []string{
		"-runtime occamy-v1=localhost:6638,occamy-v1-cubic=localhost:6643",
		"-plan bench/plans/compare.yaml -resume -out bench-compare.json",
	} {
		if !strings.Contains(harness, want) {
			t.Errorf("make bench-compare runs %q, without %q", harness, want)
		}
	}
}

// TestDemoProject checks that the demo stack's compose project is
// occamy, not the compose file's directory, docker, which other
// checkouts and other compose files share, and so make clean's compose
// down: the project that docker-compose.yaml names, which the bench
// and end-to-end stacks keep, and the one that make bench and the
// harness read CPU time from. COMPOSE_PROJECT_NAME still names another.
func TestDemoProject(t *testing.T) {
	for _, file := range []string{"docker-compose.yaml", "docker-compose.bench.yaml", "docker-compose.e2e.yaml"} {
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

// TestMakeBuildImage checks the tags that make build gives: occamy's by
// default, and OCCAMY_IMAGE's name and tag, the image that the demo stack
// runs, so that a build for a stack of its own leaves occamy:latest
// alone on a shared host. IMAGE on the command line names another.
func TestMakeBuildImage(t *testing.T) {
	for _, tc := range []struct {
		image     string
		args      []string
		name, tag string
	}{
		{name: "occamy", tag: "latest"},
		{image: "occamy-try:latest", name: "occamy-try", tag: "latest"},
		{image: "occamy-try", name: "occamy-try", tag: "latest"},
		{image: "occamy-try:v2", name: "occamy-try", tag: "v2"},
		{image: "localhost:5000/team/occamy", name: "localhost:5000/team/occamy", tag: "latest"},
		{image: "localhost:5000/team/occamy:v2", name: "localhost:5000/team/occamy", tag: "v2"},
		{args: []string{"IMAGE=mine"}, name: "mine", tag: "latest"},
		{image: "occamy-try:latest", args: []string{"IMAGE=mine"}, name: "mine", tag: "latest"},
	} {
		shell := map[string]string{}
		if tc.image != "" {
			shell["OCCAMY_IMAGE"] = tc.image
		}
		lines := makeDryRun(t, "build", append([]string{"VERSION=v9"}, tc.args...), shell)
		var tags []string
		for _, m := range regexp.MustCompile(` -t (\S+)`).FindAllStringSubmatch(strings.Join(lines, "\n"), -1) {
			tags = append(tags, m[1])
		}
		if want := []string{tc.name + ":v9", tc.name + ":" + tc.tag}; !slices.Equal(tags, want) {
			t.Errorf("OCCAMY_IMAGE=%q, %v: make build tags %q, want %q", tc.image, tc.args, tags, want)
		}
	}
}

// TestDockerfileNode checks that every package of the web client, and
// the client itself, takes the Node that the image builds it with: npm
// only warns of one that does not (EBADENGINE), and vitest leaves out
// Node's odd majors.
func TestDockerfileNode(t *testing.T) {
	dockerfile, err := os.ReadFile("../../docker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^FROM node:(\d+)-`).FindSubmatch(dockerfile)
	if m == nil {
		t.Fatal("no node image in docker/Dockerfile")
	}
	major, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../client/occamy-web/package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages map[string]struct {
			Engines json.RawMessage `json:"engines"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	for name, pkg := range lock.Packages {
		var engines struct {
			Node string `json:"node"`
		}
		// Old packages may give engines as a list, which says nothing here.
		if json.Unmarshal(pkg.Engines, &engines) != nil || engines.Node == "" {
			continue
		}
		if !nodeTakes(engines.Node, major) {
			t.Errorf("%q needs Node %s, and docker/Dockerfile has %d", name, engines.Node, major)
		}
	}
	if lock.Packages["node_modules/vitest"].Engines == nil {
		t.Error("no vitest in the lock file: this test has lost what it checks")
	}
}

// nodeTakes reports whether a package's range of Node versions has a
// version of major, for the forms ^22.13.0, >=24 and 20. A range with
// any other form counts as taking it.
func nodeTakes(versions string, major int) bool {
	for alt := range strings.SplitSeq(versions, "||") {
		alt = strings.TrimSpace(alt)
		m := regexp.MustCompile(`^(\^|>=\s*)?(\d+)(\.\d+)*$`).FindStringSubmatch(alt)
		if m == nil {
			return true
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return true
		}
		if strings.HasPrefix(m[1], ">=") && major >= n || !strings.HasPrefix(m[1], ">=") && major == n {
			return true
		}
	}
	return false
}

// TestSigningKeyHome checks that the signing key lives where a real
// install keeps it, at one path everywhere: the image gives that
// directory to its nonroot user, or occamyd could not create the key on
// the volume, the demo stack mounts a named volume there on the service
// that uses it, and the files name the key in it. The image's own run is
// make e2e's.
func TestSigningKeyHome(t *testing.T) {
	const dir, key = "/var/lib/occamy", "/var/lib/occamy/signing.key"
	const volume = "occamy-v1-data"
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	_, final, ok := strings.Cut(read("docker/Dockerfile"), "FROM gcr.io/distroless/static-debian13:nonroot")
	if !ok {
		t.Fatal("docker/Dockerfile has no distroless stage")
	}
	// A whole line, so that a comment that quotes it does not count.
	if !slices.Contains(strings.Split(final, "\n"), "COPY --from=build --chown=65532:65532 "+dir+" "+dir) {
		t.Errorf("the runtime image does not give %s to nonroot", dir)
	}
	var compose struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal([]byte(read("docker/docker-compose.yaml")), &compose); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(compose.Services["occamy-v1"].Volumes, volume+":"+dir) {
		t.Errorf("occamy-v1 mounts no volume %s at %s: %v", volume, dir, compose.Services["occamy-v1"].Volumes)
	}
	if _, ok := compose.Volumes[volume]; !ok {
		t.Errorf("docker-compose.yaml does not declare the volume %s", volume)
	}
	for _, name := range []string{"docker/occamy-v1.yaml", "docker/occamy-v1-e2e.yaml", "docker/bench/occamy-v1.yaml", "docs/configuration.md"} {
		if !strings.Contains(read(name), "signing_key_file: "+key+"\n") {
			t.Errorf("%s does not keep the signing key at %s", name, key)
		}
	}
}

// TestConfigurationExampleRefused checks that the first example of
// docs/configuration.md cannot be served as it stands: its password hash
// is a placeholder, so that nobody starts a server with a hash out of
// the documentation.
func TestConfigurationExampleRefused(t *testing.T) {
	raw, err := os.ReadFile("../../docs/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(raw), "```yaml\n")
	if !ok {
		t.Fatal("docs/configuration.md has no YAML example")
	}
	example, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("docs/configuration.md's first example does not end")
	}
	_, err = config.ParseV1([]byte(example))
	var problems config.Problems
	if !errors.As(err, &problems) {
		t.Fatalf("ParseV1: %v, want its problems", err)
	}
	if !slices.ContainsFunc(problems, func(p config.Problem) bool { return p.Path == "auth.users[0].password_hash" }) {
		t.Errorf("the example's password hash was accepted: %v", err)
	}
}

// TestDemoUserStaysInTheDemo checks that nothing but the demo stack's,
// the end-to-end tests' and the benchmark stack's v1 files holds the
// demo user's password hash, and that the file in the image and the release archives has no
// users to sign in with: occamyd has no default user or password.
func TestDemoUserStaysInTheDemo(t *testing.T) {
	// Past the $ signs, which a shell's heredoc would escape.
	const hash = "DXEVjCzdy7mEP0VBuqhlqunMrnP3lSGh0/kl6yLwA5Rhrl4hLhjPu"
	allowed := map[string]bool{
		"docker/occamy-v1.yaml":             true,
		"docker/occamy-v1-e2e.yaml":         true,
		"docker/bench/occamy-v1.yaml":       true,
		"docker/bench/occamy-v1-cubic.yaml": true,
	}
	found := map[string]bool{}
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "node_modules", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		// This test is the one other file that has to say the hash.
		if !d.Type().IsRegular() || d.Name() == "demo_test.go" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(hash)) {
			name, err := filepath.Rel("../..", path)
			if err != nil {
				return err
			}
			found[filepath.ToSlash(name)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name := range found {
		if !allowed[name] {
			t.Errorf("%s holds the demo user's password hash", name)
		}
	}
	for name := range allowed {
		if !found[name] {
			t.Errorf("%s no longer holds the demo user's password hash, so this test checks nothing", name)
		}
	}
	raw, err := os.ReadFile("../../conf.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var shipped config.File
	if err := yaml.Unmarshal(raw, &shipped); err != nil {
		t.Fatal(err)
	}
	if shipped.Auth.Users != nil || shipped.Auth.OIDC != nil || shipped.Auth.TrustedIssuers != nil {
		t.Errorf("conf.yaml, which the image and the archives ship, signs someone in: %+v", shipped.Auth)
	}
}

// TestMakeStop checks that make stop removes the demo stack's containers
// and network through compose, for the stack's project only, and keeps
// its volume and images, as docs/try-it.md says. It compares the whole
// command, so that a wider removal anywhere in it fails it.
func TestMakeStop(t *testing.T) {
	want := []string{"cd docker && engine compose down --remove-orphans"}
	if got := makeDryRun(t, "stop", nil, nil); !slices.Equal(got, want) {
		t.Errorf("make stop runs\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestMakeE2E checks that the end-to-end tests run their compose
// commands on the stack that make run E2E=1 starts: make e2e names the
// compose files that make run E2E=1 starts the stack with, in the same
// order, and make run without E2E=1 names none, for the demo's own,
// docker-compose.yaml. The tests' file is occamy-v1's v1 file only
// because the override mounts it where docker-compose.yaml mounts the
// demo's, which compose then replaces.
func TestMakeE2E(t *testing.T) {
	// files returns the files that a compose command names with -f,
	// by their base names, and the command before them.
	files := func(t *testing.T, line string) (string, []string) {
		t.Helper()
		words := strings.Fields(line)
		i := slices.Index(words, "compose")
		if i < 0 {
			t.Fatalf("%q runs no compose", line)
		}
		cmd := strings.Join(words[:i+1], " ")
		var names []string
		for i++; i+1 < len(words) && words[i] == "-f"; i += 2 {
			names = append(names, filepath.Base(words[i+1]))
		}
		return cmd, names
	}
	composeLine := func(t *testing.T, args ...string) string {
		t.Helper()
		for _, line := range makeDryRun(t, "run", args, nil) {
			if strings.Contains(line, " compose ") {
				return line
			}
		}
		t.Fatalf("make -n run %s runs no compose", strings.Join(args, " "))
		return ""
	}
	if _, got := files(t, composeLine(t)); len(got) != 0 {
		t.Errorf("make run names %q, want docker-compose.yaml alone, by default", got)
	}
	_, started := files(t, composeLine(t, "E2E=1"))
	if want := []string{"docker-compose.yaml", "docker-compose.e2e.yaml"}; !slices.Equal(started, want) {
		t.Errorf("make run E2E=1 names %q, want %q", started, want)
	}
	// E2E=1 changes the files alone: the stack is published, and its
	// debug listener gated, as the demo's (TestDemoBind,
	// TestDemoDebugGating).
	for _, debug := range []string{"", "1"} {
		args := []string{"OCCAMY_DEBUG=" + debug, "OCCAMY_BIND=0.0.0.0"}
		if got, want := makeEnv(t, "run", append(args, "E2E=1"), nil), makeEnv(t, "run", args, nil); !maps.Equal(got, want) {
			t.Errorf("make run E2E=1 %s passes %v, want make run's %v", strings.Join(args, " "), got, want)
		}
	}
	// make e2e passes the command in OCCAMY_E2E_COMPOSE, with absolute
	// paths, since the tests run in tests/.
	e2e := strings.Join(makeDryRun(t, "e2e", nil, nil), "\n")
	m := regexp.MustCompile(`OCCAMY_E2E_COMPOSE="([^"]*)"`).FindStringSubmatch(e2e)
	if m == nil {
		t.Fatalf("make -n e2e sets no OCCAMY_E2E_COMPOSE: %q", e2e)
	}
	for _, word := range strings.Fields(m[1]) {
		if strings.HasSuffix(word, ".yaml") && !filepath.IsAbs(word) {
			t.Errorf("make e2e names %s, not an absolute path", word)
		}
	}
	if cmd, got := files(t, m[1]); cmd != "engine compose" || !slices.Equal(got, started) {
		t.Errorf("make e2e runs %q, want engine compose with the files of make run E2E=1, %q", m[1], started)
	}

	// The override puts the tests' file at the path where the demo
	// stack mounts its own, and touches nothing else of occamy-v1's.
	mounts := func(t *testing.T, file string) []string {
		t.Helper()
		raw, err := os.ReadFile("../../docker/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var compose struct {
			Services map[string]struct {
				Volumes []string `yaml:"volumes"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &compose); err != nil {
			t.Fatal(err)
		}
		return compose.Services["occamy-v1"].Volumes
	}
	if !slices.Contains(mounts(t, "docker-compose.yaml"), "./occamy-v1.yaml:/occamy/v1.yaml:ro") {
		t.Errorf("docker-compose.yaml does not mount occamy-v1.yaml at /occamy/v1.yaml: %q", mounts(t, "docker-compose.yaml"))
	}
	if got, want := mounts(t, "docker-compose.e2e.yaml"), []string{"./occamy-v1-e2e.yaml:/occamy/v1.yaml:ro"}; !slices.Equal(got, want) {
		t.Errorf("docker-compose.e2e.yaml mounts %q in occamy-v1, want %q", got, want)
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
		if k != "OCCAMY_BIND" && k != "OCCAMY_DEBUG" && k != "OCCAMY_IMAGE" && k != "COMPOSE_PROJECT_NAME" && k != "E2E" && k != "OCCAMY_E2E_COMPOSE" {
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
