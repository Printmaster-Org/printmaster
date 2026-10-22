module printmaster/tests

go 1.27.3

require github.com/gorilla/websocket v1.5.3

replace printmaster/agent => ../agent

replace printmaster/server => ../server

replace printmaster/common => ../common
