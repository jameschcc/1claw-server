module 1claw-server

go 1.22

require (
	github.com/gorilla/mux v1.8.1
	github.com/gorilla/websocket v1.5.3
	github.com/mattn/go-sqlite3 v1.14.19
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/mattn/go-sqlite3 => /usr/share/gocode/src/github.com/mattn/go-sqlite3
