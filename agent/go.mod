module printmaster/agent

go 1.27.3

require (
	github.com/Masterminds/semver/v3 v3.5.0
	github.com/gorilla/websocket v1.5.3
	github.com/gosnmp/gosnmp v1.47.0
	github.com/grandcat/zeroconf v1.0.0
	github.com/kardianos/service v1.3.0
	golang.org/x/net v0.61.0
	golang.org/x/sys v0.49.0
	modernc.org/sqlite v1.61.0
	printmaster/common v0.0.0
)

replace printmaster/common => ../common

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/go-containerregistry v0.22.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/miekg/dns v1.1.27 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/secure-systems-lab/go-securesystemslib v0.11.1 // indirect
	github.com/sigstore/protobuf-specs v0.5.2 // indirect
	github.com/sigstore/sigstore v1.11.1 // indirect
	github.com/theupdateframework/go-tuf/v2 v2.4.2 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260727163830-6c54dddc4772 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
