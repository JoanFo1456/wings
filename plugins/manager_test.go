package plugins

import (
	"strings"
	"testing"

	"github.com/pelican/wings/plugins/api"
)

// trashManifest and trashSource are a cut-down recycle bin: enough to exercise
// the gate's three outcomes without the real plugin's indexing.
const trashManifest = `{
    "id": "recycler",
    "name": "Recycler",
    "author": "Tests",
    "version": "1.0.0",
    "category": "plugin",
    "package": "recycler",
    "settings": [
        {"key": "directory", "type": "string", "default": ".trash"},
        {"key": "enabled", "type": "boolean", "default": true}
    ],
    "meta": {"status": "not_installed", "load_order": 0}
}`

const trashSource = `
package recycler

import (
	"path"
	"strings"

	"github.com/pelican/wings/plugins/api"
)

type plugin struct {
	host api.Host
}

func (p *plugin) init(h api.Host) error {
	p.host = h
	return nil
}

func (p *plugin) directory() string {
	return "/" + strings.Trim(p.host.Settings().String("directory", ".trash"), "/")
}

func (p *plugin) beforeFile(e api.FileEvent) api.FileDirective {
	if e.Action != api.FileDelete {
		return api.AllowFile()
	}

	dir := p.directory()
	if e.Path == dir || strings.HasPrefix(e.Path, dir+"/") {
		return api.AllowFile()
	}

	if !p.host.Settings().Bool("enabled", true) {
		return api.AllowFile()
	}

	target := path.Join(dir, strings.TrimPrefix(e.Path, "/"))

	if parent := path.Dir(target); parent != "/" && parent != "." {
		if err := p.host.Files().CreateDirectory(e.Server.UUID, parent); err != nil {
			return api.DenyFile("could not prepare the trash: " + err.Error())
		}
	}
	if err := p.host.Files().Rename(e.Server.UUID, e.Path, target); err != nil {
		return api.DenyFile("could not move this into the trash: " + err.Error())
	}

	return api.FileHandled()
}

func (p *plugin) list(r api.Request) api.Response {
	return api.Text(200, "trash for "+r.Server.Name)
}

func New() api.Registration {
	p := &plugin{}
	return api.Registration{
		Init:             p.init,
		BeforeFileAction: p.beforeFile,
		Routes: []api.Route{
			{Method: "GET", Path: "/", ServerScoped: true, Handler: p.list},
		},
	}
}
`

// enableTestPlugin writes a plugin, discovers it and installs it enabled.
func enableTestPlugin(t *testing.T, m *Manager, root, id, manifest, source string) *Instance {
	t.Helper()

	writePlugin(t, root, id, manifest, source)

	if err := m.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	inst, ok := m.Get(id)
	if !ok {
		t.Fatalf("plugin %q was not discovered", id)
	}
	if got := inst.Status(); got != StatusNotInstalled {
		t.Fatalf("a freshly discovered plugin should be not_installed, got %q", got)
	}

	if err := m.Install(id, true); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := inst.Status(); got != StatusEnabled {
		t.Fatalf("after installing with enable the status should be enabled, got %q (%s)",
			got, inst.Manifest().Meta.StatusMessage)
	}
	if !inst.Loaded() {
		t.Fatal("an enabled plugin should be loaded")
	}

	return inst
}

func TestPluginLoadsFromSourceAndHandlesDeletes(t *testing.T) {
	bridge := newFakeBridge()
	server := testServer()
	bridge.addServer(server)
	bridge.putFile(server.UUID, "/plugins/foo.jar", []byte("jar contents"))

	m, root := newTestManager(t, bridge)
	enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	// A delete is taken over by the plugin: allowed, reported as handled, and
	// the file has moved rather than gone.
	allow, handled, reason := GateFileAction(api.FileEvent{
		Server: server,
		Action: api.FileDelete,
		Path:   "/plugins/foo.jar",
		Size:   12,
		User:   "operator",
	})

	if !allow {
		t.Fatalf("the delete should have been allowed, but was refused: %s", reason)
	}
	if !handled {
		t.Fatal("the plugin should have reported the delete as handled, so Wings skips it")
	}

	paths := bridge.paths(server.UUID)
	if len(paths) != 1 || paths[0] != "/.trash/plugins/foo.jar" {
		t.Fatalf("the file should have moved into the mirrored trash path, got %v", paths)
	}

	content, err := bridge.FileRead(server.UUID, "/.trash/plugins/foo.jar")
	if err != nil {
		t.Fatalf("the trashed file should be readable: %v", err)
	}
	if string(content) != "jar contents" {
		t.Fatalf("the trashed file's contents changed: %q", content)
	}
}

func TestDeleteInsideTrashIsNotIntercepted(t *testing.T) {
	bridge := newFakeBridge()
	server := testServer()
	bridge.addServer(server)
	bridge.putFile(server.UUID, "/.trash/old.jar", []byte("old"))

	m, root := newTestManager(t, bridge)
	enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	// Emptying the trash has to keep working, so a delete inside it must fall
	// through to Wings rather than being moved again.
	allow, handled, reason := GateFileAction(api.FileEvent{
		Server: server,
		Action: api.FileDelete,
		Path:   "/.trash/old.jar",
	})

	if !allow {
		t.Fatalf("deleting from inside the trash should be allowed: %s", reason)
	}
	if handled {
		t.Fatal("deleting from inside the trash should not be handled by the plugin, or the trash could never be emptied")
	}
}

func TestNonDeleteActionsPassStraightThrough(t *testing.T) {
	bridge := newFakeBridge()
	server := testServer()
	bridge.addServer(server)
	bridge.putFile(server.UUID, "/server.properties", []byte("motd=hi"))

	m, root := newTestManager(t, bridge)
	enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	for _, action := range []api.FileAction{api.FileWrite, api.FileRename, api.FileChmod, api.FileRead} {
		allow, handled, reason := GateFileAction(api.FileEvent{
			Server: server,
			Action: action,
			Path:   "/server.properties",
		})
		if !allow || handled {
			t.Fatalf("%s should pass through untouched, got allow=%v handled=%v reason=%q",
				action, allow, handled, reason)
		}
	}
}

func TestGateDenialIsAttributedToThePlugin(t *testing.T) {
	bridge := newFakeBridge()
	server := testServer()
	bridge.addServer(server)
	// No file on disk, so the plugin's rename fails and it denies.
	m, root := newTestManager(t, bridge)
	enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	allow, handled, reason := GateFileAction(api.FileEvent{
		Server: server,
		Action: api.FileDelete,
		Path:   "/missing.jar",
	})

	if allow {
		t.Fatal("the plugin denied the delete, so it should not be allowed")
	}
	if handled {
		t.Fatal("a denied action is not a handled one")
	}
	// The user needs to know which plugin stopped them.
	if !strings.HasPrefix(reason, "Recycler: ") {
		t.Fatalf("the denial should name the plugin, got %q", reason)
	}
}

func TestDisableStopsDispatchWithoutRestart(t *testing.T) {
	bridge := newFakeBridge()
	server := testServer()
	bridge.addServer(server)
	bridge.putFile(server.UUID, "/a.jar", []byte("a"))
	bridge.putFile(server.UUID, "/b.jar", []byte("b"))

	m, root := newTestManager(t, bridge)
	inst := enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	if _, handled, _ := GateFileAction(api.FileEvent{
		Server: server, Action: api.FileDelete, Path: "/a.jar",
	}); !handled {
		t.Fatal("the enabled plugin should handle the delete")
	}

	if err := m.Disable("recycler"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := inst.Status(); got != StatusDisabled {
		t.Fatalf("status after Disable should be disabled, got %q", got)
	}
	if inst.Loaded() {
		t.Fatal("a disabled plugin should not still be loaded")
	}

	// The whole point of interpreting plugins rather than linking them is that
	// this takes effect now, with no restart.
	if _, handled, _ := GateFileAction(api.FileEvent{
		Server: server, Action: api.FileDelete, Path: "/b.jar",
	}); handled {
		t.Fatal("a disabled plugin must not receive hooks any more")
	}

	// And enabling it again brings it back.
	if err := m.Enable("recycler"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, handled, _ := GateFileAction(api.FileEvent{
		Server: server, Action: api.FileDelete, Path: "/b.jar",
	}); !handled {
		t.Fatal("a re-enabled plugin should receive hooks again")
	}
}

func TestSettingsAreReadLiveFromTheManifest(t *testing.T) {
	bridge := newFakeBridge()
	server := testServer()
	bridge.addServer(server)
	bridge.putFile(server.UUID, "/a.jar", []byte("a"))
	bridge.putFile(server.UUID, "/b.jar", []byte("b"))

	m, root := newTestManager(t, bridge)
	enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	// Point the plugin at a different folder and confirm the next delete lands
	// there, without the plugin being reloaded.
	if err := m.SaveSettings("recycler", map[string]any{"directory": ".bin"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	if _, handled, _ := GateFileAction(api.FileEvent{
		Server: server, Action: api.FileDelete, Path: "/a.jar",
	}); !handled {
		t.Fatal("the delete should still be handled after a settings change")
	}

	if _, err := bridge.FileRead(server.UUID, "/.bin/a.jar"); err != nil {
		t.Fatalf("the new setting should have been used, but /.bin/a.jar is missing: %v", err)
	}

	// Turning the plugin off through its own settings leaves it enabled but
	// makes it decline to act, which is different from disabling it.
	if err := m.SaveSettings("recycler", map[string]any{"enabled": false}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if _, handled, _ := GateFileAction(api.FileEvent{
		Server: server, Action: api.FileDelete, Path: "/b.jar",
	}); handled {
		t.Fatal("with its enabled setting off the plugin should decline to handle deletes")
	}
}

func TestSaveSettingsRejectsUndeclaredKeys(t *testing.T) {
	bridge := newFakeBridge()
	m, root := newTestManager(t, bridge)
	enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	err := m.SaveSettings("recycler", map[string]any{"not_a_real_setting": 1})
	if err == nil {
		t.Fatal("saving a setting the manifest does not declare should fail")
	}
	if !strings.Contains(err.Error(), "not_a_real_setting") {
		t.Fatalf("the error should name the offending key, got %q", err)
	}
}

func TestServerScopedRouteIsResolvedAndInvoked(t *testing.T) {
	bridge := newFakeBridge()
	server := testServer()
	bridge.addServer(server)

	m, root := newTestManager(t, bridge)
	inst := enableTestPlugin(t, m, root, "recycler", trashManifest, trashSource)

	found, route, _, ok := m.FindRoute("recycler", "GET", "/", true)
	if !ok {
		t.Fatal("the plugin's server scoped route should be resolvable")
	}
	if found != inst {
		t.Fatal("the route resolved to the wrong plugin")
	}

	res := route.Handler(api.Request{
		Method:       "GET",
		Path:         "/",
		Server:       server,
		ServerScoped: true,
	})
	if res.Status != 200 {
		t.Fatalf("the handler should have returned 200, got %d", res.Status)
	}
	if string(res.Body) != "trash for Survival" {
		t.Fatalf("the handler got the wrong server, body was %q", res.Body)
	}

	// A node scoped lookup must not find a server scoped route.
	if _, _, _, ok := m.FindRoute("recycler", "GET", "/", false); ok {
		t.Fatal("a server scoped route should not answer a node scoped request")
	}

	// And a disabled plugin answers nothing.
	if err := m.Disable("recycler"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, _, _, ok := m.FindRoute("recycler", "GET", "/", true); ok {
		t.Fatal("a disabled plugin's routes should no longer resolve")
	}
}
