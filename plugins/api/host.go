package api

import "time"

// Host is the node, as seen by a plugin. A plugin receives one in Init and can
// hold onto it for as long as it is enabled.
//
// Every method is scoped to what the plugin is allowed to touch. A call that
// names a server the plugin has no business with, or that the node's
// configuration forbids, returns an error rather than doing the work partially.
type Host interface {
	// Logger writes to the Wings log with the plugin's id already attached, so
	// an operator can tell whose line it is.
	Logger() Logger

	// Node describes the Wings instance the plugin is running on.
	Node() Node

	// Settings returns the plugin's own settings, as the operator has them set
	// on the Panel. Keys are the ones declared in the manifest.
	Settings() Settings

	// Store is durable per-plugin key/value storage, kept in the Wings
	// database and surviving restarts and upgrades.
	Store() Store

	// Servers lists and controls the servers on this node.
	Servers() Servers

	// Files reads and writes inside a server's data directory.
	Files() Files

	// Panel calls the Panel API as this node.
	Panel() Panel

	// HTTP performs outbound HTTP requests on the plugin's behalf.
	HTTP() Fetcher

	// Events publishes onto a server's event bus, which is what the Panel's
	// console and status displays are listening to.
	Events() Events
}

// Logger writes to the Wings log.
type Logger interface {
	Debug(msg string)
	Info(msg string)
	Warn(msg string)
	Error(msg string)

	// With returns a logger that adds the given fields to every line. Use it
	// rather than formatting values into the message.
	With(fields map[string]any) Logger
}

// Settings is a plugin's configuration, as set by the operator on the Panel.
//
// The available keys are declared in the plugin's manifest, which is what lets
// the Panel render a form for them without the plugin running. Reads always
// reflect the current values: when an operator saves new settings, Wings
// updates them in place and calls the plugin's SettingsChanged hook.
type Settings interface {
	// String returns a string setting, or fallback when it is unset or empty.
	String(key, fallback string) string

	// Int returns an integer setting, or fallback when unset or not a number.
	Int(key string, fallback int) int

	// Bool returns a boolean setting, or fallback when unset.
	Bool(key string, fallback bool) bool

	// Duration parses a setting as a Go duration such as "30s", returning
	// fallback when unset or unparseable.
	Duration(key string, fallback time.Duration) time.Duration

	// StringSlice returns a list setting, or nil when unset.
	StringSlice(key string) []string

	// All returns every setting, for a plugin that would rather decode the
	// whole map itself.
	All() map[string]any
}

// Store is durable key/value storage private to one plugin.
//
// Keys are namespaced to the plugin, so two plugins using the key "state" do
// not collide, and uninstalling a plugin discards its keys. Values are opaque
// bytes; encode structured data yourself.
type Store interface {
	Get(key string) ([]byte, bool, error)
	Set(key string, value []byte) error
	Delete(key string) error

	// Keys lists the plugin's keys carrying the given prefix. Pass an empty
	// prefix for all of them.
	Keys(prefix string) ([]string, error)

	// SetWithTTL stores a value that Wings discards once ttl has elapsed. Use
	// it for caches rather than for state you cannot rebuild.
	SetWithTTL(key string, value []byte, ttl time.Duration) error
}

// Servers lists and controls the servers on this node.
type Servers interface {
	// List returns a snapshot of every server on the node.
	List() []Server

	// Get returns one server by UUID. The second result reports whether it
	// exists on this node.
	Get(uuid string) (Server, bool)

	// Power requests a power action, blocking until Wings has accepted it
	// rather than until the server finishes transitioning. A plugin calling
	// this from inside a power hook for the same server deadlocks, so don't.
	Power(uuid string, action PowerAction) error

	// SendCommand writes a command to a running server's console.
	SendCommand(uuid, command string) error

	// ConsoleWrite prints a line to a server's console as coming from the
	// plugin, so users can see that something automated acted.
	ConsoleWrite(uuid, line string) error

	// ReadLog returns the last lines of a server's console output, newest
	// last, at most 500 lines.
	ReadLog(uuid string, lines int) ([]string, error)

	// SaveActivity records an entry in the server's activity log on the Panel,
	// attributed to the plugin. Use it for actions an operator would want to
	// find later.
	SaveActivity(uuid, event string, meta map[string]any) error
}

// Files reads and writes inside a server's data directory.
//
// Every path is relative to that directory and cannot escape it: a path that
// resolves outside, or that the egg's file denylist covers, returns an error.
// Writes count against the server's disk limit exactly as a user's would.
type Files interface {
	Read(uuid, path string) ([]byte, error)
	Write(uuid, path string, content []byte) error
	Delete(uuid, path string) error
	Rename(uuid, from, to string) error
	CreateDirectory(uuid, path string) error
	Chmod(uuid, path string, mode uint32) error
	List(uuid, path string) ([]DirEntry, error)

	// Exists reports whether a path exists, without reading it.
	Exists(uuid, path string) (bool, error)
}

// Panel calls the Panel API using the node's own credentials.
//
// Wings does not interpret the response, so a plugin can reach endpoints its
// Panel-side counterpart added. Requests go to the Panel this node is attached
// to; path is relative to the Panel root, such as "/api/application/servers".
type Panel interface {
	// Request performs an HTTP call against the Panel. It returns the status
	// code and body. A non-2xx status is returned rather than turned into an
	// error, so the caller decides what counts as failure.
	Request(method, path string, body []byte, header map[string]string) (int, []byte, error)
}

// Fetcher performs outbound HTTP requests for a plugin.
//
// A plugin cannot open a socket of its own: neither net nor net/http is in the
// symbol table its source is interpreted against. This is the way out, which
// means every call a plugin makes off the node is attributable to it in the
// logs, and an operator can narrow what is reachable with the plugins
// allowed_hosts setting in the Wings config.
type Fetcher interface {
	// Do performs the request and returns the status code and body. A non-2xx
	// status is returned rather than turned into an error, so the caller
	// decides what counts as failure. Redirects are followed; the body is
	// capped, and a response over the cap is an error rather than a truncated
	// body that would parse as something else.
	Do(method, url string, body []byte, header map[string]string) (int, []byte, error)
}

// Events publishes onto a server's event bus.
//
// These are the events the Panel's websocket forwards to a browser, so a plugin
// publishing here can drive its own UI in the Panel without adding an endpoint.
type Events interface {
	// Publish sends an event on a server's bus. Prefix the topic with the
	// plugin's id so it cannot collide with a Wings event; Wings rejects a
	// topic that shadows a built-in one.
	Publish(uuid, topic string, data any) error
}
