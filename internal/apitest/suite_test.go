package apitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// suite is the one disposable API server shared by the tests in this package. It is nil unless
// AGENTPLANE_ENVTEST=1, in which case every test is skipped with an explicit message.
var suite *testSuite

type testSuite struct {
	cfg      *rest.Config
	http     *http.Client
	ext      *apiextensionsclient.Clientset
	k8sMinor string // "1.35" or "1.37", as reported by the running API server
	counter  atomic.Int64
}

func TestMain(m *testing.M) { os.Exit(run(m)) }

// run starts the disposable API server (when asked to), runs the tests and stops it again. The stop
// is registered before Start, runs on every exit path, and a stop error fails the run.
func run(m *testing.M) (code int) {
	if os.Getenv("AGENTPLANE_ENVTEST") != "1" {
		return m.Run()
	}
	if v := os.Getenv("USE_EXISTING_CLUSTER"); v != "" && v != "false" {
		fmt.Fprintf(os.Stderr, "refusing to run: USE_EXISTING_CLUSTER=%q; these tests only use a disposable local API server\n", v)
		return 1
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		fmt.Fprintln(os.Stderr, "KUBEBUILDER_ASSETS is not set; run these tests through make test-envtest")
		return 1
	}
	want := os.Getenv("ENVTEST_K8S_VERSION")
	if want != "1.35.0" && want != "1.37.0" {
		fmt.Fprintf(os.Stderr, "ENVTEST_K8S_VERSION must be 1.35.0 or 1.37.0, got %q\n", want)
		return 1
	}
	// Never let a user kubeconfig be discovered by anything below.
	_ = os.Unsetenv("KUBECONFIG")
	_ = os.Unsetenv("KUBERNETES_SERVICE_HOST")

	env := &envtest.Environment{
		CRDDirectoryPaths:        []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing:    true,
		BinaryAssetsDirectory:    assets,
		UseExistingCluster:       ptr.To(false),
		ControlPlaneStartTimeout: 90 * time.Second,
		ControlPlaneStopTimeout:  90 * time.Second,
	}
	env.CRDInstallOptions.MaxTime = 60 * time.Second
	defer func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: stopping the envtest API server: %v\n", err)
			code = 1
		}
	}()
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: starting the envtest API server: %v\n", err)
		return 1
	}
	s, err := newSuite(cfg, want)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		return 1
	}
	suite = s
	return m.Run()
}

func newSuite(cfg *rest.Config, wantVersion string) (*testSuite, error) {
	u, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("parsing the API server address %q: %w", cfg.Host, err)
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return nil, fmt.Errorf("the API server address %q is not loopback; refusing to continue", cfg.Host)
	}
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	hc.Timeout = 30 * time.Second
	ext, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	sv, err := dc.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("reading the API server version: %w", err)
	}
	got, err := version.ParseGeneric(sv.GitVersion)
	if err != nil {
		return nil, fmt.Errorf("parsing server version %q: %w", sv.GitVersion, err)
	}
	minor := fmt.Sprintf("%d.%d", got.Major(), got.Minor())
	if !strings.HasPrefix(wantVersion, minor+".") {
		return nil, fmt.Errorf("the API server reports Kubernetes %s but ENVTEST_K8S_VERSION is %s", sv.GitVersion, wantVersion)
	}
	return &testSuite{cfg: cfg, http: hc, ext: ext, k8sMinor: minor}, nil
}

// need returns the shared suite or skips the test with an explicit reason.
func need(t *testing.T) *testSuite {
	t.Helper()
	if suite == nil {
		t.Skip("envtest suite not started: run through make test-envtest (AGENTPLANE_ENVTEST=1)")
	}
	return suite
}

func (s *testSuite) unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, s.counter.Add(1))
}

type response struct {
	Code int
	Body []byte
}

// do sends a raw request: the body is exactly the bytes given, so a missing or out-of-range field in
// a test is really missing or out of range on the wire (a typed Go value would serialize a zero).
func (s *testSuite) do(t *testing.T, method, path, contentType, accept string, body []byte) response {
	t.Helper()
	req, err := http.NewRequest(method, strings.TrimSuffix(s.cfg.Host, "/")+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{Code: resp.StatusCode, Body: b}
}

func (s *testSuite) postJSON(t *testing.T, path string, body any) response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return s.do(t, http.MethodPost, path, "application/json", "application/json", b)
}

func (s *testSuite) get(t *testing.T, path string) response {
	t.Helper()
	return s.do(t, http.MethodGet, path, "", "application/json", nil)
}

func (s *testSuite) put(t *testing.T, path string, body any) response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return s.do(t, http.MethodPut, path, "application/json", "application/json", b)
}

func (s *testSuite) delete(t *testing.T, path string) {
	t.Helper()
	r := s.do(t, http.MethodDelete, path, "", "application/json", nil)
	if r.Code != http.StatusOK && r.Code != http.StatusNotFound {
		t.Errorf("cleanup DELETE %s returned %d: %s", path, r.Code, r.Body)
	}
}

// decode parses a response body keeping integers exact (json.Number), so a value such as
// 9223372036854775807 survives and an explicit 0 is distinguishable from an absent field.
func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decoding %q: %v", b, err)
	}
	return m
}

func status(t *testing.T, r response) *metav1.Status {
	t.Helper()
	var st metav1.Status
	if err := json.Unmarshal(r.Body, &st); err != nil || st.Kind != "Status" {
		t.Fatalf("response is not a Status (code %d): %s", r.Code, r.Body)
	}
	return &st
}

// requireRejected asserts a 422 Invalid whose causes name every wanted field path.
func requireRejected(t *testing.T, r response, wantFields ...string) {
	t.Helper()
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 Invalid, got %d: %s", r.Code, r.Body)
	}
	st := status(t, r)
	if st.Reason != metav1.StatusReasonInvalid {
		t.Fatalf("want reason Invalid, got %q: %s", st.Reason, r.Body)
	}
	have := map[string]bool{}
	if st.Details != nil {
		for _, c := range st.Details.Causes {
			have[c.Field] = true
		}
	}
	for _, f := range wantFields {
		if !have[f] {
			t.Errorf("no cause names field %q; causes: %s", f, r.Body)
		}
	}
}

func requireCreated(t *testing.T, r response) map[string]any {
	t.Helper()
	if r.Code != http.StatusCreated {
		t.Fatalf("want 201 Created, got %d: %s", r.Code, r.Body)
	}
	return decode(t, r.Body)
}

// dig walks nested maps and reports whether the path exists (absent differs from present-but-zero).
func dig(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// without returns a deep copy of obj with the dotted path removed.
func without(obj map[string]any, path ...string) map[string]any {
	c := clone(obj)
	cur := c
	for _, p := range path[:len(path)-1] {
		cur = cur[p].(map[string]any)
	}
	delete(cur, path[len(path)-1])
	return c
}

// with returns a deep copy of obj with the dotted path set to v.
func with(obj map[string]any, v any, path ...string) map[string]any {
	c := clone(obj)
	cur := c
	for _, p := range path[:len(path)-1] {
		cur = cur[p].(map[string]any)
	}
	cur[path[len(path)-1]] = v
	return c
}

func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out map[string]any
	_ = dec.Decode(&out)
	return out
}
