package plugins

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/internal/models"
	"github.com/pelican/wings/plugins/api"
)

// fakeBridge stands in for the rest of Wings. Files live in a map rather than
// on disk so a test can assert on exactly what a plugin did, and so a plugin
// that tries to escape a server's directory fails here the way it would in the
// real bridge.
type fakeBridge struct {
	mu sync.Mutex

	servers map[string]api.Server

	// files maps server uuid to a path/content map. Directories are recorded
	// as entries with a nil value, which is enough to make Exists and List
	// behave.
	files map[string]map[string][]byte

	// calls records what the plugin asked the node to do, in order, so a test
	// can assert on the sequence rather than only the end state.
	calls []string

	powered   []string
	commands  []string
	console   []string
	activity  []string
	published []string
}

func newFakeBridge() *fakeBridge {
	return &fakeBridge{
		servers: map[string]api.Server{},
		files:   map[string]map[string][]byte{},
	}
}

func (b *fakeBridge) addServer(s api.Server) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.servers[s.UUID] = s
	if _, ok := b.files[s.UUID]; !ok {
		b.files[s.UUID] = map[string][]byte{}
	}
}

func (b *fakeBridge) putFile(uuid, p string, content []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files[uuid] == nil {
		b.files[uuid] = map[string][]byte{}
	}
	b.files[uuid][normalize(p)] = content
}

func (b *fakeBridge) paths(uuid string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]string, 0, len(b.files[uuid]))
	for p, content := range b.files[uuid] {
		if content == nil {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (b *fakeBridge) record(call string) {
	b.mu.Lock()
	b.calls = append(b.calls, call)
	b.mu.Unlock()
}

// normalize gives every path a single leading slash and no trailing one, so
// "/a/b", "a/b" and "/a/b/" are one key.
func normalize(p string) string {
	p = path.Clean("/" + strings.TrimSpace(p))
	return p
}

func (b *fakeBridge) Node() api.Node {
	return api.Node{UUID: "node-1", Version: "1.0.0", OS: "linux", Architecture: "amd64"}
}

func (b *fakeBridge) ListServers() []api.Server {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]api.Server, 0, len(b.servers))
	for _, s := range b.servers {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	return out
}

func (b *fakeBridge) GetServer(uuid string) (api.Server, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.servers[uuid]
	return s, ok
}

func (b *fakeBridge) Power(uuid string, action api.PowerAction) error {
	b.record("power " + uuid + " " + string(action))
	b.mu.Lock()
	b.powered = append(b.powered, uuid+":"+string(action))
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) SendCommand(uuid, command string) error {
	b.record("command " + uuid + " " + command)
	b.mu.Lock()
	b.commands = append(b.commands, command)
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) ConsoleWrite(uuid, line string) error {
	b.mu.Lock()
	b.console = append(b.console, line)
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) ReadLog(uuid string, lines int) ([]string, error) {
	return []string{"log line"}, nil
}

func (b *fakeBridge) SaveActivity(uuid, pluginID, event string, meta map[string]any) error {
	b.mu.Lock()
	b.activity = append(b.activity, pluginID+":"+event)
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) PublishEvent(uuid, topic string, data any) error {
	b.mu.Lock()
	b.published = append(b.published, topic)
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) FileRead(uuid, p string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	content, ok := b.files[uuid][normalize(p)]
	if !ok || content == nil {
		return nil, os.ErrNotExist
	}
	return content, nil
}

func (b *fakeBridge) FileWrite(uuid, p string, content []byte) error {
	b.record("write " + p)
	b.putFile(uuid, p, content)
	return nil
}

func (b *fakeBridge) FileDelete(uuid, p string) error {
	b.record("delete " + p)

	b.mu.Lock()
	defer b.mu.Unlock()

	key := normalize(p)
	if _, ok := b.files[uuid][key]; !ok {
		return os.ErrNotExist
	}
	// Delete the entry and anything beneath it, the way a recursive delete
	// behaves.
	for existing := range b.files[uuid] {
		if existing == key || strings.HasPrefix(existing, key+"/") {
			delete(b.files[uuid], existing)
		}
	}
	return nil
}

func (b *fakeBridge) FileRename(uuid, from, to string) error {
	b.record("rename " + from + " -> " + to)

	b.mu.Lock()
	defer b.mu.Unlock()

	src, dst := normalize(from), normalize(to)
	content, ok := b.files[uuid][src]
	if !ok {
		return os.ErrNotExist
	}
	if _, taken := b.files[uuid][dst]; taken {
		return os.ErrExist
	}

	delete(b.files[uuid], src)
	b.files[uuid][dst] = content
	return nil
}

func (b *fakeBridge) FileCreateDirectory(uuid, p string) error {
	b.record("mkdir " + p)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.files[uuid] == nil {
		b.files[uuid] = map[string][]byte{}
	}
	// Record every ancestor, matching the recursive create the real
	// filesystem performs.
	parts := strings.Split(strings.Trim(normalize(p), "/"), "/")
	for i := range parts {
		b.files[uuid]["/"+strings.Join(parts[:i+1], "/")] = nil
	}
	return nil
}

func (b *fakeBridge) FileChmod(uuid, p string, mode uint32) error { return nil }

func (b *fakeBridge) FileList(uuid, p string) ([]api.DirEntry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	dir := normalize(p)
	if _, ok := b.files[uuid][dir]; !ok && dir != "/" {
		return nil, os.ErrNotExist
	}

	seen := map[string]bool{}
	var out []api.DirEntry

	prefix := dir
	if prefix != "/" {
		prefix += "/"
	}

	for existing, content := range b.files[uuid] {
		if existing == dir || !strings.HasPrefix(existing, prefix) {
			continue
		}
		name := strings.SplitN(strings.TrimPrefix(existing, prefix), "/", 2)[0]
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		out = append(out, api.DirEntry{
			Name:      name,
			Size:      int64(len(content)),
			Directory: content == nil,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (b *fakeBridge) FileExists(uuid, p string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.files[uuid][normalize(p)]
	return ok, nil
}

func (b *fakeBridge) PanelRequest(method, p string, body []byte, header map[string]string) (int, []byte, error) {
	b.record("panel " + method + " " + p)
	return 200, []byte(`{}`), nil
}

var _ Bridge = (*fakeBridge)(nil)

// testDB returns an in-memory database with the plugin tables migrated.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("could not open the test database: %v", err)
	}
	if err := db.AutoMigrate(&models.PluginStore{}); err != nil {
		t.Fatalf("could not migrate the test database: %v", err)
	}
	// One connection, so the shared in-memory database is not torn down
	// between statements.
	if sql, err := db.DB(); err == nil {
		sql.SetMaxOpenConns(1)
	}
	return db
}

// writePlugin puts a plugin on disk in dir and returns the plugin directory.
func writePlugin(t *testing.T, root, id, manifest, source string) string {
	t.Helper()

	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("could not create the plugin directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("could not write plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".go"), []byte(source), 0o644); err != nil {
		t.Fatalf("could not write the plugin source: %v", err)
	}
	return dir
}

// newTestManager builds a manager over a fresh temporary plugin directory.
func newTestManager(t *testing.T, bridge Bridge) (*Manager, string) {
	t.Helper()

	root := t.TempDir()

	m := NewManager(config.PluginConfiguration{
		Enabled:         true,
		Directory:       root,
		GateTimeout:     2,
		RequestTimeout:  5,
		MaxResponseSize: 1,
		MaxInstallSize:  4,
	}, bridge, testDB(t))

	// Dispatch reads a package level pointer, so a test that dispatches has
	// to install its manager and put it back afterwards.
	SetManager(m)
	t.Cleanup(func() { SetManager(nil) })

	return m, root
}

// testServer is the server every fixture plugin sees.
func testServer() api.Server {
	return api.Server{
		UUID:  "11111111-1111-1111-1111-111111111111",
		ID:    1,
		Name:  "Survival",
		State: api.StateRunning,
		Image: "ghcr.io/pelican-eggs/java:21",
		EggID: "egg-1",
	}
}
